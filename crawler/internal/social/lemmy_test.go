package social

import (
	"strings"
	"testing"
)

// Shape-accurate Lemmy v3 fixtures (docs/08 §9 contract test). Keep in sync
// with https://join-lemmy.org/api — these alert us when the parser and the
// platform drift.

// A post listing: a link-post (external url, no body) and a self/text post.
const lemmyPosts = `{
  "posts": [
    {
      "post": {
        "id": 8863,
        "name": "Rust 2.0 released",
        "body": "",
        "url": "https://blog.rust-lang.org/2026/01/01/Rust-2.0.html",
        "published": "2026-07-01T12:00:00.123456Z",
        "ap_id": "https://lemmy.ml/post/8863",
        "deleted": false,
        "removed": false
      },
      "creator": {"name": "alice", "actor_id": "https://lemmy.ml/u/alice"},
      "community": {"name": "rust", "actor_id": "https://lemmy.ml/c/rust"},
      "counts": {"score": 111, "upvotes": 120, "downvotes": 9, "comments": 71}
    },
    {
      "post": {
        "id": 9001,
        "name": "What are you working on?",
        "body": "Share your **weekend** projects.",
        "url": "",
        "published": "2026-07-01T12:05:00",
        "ap_id": "https://lemmy.ml/post/9001"
      },
      "creator": {"name": "bob", "actor_id": "https://lemmy.ml/u/bob"},
      "community": {"name": "programming", "actor_id": "https://lemmy.ml/c/programming"},
      "counts": {"score": 5, "comments": 2}
    },
    {
      "post": {"id": 9099, "name": "gone", "body": "", "url": "", "removed": true},
      "creator": {"name": "carol"},
      "community": {"name": "rust"},
      "counts": {"score": 0}
    }
  ]
}`

// A comment listing on a thread: a top-level comment (path "0.<self>") and a
// reply (path "0.<parent>.<self>").
const lemmyComments = `{
  "comments": [
    {
      "comment": {
        "id": 500,
        "content": "Congrats to the &amp; whole team!",
        "published": "2026-07-01T13:00:00Z",
        "ap_id": "https://lemmy.ml/comment/500",
        "path": "0.500",
        "post_id": 8863
      },
      "creator": {"name": "dave", "actor_id": "https://lemmy.ml/u/dave"},
      "post": {"id": 8863, "name": "Rust 2.0 released"},
      "counts": {"score": 12, "child_count": 1}
    },
    {
      "comment": {
        "id": 501,
        "content": "Agreed, huge milestone.",
        "published": "2026-07-01T13:05:00Z",
        "ap_id": "https://lemmy.ml/comment/501",
        "path": "0.500.501",
        "post_id": 8863
      },
      "creator": {"name": "erin"},
      "post": {"id": 8863, "name": "Rust 2.0 released"},
      "counts": {"score": 3, "child_count": 0}
    }
  ]
}`

func TestLemmyParsePosts(t *testing.T) {
	docs, err := NewLemmy("x", 0, 0).Parse(&RawContent{Body: []byte(lemmyPosts)})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	// The removed post is dropped.
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2 (removed post dropped)", len(docs))
	}

	link := docs[0]
	if link.PostID != "post/8863" {
		t.Errorf("PostID = %q, want namespaced post/8863", link.PostID)
	}
	if link.AuthorHandle != "alice" {
		t.Errorf("AuthorHandle = %q", link.AuthorHandle)
	}
	if link.Title != "Rust 2.0 released" {
		t.Errorf("Title = %q", link.Title)
	}
	if link.Text != "Rust 2.0 released" {
		t.Errorf("Text = %q (title only when a link-post has no body)", link.Text)
	}
	if link.ParentID != "" {
		t.Errorf("ParentID = %q, want empty for a root post", link.ParentID)
	}
	if link.Permalink != "https://lemmy.ml/post/8863" {
		t.Errorf("Permalink = %q", link.Permalink)
	}
	if len(link.MediaURLs) != 1 || link.MediaURLs[0] != "https://blog.rust-lang.org/2026/01/01/Rust-2.0.html" {
		t.Errorf("MediaURLs = %v, want the external link", link.MediaURLs)
	}
	if link.Engagement["score"] != 111 || link.Engagement["comments"] != 71 {
		t.Errorf("Engagement = %v", link.Engagement)
	}
	if link.PostedAt.Format("2006-01-02T15:04:05") != "2026-07-01T12:00:00" {
		t.Errorf("PostedAt = %v (RFC3339 decode wrong)", link.PostedAt)
	}
	if len(link.ContentHash) != 32 {
		t.Errorf("ContentHash len = %d, want 32", len(link.ContentHash))
	}

	self := docs[1]
	if self.Text != "What are you working on? Share your **weekend** projects." {
		t.Errorf("Text = %q (title + body expected)", self.Text)
	}
	// The timezone-less form must still decode, not zero out.
	if self.PostedAt.Format("2006-01-02T15:04:05") != "2026-07-01T12:05:00" {
		t.Errorf("PostedAt = %v (naive-UTC decode wrong)", self.PostedAt)
	}

	meta := link.Meta()
	if meta["source"] != "social" || meta["platform"] != "lemmy" {
		t.Errorf("meta source/platform wrong: %v", meta)
	}
}

func TestLemmyParseComments(t *testing.T) {
	docs, err := NewLemmy("x", 0, 0).Parse(&RawContent{Body: []byte(lemmyComments)})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(docs))
	}

	top := docs[0]
	if top.PostID != "comment/500" {
		t.Errorf("PostID = %q", top.PostID)
	}
	if top.Text != "Congrats to the & whole team!" {
		t.Errorf("Text = %q (entity decode wrong)", top.Text)
	}
	// A top-level comment (path "0.500") is parented to its post.
	if top.ParentID != "post/8863" {
		t.Errorf("ParentID = %q, want post/8863 for a top-level comment", top.ParentID)
	}
	if top.Title != "@dave: Congrats to the & whole team!" {
		t.Errorf("Title = %q", top.Title)
	}
	if _, ok := top.Engagement["comments"]; ok {
		t.Errorf("comment should not carry post-level engagement: %v", top.Engagement)
	}
	if top.Engagement["score"] != 12 {
		t.Errorf("Engagement score = %d", top.Engagement["score"])
	}

	reply := docs[1]
	// A reply (path "0.500.501") is parented to its ancestor comment.
	if reply.ParentID != "comment/500" {
		t.Errorf("ParentID = %q, want comment/500 for a nested reply", reply.ParentID)
	}
}

func TestLemmyDiscover(t *testing.T) {
	l := NewLemmy("x", 0, 0)

	// Bare instance host → all-communities post listing.
	got, err := l.Discover(nil, "lemmy.ml")
	if err != nil {
		t.Fatalf("Discover(host) error: %v", err)
	}
	if len(got) != 1 || !containsAll(got[0], "https://lemmy.ml/api/v3/post/list?", "page=1", "limit=20") {
		t.Errorf("Discover(host) = %v", got)
	}
	if containsAll(got[0], "community_name") {
		t.Errorf("bare host should not scope to a community: %s", got[0])
	}

	// Community reference → community-scoped listing.
	got, _ = l.Discover(nil, "lemmy.ml/c/technology")
	if len(got) != 1 || !containsAll(got[0], "community_name=technology") {
		t.Errorf("Discover(community) = %v", got)
	}

	// Community reference as a URL resolves the same way.
	got, _ = l.Discover(nil, "https://lemmy.world/c/asklemmy")
	if len(got) != 1 || !containsAll(got[0], "https://lemmy.world/api/v3/post/list?", "community_name=asklemmy") {
		t.Errorf("Discover(url community) = %v", got)
	}

	// Full API URL passes through untouched (e.g. a comment thread).
	api := "https://lemmy.ml/api/v3/comment/list?post_id=8863"
	got, _ = l.Discover(nil, api)
	if len(got) != 1 || got[0] != api {
		t.Errorf("Discover(api url) = %v, want [%s]", got, api)
	}

	// Empty seed is a loud error.
	if _, err := l.Discover(nil, "  "); err == nil {
		t.Error("Discover(empty) should error")
	}
}

func TestLemmyPaginate(t *testing.T) {
	l := NewLemmy("x", 0, 0)

	// A page with items advances the page number.
	raw := &RawContent{
		Target: "https://lemmy.ml/api/v3/post/list?limit=20&page=1&sort=Active",
		Body:   []byte(lemmyPosts),
	}
	next, ok := l.Paginate(raw)
	if !ok {
		t.Fatal("Paginate should continue while items are returned")
	}
	if !containsAll(next, "page=2") {
		t.Errorf("next = %q, want page=2", next)
	}

	// An empty page stops pagination — no endless fetching of blank pages.
	empty := &RawContent{Target: raw.Target, Body: []byte(`{"posts": []}`)}
	if _, ok := l.Paginate(empty); ok {
		t.Error("Paginate should stop on an empty listing")
	}
}

func TestLemmyContentHashDistinctPostAndComment(t *testing.T) {
	// A post id and a comment id can both be "5"; the namespaced PostID must
	// keep their content hashes distinct so neither dedupes the other away.
	body := `{
      "posts": [{"post": {"id": 5, "name": "x", "published": "2026-07-01T00:00:00Z", "ap_id": "p"}, "creator": {"name": "a"}, "counts": {}}],
      "comments": [{"comment": {"id": 5, "content": "x", "published": "2026-07-01T00:00:00Z", "ap_id": "c", "path": "0.5", "post_id": 5}, "creator": {"name": "b"}, "counts": {}}]
    }`
	docs, err := NewLemmy("x", 0, 0).Parse(&RawContent{Body: []byte(body)})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(docs))
	}
	if string(docs[0].ContentHash) == string(docs[1].ContentHash) {
		t.Error("post/5 and comment/5 collided on content_hash")
	}
}

// containsAll reports whether s contains every substring in subs.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
