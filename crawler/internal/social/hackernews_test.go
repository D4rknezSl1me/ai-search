package social

import (
	"testing"
)

// Shape-accurate Hacker News item fixtures (docs/08 §9 contract test). Keep in
// sync with https://github.com/HackerNews/API — these alert us when the parser
// and the platform drift.

// A link story: a title + external URL, no self text.
const hnStory = `{
  "id": 8863,
  "type": "story",
  "by": "dhouston",
  "time": 1175714200,
  "title": "My YC app: Dropbox - Throw away your USB drive",
  "url": "http://www.getdropbox.com/u/2/screencast.html",
  "score": 111,
  "descendants": 71
}`

// A comment: HTML body, a parent, no title/score.
const hnComment = `{
  "id": 2921983,
  "type": "comment",
  "by": "norvig",
  "parent": 2921506,
  "time": 1314211127,
  "text": "Aw shucks, guys ... you make me <i>blush</i> with your &lt;3."
}`

// An Ask HN: both a title and self text.
const hnAsk = `{
  "id": 121003,
  "type": "story",
  "by": "tel",
  "time": 1203647620,
  "title": "Ask HN: The Arc Effect",
  "text": "<p>Anyone else notice a bump in traffic?</p>",
  "score": 25,
  "descendants": 16
}`

// A moderated/deleted item carries no content.
const hnDeleted = `{"id": 42, "type": "comment", "deleted": true, "parent": 41}`

func TestHackerNewsParseStory(t *testing.T) {
	docs, err := NewHackerNews("x", 0, 0).Parse(&RawContent{Body: []byte(hnStory)})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(docs))
	}
	d := docs[0]
	if d.PostID != "8863" {
		t.Errorf("PostID = %q", d.PostID)
	}
	if d.AuthorHandle != "dhouston" {
		t.Errorf("AuthorHandle = %q", d.AuthorHandle)
	}
	if d.Title != "My YC app: Dropbox - Throw away your USB drive" {
		t.Errorf("Title = %q", d.Title)
	}
	if d.Text != "My YC app: Dropbox - Throw away your USB drive" {
		t.Errorf("Text = %q (should be title when there is no self text)", d.Text)
	}
	if d.ParentID != "" {
		t.Errorf("ParentID = %q, want empty for a root story", d.ParentID)
	}
	if d.Permalink != "https://news.ycombinator.com/item?id=8863" {
		t.Errorf("Permalink = %q", d.Permalink)
	}
	if d.Engagement["score"] != 111 || d.Engagement["descendants"] != 71 {
		t.Errorf("Engagement = %v", d.Engagement)
	}
	if len(d.MediaURLs) != 1 || d.MediaURLs[0] != "http://www.getdropbox.com/u/2/screencast.html" {
		t.Errorf("MediaURLs = %v, want the external story url", d.MediaURLs)
	}
	if d.PostedAt.Format("2006-01-02") != "2007-04-04" {
		t.Errorf("PostedAt = %v (unix decode wrong)", d.PostedAt)
	}
	if len(d.ContentHash) != 32 {
		t.Errorf("ContentHash len = %d, want 32", len(d.ContentHash))
	}

	meta := d.Meta()
	if meta["source"] != "social" || meta["platform"] != "hackernews" {
		t.Errorf("meta source/platform wrong: %v", meta)
	}
}

func TestHackerNewsParseComment(t *testing.T) {
	docs, err := NewHackerNews("x", 0, 0).Parse(&RawContent{Body: []byte(hnComment)})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(docs))
	}
	d := docs[0]
	if d.Text != "Aw shucks, guys ... you make me blush with your <3." {
		t.Errorf("Text = %q (HTML strip / entity decode wrong)", d.Text)
	}
	if d.ParentID != "2921506" {
		t.Errorf("ParentID = %q, want the parent id", d.ParentID)
	}
	// Comments have no title, so it falls back to the @handle: snippet form.
	if d.Title != "@norvig: Aw shucks, guys ... you make me blush with your <3." {
		t.Errorf("Title = %q", d.Title)
	}
	// Comments carry no ranking engagement keys.
	if _, ok := d.Engagement["score"]; ok {
		t.Errorf("comment should not carry a score: %v", d.Engagement)
	}
}

func TestHackerNewsParseAsk(t *testing.T) {
	docs, err := NewHackerNews("x", 0, 0).Parse(&RawContent{Body: []byte(hnAsk)})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(docs))
	}
	if docs[0].Text != "Ask HN: The Arc Effect Anyone else notice a bump in traffic?" {
		t.Errorf("Text = %q (title + body expected)", docs[0].Text)
	}
}

func TestHackerNewsParseSkips(t *testing.T) {
	hn := NewHackerNews("x", 0, 0)
	for name, body := range map[string]string{
		"deleted": hnDeleted,
		"null":    "null",
		"empty":   "",
	} {
		docs, err := hn.Parse(&RawContent{Body: []byte(body)})
		if err != nil {
			t.Fatalf("%s: Parse error: %v", name, err)
		}
		if len(docs) != 0 {
			t.Errorf("%s: got %d docs, want 0", name, len(docs))
		}
	}
}

func TestHackerNewsDiscover(t *testing.T) {
	hn := NewHackerNews("x", 0, 0)

	// Numeric id → item URL.
	got, err := hn.Discover(nil, "8863")
	if err != nil {
		t.Fatalf("Discover(id) error: %v", err)
	}
	want := "https://hacker-news.firebaseio.com/v0/item/8863.json"
	if len(got) != 1 || got[0] != want {
		t.Errorf("Discover(id) = %v, want [%s]", got, want)
	}

	// Full item URL passes through untouched.
	api := "https://hacker-news.firebaseio.com/v0/item/1.json"
	got, _ = hn.Discover(nil, api)
	if len(got) != 1 || got[0] != api {
		t.Errorf("Discover(url) = %v, want [%s]", got, api)
	}

	// Unknown seed is a loud error, not a silent empty result.
	if _, err := hn.Discover(nil, "not-a-feed"); err == nil {
		t.Error("Discover(unknown) should error")
	}
}

func TestHackerNewsContentHashDistinctPerPost(t *testing.T) {
	// Two items with identical text but different ids must not collide.
	a, err := NewHackerNews("x", 0, 0).Parse(&RawContent{Body: []byte(`{"id":1,"type":"comment","by":"a","text":"gm","parent":9}`)})
	if err != nil {
		t.Fatalf("Parse a: %v", err)
	}
	b, err := NewHackerNews("x", 0, 0).Parse(&RawContent{Body: []byte(`{"id":2,"type":"comment","by":"b","text":"gm","parent":9}`)})
	if err != nil {
		t.Fatalf("Parse b: %v", err)
	}
	if string(a[0].ContentHash) == string(b[0].ContentHash) {
		t.Error("identical text produced identical content_hash; posts would dedupe away")
	}
}
