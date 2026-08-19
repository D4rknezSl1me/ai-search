package feeds

import (
	"bytes"
	"compress/gzip"
	"testing"
)

const rss2 = `<?xml version="1.0"?>
<rss version="2.0"><channel>
  <title>Example</title>
  <item><title>A</title><link>https://example.com/a</link></item>
  <item><title>B</title><link>https://example.com/b</link></item>
  <item><title>Dup</title><link>https://example.com/a</link></item>
</channel></rss>`

const atom = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Example</title>
  <entry>
    <title>A</title>
    <link rel="self" href="https://example.com/feed"/>
    <link rel="alternate" type="text/html" href="https://example.com/a"/>
  </entry>
  <entry>
    <title>B</title>
    <link href="https://example.com/b"/>
  </entry>
</feed>`

const rdf = `<?xml version="1.0"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/">
  <item><link>https://example.com/x</link></item>
  <item><link>https://example.com/y</link></item>
</rdf:RDF>`

func gz(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestParseRSS2WithDedupe(t *testing.T) {
	urls, err := Parse([]byte(rss2))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, urls, "https://example.com/a", "https://example.com/b")
}

func TestParseAtomPrefersAlternateHTMLLink(t *testing.T) {
	urls, err := Parse([]byte(atom))
	if err != nil {
		t.Fatal(err)
	}
	// entry 1 has a self + an alternate/html link → the alternate one wins;
	// entry 2 has a single bare href → used as-is.
	eq(t, urls, "https://example.com/a", "https://example.com/b")
}

func TestParseRDF(t *testing.T) {
	urls, err := Parse([]byte(rdf))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, urls, "https://example.com/x", "https://example.com/y")
}

func TestParseGzip(t *testing.T) {
	urls, err := Parse(gz(rss2))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, urls, "https://example.com/a", "https://example.com/b")
}

func TestParseInvalidXML(t *testing.T) {
	if _, err := Parse([]byte("<<not a feed")); err == nil {
		t.Fatal("expected an error for malformed XML")
	}
}

func TestParseEmptyFeedNoURLs(t *testing.T) {
	urls, err := Parse([]byte(`<rss version="2.0"><channel><title>empty</title></channel></rss>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 0 {
		t.Fatalf("got %v, want none", urls)
	}
}

func TestBestLinkFallbackToNonAlternateHref(t *testing.T) {
	// Only a self/edit link present → fall back to the first href rather than "".
	got := bestLink([]link{
		{Href: "https://example.com/edit", Rel: "edit"},
		{Href: "https://example.com/self", Rel: "self", Type: "application/atom+xml"},
	})
	if got != "https://example.com/edit" {
		t.Fatalf("bestLink fallback = %q", got)
	}
}
