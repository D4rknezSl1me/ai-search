package social

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/abadojack/whatlanggo"
)

// Mastodon ingests public content from any Mastodon/Fediverse instance. The
// public REST API needs no authentication for public timelines and account
// statuses (docs/08 §5: "open APIs; easy, high-quality"), which makes it the
// ideal first adapter — the whole social path can be exercised with zero
// credentials or browser automation.
type Mastodon struct {
	client    *http.Client
	userAgent string
	limit     int // page size (Mastodon caps public timelines at 40)
	health    *HealthTracker
}

// NewMastodon builds a Mastodon adapter. userAgent should identify the crawler.
func NewMastodon(userAgent string, timeout time.Duration) *Mastodon {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Mastodon{
		client:    &http.Client{Timeout: timeout},
		userAgent: userAgent,
		limit:     40,
		health:    NewHealthTracker("mastodon", 0.5, 5),
	}
}

func (m *Mastodon) Name() string { return "mastodon" }

func (m *Mastodon) Health() Health { return m.health.Status() }

// Discover turns a seed into fetch targets. Supported seeds:
//   - an instance host or URL ("mastodon.social") → its public timeline;
//   - a full timeline/account API URL → passed through unchanged.
//
// Account/hashtag lookup by handle needs an extra API round-trip and is left to
// a later iteration; the public timeline already exercises the full path.
func (m *Mastodon) Discover(_ context.Context, seed string) ([]string, error) {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return nil, fmt.Errorf("mastodon: empty seed")
	}
	// Already an API endpoint — use as-is.
	if strings.Contains(seed, "/api/v1/") {
		return []string{seed}, nil
	}
	host := seed
	if u, err := url.Parse(seed); err == nil && u.Host != "" {
		host = u.Host
	}
	host = strings.TrimSuffix(host, "/")
	endpoint := fmt.Sprintf("https://%s/api/v1/timelines/public?limit=%d", host, m.limit)
	return []string{endpoint}, nil
}

// Fetch retrieves a target and extracts the rel="next" pagination cursor from
// the Link header. Outcomes are recorded on the health tracker.
func (m *Mastodon) Fetch(ctx context.Context, target string) (*RawContent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		m.health.RecordFetch(err)
		return nil, err
	}
	req.Header.Set("User-Agent", m.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		m.health.RecordFetch(err)
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap
	if err != nil {
		m.health.RecordFetch(err)
		return nil, err
	}
	if resp.StatusCode >= 400 {
		err = fmt.Errorf("mastodon: %s returned %d", target, resp.StatusCode)
		m.health.RecordFetch(err)
		return nil, err
	}
	m.health.RecordFetch(nil)

	return &RawContent{
		Target:      target,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
		Next:        nextFromLinkHeader(resp.Header.Get("Link")),
	}, nil
}

func (m *Mastodon) Paginate(raw *RawContent) (string, bool) {
	if raw == nil || raw.Next == "" {
		return "", false
	}
	return raw.Next, true
}

// mastoStatus is the subset of a Mastodon Status object we consume.
// https://docs.joinmastodon.org/entities/Status/
type mastoStatus struct {
	ID               string           `json:"id"`
	CreatedAt        time.Time        `json:"created_at"`
	URL              string           `json:"url"`
	URI              string           `json:"uri"`
	Content          string           `json:"content"` // HTML
	Language         string           `json:"language"`
	InReplyToID      string           `json:"in_reply_to_id"`
	RepliesCount     int              `json:"replies_count"`
	ReblogsCount     int              `json:"reblogs_count"`
	FavouritesCount  int              `json:"favourites_count"`
	Account          mastoAccount     `json:"account"`
	MediaAttachments []mastoMedia     `json:"media_attachments"`
	Reblog           *mastoStatus     `json:"reblog"` // boost wraps the original
}

type mastoAccount struct {
	Acct     string `json:"acct"`     // user@remote for federated, else local user
	Username string `json:"username"`
	URL      string `json:"url"`
}

type mastoMedia struct {
	URL        string `json:"url"`
	PreviewURL string `json:"preview_url"`
	RemoteURL  string `json:"remote_url"`
}

// Parse turns a Mastodon timeline/statuses JSON array into normalized docs.
// Boosts (reblogs) are unwrapped to the original status so the actual content
// is indexed once, attributed to its real author.
func (m *Mastodon) Parse(raw *RawContent) ([]NormalizedDoc, error) {
	if raw == nil {
		return nil, fmt.Errorf("mastodon: nil raw content")
	}
	var statuses []mastoStatus
	if err := json.Unmarshal(raw.Body, &statuses); err != nil {
		return nil, fmt.Errorf("mastodon: decode statuses: %w", err)
	}

	out := make([]NormalizedDoc, 0, len(statuses))
	seen := map[string]bool{}
	for i := range statuses {
		s := &statuses[i]
		if s.Reblog != nil { // unwrap boost to the original post
			s = s.Reblog
		}
		if s.ID == "" || seen[s.ID] {
			continue
		}
		seen[s.ID] = true

		text := htmlToText(s.Content)
		if text == "" {
			continue // media-only post with no caption: nothing to index
		}

		d := NormalizedDoc{
			Platform:     "mastodon",
			PostID:       s.ID,
			AuthorHandle: s.Account.Acct,
			PostedAt:     s.CreatedAt.UTC(),
			Permalink:    firstNonEmpty(s.URL, s.URI),
			ParentID:     s.InReplyToID,
			Title:        makeTitle(s.Account.Acct, text),
			Text:         text,
			Lang:         resolveLang(s.Language, text),
			MediaURLs:    mediaURLs(s.MediaAttachments),
			Engagement: map[string]int{
				"replies":   s.RepliesCount,
				"reblogs":   s.ReblogsCount,
				"favourites": s.FavouritesCount,
			},
		}
		d.finalize()
		out = append(out, d)
	}
	m.health.RecordItems(len(out))
	return out, nil
}

// --- helpers ---------------------------------------------------------------

func mediaURLs(atts []mastoMedia) []string {
	if len(atts) == 0 {
		return nil
	}
	urls := make([]string, 0, len(atts))
	for _, a := range atts {
		if u := firstNonEmpty(a.URL, a.RemoteURL, a.PreviewURL); u != "" {
			urls = append(urls, u)
		}
	}
	return urls
}

// makeTitle derives a short, human-readable title: "@handle: first words…".
func makeTitle(handle, text string) string {
	const maxRunes = 80
	snippet := text
	if r := []rune(text); len(r) > maxRunes {
		snippet = strings.TrimRight(string(r[:maxRunes]), " ") + "…"
	}
	if handle == "" {
		return snippet
	}
	return "@" + handle + ": " + snippet
}

// resolveLang trusts the platform-declared language, falling back to detection
// so downstream language filters still work when the field is absent.
func resolveLang(declared, text string) string {
	if declared != "" {
		return declared
	}
	if len([]rune(text)) >= 20 {
		if info := whatlanggo.Detect(text); info.IsReliable() {
			return info.Lang.Iso6391()
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// nextFromLinkHeader pulls the rel="next" URL out of an RFC 5988 Link header,
// which is how Mastodon paginates (max_id cursor embedded in the URL).
var linkRe = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="?next"?`)

func nextFromLinkHeader(header string) string {
	if header == "" {
		return ""
	}
	if mm := linkRe.FindStringSubmatch(header); mm != nil {
		return mm[1]
	}
	return ""
}
