package social

import (
	"testing"
)

// fixtureStatuses is a trimmed but shape-accurate Mastodon public-timeline
// response: a normal post, a reply (has in_reply_to_id), a boost (reblog wraps
// the original), and a media-only post with empty content. Keep this in sync
// with the real API shape — it is the contract test that alerts us when the
// parser and the platform drift (docs/08 §9).
const fixtureStatuses = `[
  {
    "id": "111000000000000001",
    "created_at": "2026-07-01T12:00:00.000Z",
    "url": "https://mastodon.social/@alice/111000000000000001",
    "uri": "https://mastodon.social/users/alice/statuses/111000000000000001",
    "content": "<p>Hello <b>Fediverse</b>!<br>Testing &amp; indexing.</p>",
    "language": "en",
    "in_reply_to_id": null,
    "replies_count": 2,
    "reblogs_count": 5,
    "favourites_count": 9,
    "account": {"acct": "alice", "username": "alice", "url": "https://mastodon.social/@alice"},
    "media_attachments": []
  },
  {
    "id": "111000000000000002",
    "created_at": "2026-07-01T12:05:00.000Z",
    "url": "https://mastodon.social/@bob/111000000000000002",
    "uri": "https://mastodon.social/users/bob/statuses/111000000000000002",
    "content": "<p>@alice good point, thanks!</p>",
    "language": "en",
    "in_reply_to_id": "111000000000000001",
    "replies_count": 0,
    "reblogs_count": 0,
    "favourites_count": 1,
    "account": {"acct": "bob@example.org", "username": "bob", "url": "https://example.org/@bob"},
    "media_attachments": []
  },
  {
    "id": "111000000000000099",
    "created_at": "2026-07-01T12:10:00.000Z",
    "url": "https://mastodon.social/@carol/111000000000000099",
    "content": "",
    "account": {"acct": "carol", "username": "carol"},
    "media_attachments": [],
    "reblog": {
      "id": "111000000000000001",
      "created_at": "2026-07-01T12:00:00.000Z",
      "url": "https://mastodon.social/@alice/111000000000000001",
      "content": "<p>Hello <b>Fediverse</b>!<br>Testing &amp; indexing.</p>",
      "language": "en",
      "replies_count": 2,
      "reblogs_count": 5,
      "favourites_count": 9,
      "account": {"acct": "alice", "username": "alice"},
      "media_attachments": []
    }
  },
  {
    "id": "111000000000000003",
    "created_at": "2026-07-01T12:15:00.000Z",
    "url": "https://mastodon.social/@dave/111000000000000003",
    "content": "",
    "language": "en",
    "account": {"acct": "dave", "username": "dave"},
    "media_attachments": [{"url": "https://cdn.example/img.png", "preview_url": "https://cdn.example/thumb.png"}]
  }
]`

func TestMastodonParse(t *testing.T) {
	m := NewMastodon("test-agent", 0)
	docs, err := m.Parse(&RawContent{Body: []byte(fixtureStatuses)})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// The boost (dedupes to the original) and the empty media-only post are
	// dropped, leaving the two textual posts.
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2: %+v", len(docs), docs)
	}

	root := docs[0]
	if root.PostID != "111000000000000001" {
		t.Errorf("root PostID = %q", root.PostID)
	}
	if root.Text != "Hello Fediverse! Testing & indexing." {
		t.Errorf("root Text = %q (HTML strip/entity decode wrong)", root.Text)
	}
	if root.AuthorHandle != "alice" {
		t.Errorf("root AuthorHandle = %q", root.AuthorHandle)
	}
	if root.ParentID != "" {
		t.Errorf("root ParentID = %q, want empty", root.ParentID)
	}
	if root.Permalink != "https://mastodon.social/@alice/111000000000000001" {
		t.Errorf("root Permalink = %q", root.Permalink)
	}
	if root.Lang != "en" {
		t.Errorf("root Lang = %q, want en", root.Lang)
	}
	if root.Engagement["favourites"] != 9 || root.Engagement["reblogs"] != 5 || root.Engagement["replies"] != 2 {
		t.Errorf("root Engagement = %v", root.Engagement)
	}
	if len(root.ContentHash) != 32 {
		t.Errorf("root ContentHash len = %d, want 32", len(root.ContentHash))
	}

	reply := docs[1]
	if reply.ParentID != "111000000000000001" {
		t.Errorf("reply ParentID = %q, want the root id", reply.ParentID)
	}
	if reply.AuthorHandle != "bob@example.org" {
		t.Errorf("reply AuthorHandle = %q, want federated handle", reply.AuthorHandle)
	}

	// Meta payload (docs/08 §6) must carry the social identity fields.
	meta := root.Meta()
	if meta["source"] != "social" || meta["platform"] != "mastodon" {
		t.Errorf("meta source/platform wrong: %v", meta)
	}
	if _, ok := meta["parent_id"]; ok {
		t.Errorf("root meta should omit parent_id, got %v", meta["parent_id"])
	}
	if meta["posted_at"] != "2026-07-01T12:00:00Z" {
		t.Errorf("meta posted_at = %v", meta["posted_at"])
	}
}

func TestMastodonContentHashDistinctPerPost(t *testing.T) {
	// Two posts with identical text but different ids must not collide (so the
	// documents unique-content_hash constraint keeps them both).
	body := `[
	  {"id":"1","created_at":"2026-07-01T00:00:00Z","url":"u1","content":"<p>gm</p>","account":{"acct":"a"}},
	  {"id":"2","created_at":"2026-07-01T00:00:01Z","url":"u2","content":"<p>gm</p>","account":{"acct":"b"}}
	]`
	docs, err := NewMastodon("x", 0).Parse(&RawContent{Body: []byte(body)})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(docs))
	}
	if string(docs[0].ContentHash) == string(docs[1].ContentHash) {
		t.Error("identical text produced identical content_hash; posts would dedupe away")
	}
}

func TestNextFromLinkHeader(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<https://m.social/api/v1/timelines/public?max_id=110>; rel="next", <...>; rel="prev"`,
			"https://m.social/api/v1/timelines/public?max_id=110"},
		{`<https://m.social/x?max_id=9>; rel=next`, "https://m.social/x?max_id=9"},
		{`<https://m.social/x>; rel="prev"`, ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := nextFromLinkHeader(c.in); got != c.want {
			t.Errorf("nextFromLinkHeader(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMastodonDiscover(t *testing.T) {
	m := NewMastodon("x", 0)
	got, err := m.Discover(nil, "mastodon.social")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	want := "https://mastodon.social/api/v1/timelines/public?limit=40"
	if len(got) != 1 || got[0] != want {
		t.Errorf("Discover(host) = %v, want [%s]", got, want)
	}

	// A full API URL is passed through untouched.
	api := "https://mastodon.social/api/v1/accounts/1/statuses"
	got, _ = m.Discover(nil, api)
	if len(got) != 1 || got[0] != api {
		t.Errorf("Discover(api url) = %v, want [%s]", got, api)
	}
}

func TestHealthTrackerAutoDisable(t *testing.T) {
	h := NewHealthTracker("t", 0.5, 4)
	// Under min sample: always enabled even if all error.
	h.RecordFetch(errBoom)
	h.RecordFetch(errBoom)
	if !h.Enabled() {
		t.Fatal("should stay enabled below min sample")
	}
	// Cross the sample with a >50% error rate → disable.
	h.RecordFetch(errBoom)
	h.RecordFetch(nil)
	if h.Enabled() {
		t.Fatal("should auto-disable above error threshold")
	}
	st := h.Status()
	if st.Fetches != 4 || st.Errors != 3 {
		t.Errorf("status counters wrong: %+v", st)
	}
}

var errBoom = errBoomT("boom")

type errBoomT string

func (e errBoomT) Error() string { return string(e) }
