package social

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// hnBaseURL is the official Hacker News Firebase API root. It is fully public
// and needs no authentication, which makes HN an ideal second credential-free
// adapter (docs/08 §5) — it exercises a different shape than Mastodon: a story
// feed is a flat array of item ids, and every story/comment is fetched as its
// own item object rather than arriving in one timeline page.
const hnBaseURL = "https://hacker-news.firebaseio.com/v0"

// hnFeeds maps friendly seed names to the API's story-list endpoints. Each list
// returns an ordered array of item ids (up to 500 for the ranked feeds).
var hnFeeds = map[string]string{
	"top":  "topstories",
	"new":  "newstories",
	"best": "beststories",
	"ask":  "askstories",
	"show": "showstories",
	"job":  "jobstories",
}

// HackerNews ingests stories and comments from the Hacker News Firebase API.
// Unlike Mastodon (one timeline page → many posts), HN resolves a feed name to
// item ids at Discover time and fetches each item individually, so Parse turns a
// single item object into a single normalized doc.
type HackerNews struct {
	client    *http.Client
	userAgent string
	limit     int // max items expanded from a feed per Discover call
	health    *HealthTracker
}

// NewHackerNews builds a Hacker News adapter. userAgent should identify the
// crawler; limit caps how many items a feed seed expands to (<=0 → 30, the
// front-page size).
func NewHackerNews(userAgent string, timeout time.Duration, limit int) *HackerNews {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if limit <= 0 {
		limit = 30
	}
	return &HackerNews{
		client:    &http.Client{Timeout: timeout},
		userAgent: userAgent,
		limit:     limit,
		health:    NewHealthTracker("hackernews", 0.5, 5),
	}
}

func (h *HackerNews) Name() string { return "hackernews" }

func (h *HackerNews) Health() Health { return h.health.Status() }

// Discover turns a seed into concrete item-fetch targets. Supported seeds:
//   - a feed name ("top", "new", "best", "ask", "show", "job") → the first
//     `limit` item URLs from that story list (requires one API round-trip);
//   - a numeric item id → that item's URL;
//   - a full HN item API URL → passed through unchanged.
func (h *HackerNews) Discover(ctx context.Context, seed string) ([]string, error) {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return nil, fmt.Errorf("hackernews: empty seed")
	}
	// Already an item endpoint — use as-is.
	if strings.Contains(seed, "/v0/item/") {
		return []string{seed}, nil
	}
	// A bare numeric id.
	if isAllDigits(seed) {
		return []string{itemURL(seed)}, nil
	}
	// Otherwise treat it as a feed name (tolerate the "topstories" form too).
	name := strings.ToLower(strings.TrimSuffix(seed, "stories"))
	list, ok := hnFeeds[name]
	if !ok {
		return nil, fmt.Errorf("hackernews: unknown seed %q (want a feed name, item id, or item URL)", seed)
	}
	ids, err := h.fetchIDs(ctx, hnBaseURL+"/"+list+".json")
	if err != nil {
		return nil, err
	}
	if len(ids) > h.limit {
		ids = ids[:h.limit]
	}
	targets := make([]string, 0, len(ids))
	for _, id := range ids {
		targets = append(targets, itemURL(strconv.FormatInt(id, 10)))
	}
	return targets, nil
}

// fetchIDs retrieves a story-list endpoint (an array of item ids). The outcome
// is recorded on the health tracker so a failing feed surfaces in monitoring.
func (h *HackerNews) fetchIDs(ctx context.Context, target string) ([]int64, error) {
	raw, err := h.Fetch(ctx, target)
	if err != nil {
		return nil, err
	}
	var ids []int64
	if err := json.Unmarshal(raw.Body, &ids); err != nil {
		return nil, fmt.Errorf("hackernews: decode story list: %w", err)
	}
	return ids, nil
}

// Fetch retrieves a target (a story list or a single item). HN feeds do not
// paginate via cursor, so Next is always empty.
func (h *HackerNews) Fetch(ctx context.Context, target string) (*RawContent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		h.health.RecordFetch(err)
		return nil, err
	}
	req.Header.Set("User-Agent", h.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		h.health.RecordFetch(err)
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap
	if err != nil {
		h.health.RecordFetch(err)
		return nil, err
	}
	if resp.StatusCode >= 400 {
		err = fmt.Errorf("hackernews: %s returned %d", target, resp.StatusCode)
		h.health.RecordFetch(err)
		return nil, err
	}
	h.health.RecordFetch(nil)

	return &RawContent{
		Target:      target,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
	}, nil
}

// Paginate always ends: a feed is expanded to its item targets at Discover time,
// and an individual item has no successor cursor.
func (h *HackerNews) Paginate(_ *RawContent) (string, bool) { return "", false }

// hnItem is the subset of a Hacker News item we consume.
// https://github.com/HackerNews/API#items
type hnItem struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"` // "story", "comment", "job", "poll", "pollopt"
	By          string `json:"by"`
	Time        int64  `json:"time"` // unix seconds
	Text        string `json:"text"` // HTML (comments, Ask/Show HN, jobs)
	Title       string `json:"title"`
	URL         string `json:"url"` // external link for link-stories
	Parent      int64  `json:"parent"`
	Score       int    `json:"score"`
	Descendants int    `json:"descendants"`
	Deleted     bool   `json:"deleted"`
	Dead        bool   `json:"dead"`
}

// Parse turns a single HN item object into a normalized doc. Deleted, dead, and
// text-less items (e.g. a pure link story with no title, or a moderated comment)
// are dropped. Stories carry score/descendants engagement; comments carry their
// parent id so threads reconstruct downstream.
func (h *HackerNews) Parse(raw *RawContent) ([]NormalizedDoc, error) {
	if raw == nil {
		return nil, fmt.Errorf("hackernews: nil raw content")
	}
	// The API returns the JSON literal `null` for a missing/expired item.
	if len(raw.Body) == 0 || string(raw.Body) == "null" {
		return nil, nil
	}
	var it hnItem
	if err := json.Unmarshal(raw.Body, &it); err != nil {
		return nil, fmt.Errorf("hackernews: decode item: %w", err)
	}
	if it.ID == 0 || it.Deleted || it.Dead {
		return nil, nil
	}

	body := htmlToText(it.Text)
	title := strings.TrimSpace(it.Title)
	// Text is title + body so a story's headline is searchable even when its
	// only "content" is a linked article, and Ask/Show HN carry both.
	text := strings.TrimSpace(title + " " + body)
	if text == "" {
		return nil, nil
	}

	id := strconv.FormatInt(it.ID, 10)
	engagement := map[string]int{}
	if it.Type != "comment" { // stories/jobs/polls carry ranking signals
		engagement["score"] = it.Score
		engagement["descendants"] = it.Descendants
	}

	var media []string
	if it.URL != "" { // the external article a link-story points at
		media = []string{it.URL}
	}

	d := NormalizedDoc{
		Platform:     "hackernews",
		PostID:       id,
		AuthorHandle: it.By,
		PostedAt:     time.Unix(it.Time, 0).UTC(),
		Permalink:    "https://news.ycombinator.com/item?id=" + id,
		ParentID:     parentID(it.Parent),
		Title:        hnTitle(title, it.By, text),
		Text:         text,
		Lang:         resolveLang("", text),
		MediaURLs:    media,
		Engagement:   engagement,
	}
	d.finalize()
	h.health.RecordItems(1)
	return []NormalizedDoc{d}, nil
}

// --- helpers ---------------------------------------------------------------

func itemURL(id string) string { return hnBaseURL + "/item/" + id + ".json" }

func parentID(p int64) string {
	if p == 0 {
		return ""
	}
	return strconv.FormatInt(p, 10)
}

// hnTitle prefers a story's real title; comments have none, so it falls back to
// the "@handle: snippet" form used across adapters.
func hnTitle(title, handle, text string) string {
	if title != "" {
		return title
	}
	return makeTitle(handle, text)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
