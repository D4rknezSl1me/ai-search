package sitemap

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"testing"
)

const urlset = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/a</loc></url>
  <url><loc>  https://example.com/b  </loc></url>
  <url><loc></loc></url>
</urlset>`

const index = `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://example.com/sm1.xml</loc></sitemap>
  <sitemap><loc>https://example.com/sm2.xml</loc></sitemap>
</sitemapindex>`

func gz(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

// --------------------------------------------------------------------- Parse ---

func TestParseURLSet(t *testing.T) {
	res, err := Parse([]byte(urlset))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.URLs) != 2 || res.URLs[0] != "https://example.com/a" || res.URLs[1] != "https://example.com/b" {
		t.Fatalf("urls = %v (empty <loc> should be dropped, whitespace trimmed)", res.URLs)
	}
	if len(res.Sitemaps) != 0 {
		t.Fatalf("sitemaps = %v, want none", res.Sitemaps)
	}
}

func TestParseIndex(t *testing.T) {
	res, err := Parse([]byte(index))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sitemaps) != 2 || len(res.URLs) != 0 {
		t.Fatalf("got urls=%v sitemaps=%v", res.URLs, res.Sitemaps)
	}
}

func TestParseGzip(t *testing.T) {
	res, err := Parse(gz(urlset))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.URLs) != 2 {
		t.Fatalf("gzip parse urls = %v", res.URLs)
	}
}

func TestParseInvalidXML(t *testing.T) {
	if _, err := Parse([]byte("not xml <<<")); err == nil {
		t.Fatal("expected an error for malformed XML")
	}
}

// ------------------------------------------------------------------ Discover ---

func fakeFetch(m map[string][]byte) FetchFunc {
	return func(_ context.Context, u string) ([]byte, error) {
		b, ok := m[u]
		if !ok {
			return nil, fmt.Errorf("no such url: %s", u)
		}
		return b, nil
	}
}

func TestDiscoverFlat(t *testing.T) {
	urls, err := Discover(context.Background(), "root", fakeFetch(map[string][]byte{
		"root": []byte(urlset),
	}), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 2 {
		t.Fatalf("urls = %v", urls)
	}
}

func TestDiscoverIndexFollowsChildrenAndDedupes(t *testing.T) {
	sm1 := `<urlset><url><loc>https://example.com/a</loc></url><url><loc>https://example.com/b</loc></url></urlset>`
	sm2 := `<urlset><url><loc>https://example.com/b</loc></url><url><loc>https://example.com/c</loc></url></urlset>`
	urls, err := Discover(context.Background(), "root", fakeFetch(map[string][]byte{
		"root":                        []byte(index),
		"https://example.com/sm1.xml": []byte(sm1),
		"https://example.com/sm2.xml": []byte(sm2),
	}), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	// a, b, c — b de-duplicated across the two child sitemaps.
	if len(urls) != 3 {
		t.Fatalf("urls = %v, want 3 deduped", urls)
	}
}

func TestDiscoverRootFetchErrorIsFatal(t *testing.T) {
	_, err := Discover(context.Background(), "root", fakeFetch(map[string][]byte{}), Limits{})
	if err == nil {
		t.Fatal("expected a fatal error when the root can't be fetched")
	}
}

func TestDiscoverBadChildSkipped(t *testing.T) {
	// sm1 is missing (fetch error), sm2 is fine → we still get sm2's URLs.
	sm2 := `<urlset><url><loc>https://example.com/c</loc></url></urlset>`
	urls, err := Discover(context.Background(), "root", fakeFetch(map[string][]byte{
		"root":                        []byte(index),
		"https://example.com/sm2.xml": []byte(sm2),
	}), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 || urls[0] != "https://example.com/c" {
		t.Fatalf("urls = %v, want just c", urls)
	}
}

func TestDiscoverRespectsMaxURLs(t *testing.T) {
	urls, err := Discover(context.Background(), "root", fakeFetch(map[string][]byte{
		"root": []byte(urlset),
	}), Limits{MaxURLs: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 {
		t.Fatalf("urls = %v, want capped at 1", urls)
	}
}

func TestDiscoverRespectsMaxSitemaps(t *testing.T) {
	sm := `<urlset><url><loc>https://example.com/x</loc></url></urlset>`
	calls := 0
	fetch := func(_ context.Context, u string) ([]byte, error) {
		calls++
		if u == "root" {
			return []byte(index), nil
		}
		return []byte(sm), nil
	}
	_, err := Discover(context.Background(), "root", fetch, Limits{MaxSitemaps: 1})
	if err != nil {
		t.Fatal(err)
	}
	// 1 root fetch + at most 1 child fetch.
	if calls > 2 {
		t.Fatalf("fetched %d times, want ≤2 (root + 1 child)", calls)
	}
}
