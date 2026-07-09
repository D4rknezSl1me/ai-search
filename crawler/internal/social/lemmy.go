package social

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Lemmy ingests public posts and comments from any Lemmy instance — the
// fediverse's link-aggregator (Reddit-shaped). Its v3 REST API serves public
// listings with **no authentication** (docs/08 §5: "open APIs; easy,
// high-quality"), so like Mastodon it is credential-free and needs no browser
// automation. It is the third open adapter and exercises a third pipeline
// shape: a page-numbered community/instance listing whose response is an
// envelope of PostView/CommentView objects (post + creator + community +
// counts), with Markdown bodies and materialized-path comment threading.
type Lemmy struct {
	client    *http.Client
	userAgent string
	limit     int // page size (Lemmy caps a listing page at 50)
	health    *HealthTracker
}

// NewLemmy builds a Lemmy adapter. userAgent should identify the crawler;
// limit caps the listing page size (<=0 → 20; clamped to Lemmy's max of 50).
func NewLemmy(userAgent string, timeout time.Duration, limit int) *Lemmy {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	return &Lemmy{
		client:    &http.Client{Timeout: timeout},
		userAgent: userAgent,
		limit:     limit,
		health:    NewHealthTracker("lemmy", 0.5, 5),
	}
}

func (l *Lemmy) Name() string { return "lemmy" }

func (l *Lemmy) Health() Health { return l.health.Status() }

// Discover turns a seed into the first listing target. Supported seeds:
//   - an instance host ("lemmy.ml") → its all-communities post listing;
//   - a community reference ("lemmy.ml/c/technology" or the same as a URL) →
//     that community's post listing (community_name=technology);
//   - a full v3 API URL → passed through unchanged (also lets a caller point at
//     /api/v3/comment/list to ingest a thread).
func (l *Lemmy) Discover(_ context.Context, seed string) ([]string, error) {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return nil, fmt.Errorf("lemmy: empty seed")
	}
	// Already an API endpoint — use as-is.
	if strings.Contains(seed, "/api/v3/") {
		return []string{seed}, nil
	}
	host, community := parseLemmySeed(seed)
	if host == "" {
		return nil, fmt.Errorf("lemmy: cannot derive instance host from seed %q", seed)
	}
	q := url.Values{}
	q.Set("type_", "All")
	q.Set("sort", "Active")
	q.Set("limit", strconv.Itoa(l.limit))
	q.Set("page", "1")
	if community != "" {
		q.Set("community_name", community)
	}
	return []string{"https://" + host + "/api/v3/post/list?" + q.Encode()}, nil
}

// Fetch retrieves a listing page. Lemmy paginates by page number rather than a
// cursor header, so Next is left empty here and computed by Paginate from the
// current target. Outcomes are recorded on the health tracker.
func (l *Lemmy) Fetch(ctx context.Context, target string) (*RawContent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		l.health.RecordFetch(err)
		return nil, err
	}
	req.Header.Set("User-Agent", l.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := l.client.Do(req)
	if err != nil {
		l.health.RecordFetch(err)
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap
	if err != nil {
		l.health.RecordFetch(err)
		return nil, err
	}
	if resp.StatusCode >= 400 {
		err = fmt.Errorf("lemmy: %s returned %d", target, resp.StatusCode)
		l.health.RecordFetch(err)
		return nil, err
	}
	l.health.RecordFetch(nil)

	return &RawContent{
		Target:      target,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
	}, nil
}

// Paginate advances to the next page by incrementing the target's page param.
// It stops when the current page came back empty, so an exhausted listing does
// not loop forever fetching empty pages.
func (l *Lemmy) Paginate(raw *RawContent) (string, bool) {
	if raw == nil || !lemmyBodyHasItems(raw.Body) {
		return "", false
	}
	return incrementPage(raw.Target)
}

// lemmyListing is the response envelope: a post listing carries "posts", a
// comment listing carries "comments". Parse handles either so the same adapter
// can ingest both roots and threads.
type lemmyListing struct {
	Posts    []lemmyPostView    `json:"posts"`
	Comments []lemmyCommentView `json:"comments"`
}

type lemmyPostView struct {
	Post      lemmyPost      `json:"post"`
	Creator   lemmyPerson    `json:"creator"`
	Community lemmyCommunity `json:"community"`
	Counts    lemmyCounts    `json:"counts"`
}

type lemmyCommentView struct {
	Comment lemmyComment `json:"comment"`
	Creator lemmyPerson  `json:"creator"`
	Post    lemmyPost    `json:"post"`
	Counts  lemmyCounts  `json:"counts"`
}

type lemmyPost struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"` // title
	Body      string `json:"body"` // Markdown
	URL       string `json:"url"`  // external link for link-posts
	Published string `json:"published"`
	APID      string `json:"ap_id"` // canonical ActivityPub permalink
	Deleted   bool   `json:"deleted"`
	Removed   bool   `json:"removed"`
}

type lemmyComment struct {
	ID        int64  `json:"id"`
	Content   string `json:"content"` // Markdown
	Published string `json:"published"`
	APID      string `json:"ap_id"`
	Path      string `json:"path"` // materialized path "0.<ancestor>.….<self>"
	PostID    int64  `json:"post_id"`
	Deleted   bool   `json:"deleted"`
	Removed   bool   `json:"removed"`
}

type lemmyPerson struct {
	Name    string `json:"name"`
	ActorID string `json:"actor_id"`
}

type lemmyCommunity struct {
	Name    string `json:"name"`
	ActorID string `json:"actor_id"`
}

type lemmyCounts struct {
	Score      int `json:"score"`
	Upvotes    int `json:"upvotes"`
	Downvotes  int `json:"downvotes"`
	Comments   int `json:"comments"`
	ChildCount int `json:"child_count"`
}

// Parse turns a Lemmy post or comment listing into normalized docs. Deleted,
// removed, and empty-text items are dropped. Post and comment ids live in
// separate integer spaces on Lemmy, so PostID is namespaced ("post/<id>",
// "comment/<id>") to keep the platform+post_id content hash collision-free and
// to let ParentID reference the right kind of ancestor for thread rebuilds.
func (l *Lemmy) Parse(raw *RawContent) ([]NormalizedDoc, error) {
	if raw == nil {
		return nil, fmt.Errorf("lemmy: nil raw content")
	}
	var listing lemmyListing
	if err := json.Unmarshal(raw.Body, &listing); err != nil {
		return nil, fmt.Errorf("lemmy: decode listing: %w", err)
	}

	out := make([]NormalizedDoc, 0, len(listing.Posts)+len(listing.Comments))
	for i := range listing.Posts {
		if d, ok := l.postDoc(&listing.Posts[i]); ok {
			out = append(out, d)
		}
	}
	for i := range listing.Comments {
		if d, ok := l.commentDoc(&listing.Comments[i]); ok {
			out = append(out, d)
		}
	}
	l.health.RecordItems(len(out))
	return out, nil
}

func (l *Lemmy) postDoc(pv *lemmyPostView) (NormalizedDoc, bool) {
	p := &pv.Post
	if p.ID == 0 || p.Deleted || p.Removed {
		return NormalizedDoc{}, false
	}
	title := strings.TrimSpace(p.Name)
	body := htmlToText(p.Body)
	// Title + body so a link-post's headline is searchable even when its only
	// content is the external article (mirrors the Hacker News adapter).
	text := strings.TrimSpace(title + " " + body)
	if text == "" {
		return NormalizedDoc{}, false
	}
	var media []string
	if p.URL != "" {
		media = []string{p.URL}
	}
	id := "post/" + strconv.FormatInt(p.ID, 10)
	d := NormalizedDoc{
		Platform:     "lemmy",
		PostID:       id,
		AuthorHandle: pv.Creator.Name,
		PostedAt:     parseLemmyTime(p.Published),
		Permalink:    firstNonEmpty(p.APID, ""),
		Title:        firstNonEmpty(title, makeTitle(pv.Creator.Name, text)),
		Text:         text,
		Lang:         resolveLang("", text),
		MediaURLs:    media,
		Engagement: map[string]int{
			"score":     pv.Counts.Score,
			"upvotes":   pv.Counts.Upvotes,
			"downvotes": pv.Counts.Downvotes,
			"comments":  pv.Counts.Comments,
		},
	}
	d.finalize()
	return d, true
}

func (l *Lemmy) commentDoc(cv *lemmyCommentView) (NormalizedDoc, bool) {
	c := &cv.Comment
	if c.ID == 0 || c.Deleted || c.Removed {
		return NormalizedDoc{}, false
	}
	text := htmlToText(c.Content)
	if text == "" {
		return NormalizedDoc{}, false
	}
	d := NormalizedDoc{
		Platform:     "lemmy",
		PostID:       "comment/" + strconv.FormatInt(c.ID, 10),
		AuthorHandle: cv.Creator.Name,
		PostedAt:     parseLemmyTime(c.Published),
		Permalink:    firstNonEmpty(c.APID, ""),
		ParentID:     commentParent(c.Path, c.PostID),
		Title:        makeTitle(cv.Creator.Name, text),
		Text:         text,
		Lang:         resolveLang("", text),
		Engagement: map[string]int{
			"score":       cv.Counts.Score,
			"child_count": cv.Counts.ChildCount,
		},
	}
	d.finalize()
	return d, true
}

// --- helpers ---------------------------------------------------------------

// parseLemmySeed extracts the instance host and (optional) community name from a
// seed given as a bare host, "host/c/community", or the same forms as a URL.
func parseLemmySeed(seed string) (host, community string) {
	path := seed
	if u, err := url.Parse(seed); err == nil && u.Host != "" {
		host = u.Host
		path = u.Path
	} else {
		parts := strings.SplitN(seed, "/", 2)
		host = parts[0]
		if len(parts) == 2 {
			path = "/" + parts[1]
		} else {
			path = ""
		}
	}
	if i := strings.Index(path, "/c/"); i >= 0 {
		rest := path[i+len("/c/"):]
		community = strings.Trim(strings.SplitN(rest, "/", 2)[0], "/")
	}
	return strings.TrimSuffix(host, "/"), community
}

// commentParent maps a Lemmy comment's materialized path to its parent's
// namespaced PostID. A path is "0.<self>" for a top-level comment (parent is
// the post) or "0.<ancestor>.….<self>" deeper in the tree.
func commentParent(path string, postID int64) string {
	parts := strings.Split(path, ".")
	if len(parts) <= 2 { // "0.<self>" (or malformed) → parented to the post
		return "post/" + strconv.FormatInt(postID, 10)
	}
	return "comment/" + parts[len(parts)-2]
}

// parseLemmyTime parses a Lemmy timestamp, tolerating both the RFC3339 form and
// the timezone-less naive-UTC form some Lemmy versions emit.
func parseLemmyTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// incrementPage returns the target with its page query param advanced by one.
func incrementPage(target string) (string, bool) {
	u, err := url.Parse(target)
	if err != nil {
		return "", false
	}
	q := u.Query()
	page := 1
	if p := q.Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			page = n
		}
	}
	q.Set("page", strconv.Itoa(page+1))
	u.RawQuery = q.Encode()
	return u.String(), true
}

// lemmyBodyHasItems reports whether a listing page contained any post or comment
// — the signal Paginate uses to know it has reached the end.
func lemmyBodyHasItems(body []byte) bool {
	var l lemmyListing
	if err := json.Unmarshal(body, &l); err != nil {
		return false
	}
	return len(l.Posts) > 0 || len(l.Comments) > 0
}
