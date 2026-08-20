package metasearch

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func page(urls ...string) string {
	parts := make([]string, len(urls))
	for i, u := range urls {
		parts[i] = fmt.Sprintf(`{"url":%q,"title":"t","content":"c"}`, u)
	}
	return `{"query":"x","results":[` + strings.Join(parts, ",") + `]}`
}

func TestParseResults(t *testing.T) {
	data := page("https://a.com/1", "https://b.com/2", "https://a.com/1") // dup
	got := ParseResults([]byte(data))
	if len(got) != 2 || got[0] != "https://a.com/1" || got[1] != "https://b.com/2" {
		t.Fatalf("got %v", got)
	}
}

func TestParseResultsSkipsNonHTTPAndJunk(t *testing.T) {
	data := `{"results":[{"url":"ftp://x/y"},{"url":""},{"url":"https://ok.com"}]}`
	got := ParseResults([]byte(data))
	if len(got) != 1 || got[0] != "https://ok.com" {
		t.Fatalf("got %v", got)
	}
	if r := ParseResults([]byte("not json")); r != nil {
		t.Fatalf("bad json should yield nil, got %v", r)
	}
}

func TestQueryURLEncodesAndFormats(t *testing.T) {
	u := queryURL("http://searxng:8080/", "vanessa vita", 2)
	for _, want := range []string{"q=vanessa+vita", "format=json", "pageno=2"} {
		if !strings.Contains(u, want) {
			t.Fatalf("query %q missing %q", u, want)
		}
	}
	if strings.Contains(u, "8080//search") {
		t.Fatalf("trailing slash not trimmed: %q", u)
	}
}

func router(pages map[int]string) FetchFunc {
	return func(_ context.Context, u string) ([]byte, error) {
		p := 1
		if strings.Contains(u, "pageno=2") {
			p = 2
		} else if strings.Contains(u, "pageno=3") {
			p = 3
		}
		body, ok := pages[p]
		if !ok {
			return nil, fmt.Errorf("no page %d", p)
		}
		return []byte(body), nil
	}
}

func TestDiscoverAcrossPagesWithDedup(t *testing.T) {
	f := router(map[int]string{
		1: page("https://a.com/1", "https://b.com/2"),
		2: page("https://b.com/2", "https://c.com/3"),
	})
	urls, err := Discover(context.Background(), "http://searxng:8080", "ada lovelace", f, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 3 { // a, b, c — b de-duplicated across pages
		t.Fatalf("urls = %v, want 3", urls)
	}
}

func TestDiscoverRespectsMaxURLs(t *testing.T) {
	f := router(map[int]string{1: page("https://a.com/1", "https://b.com/2", "https://c.com/3")})
	urls, err := Discover(context.Background(), "http://s", "q", f, Opts{MaxURLs: 2, MaxPages: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 2 {
		t.Fatalf("urls = %v, want 2", urls)
	}
}

func TestDiscoverFirstPageErrorFatal(t *testing.T) {
	f := func(_ context.Context, _ string) ([]byte, error) { return nil, fmt.Errorf("down") }
	if _, err := Discover(context.Background(), "http://s", "q", f, Opts{}); err == nil {
		t.Fatal("expected fatal error on first-page failure")
	}
}

func TestDiscoverValidatesInput(t *testing.T) {
	f := router(map[int]string{1: page("https://a.com")})
	if _, err := Discover(context.Background(), "", "q", f, Opts{}); err == nil {
		t.Fatal("empty base should error")
	}
	if _, err := Discover(context.Background(), "http://s", "  ", f, Opts{}); err == nil {
		t.Fatal("empty query should error")
	}
}
