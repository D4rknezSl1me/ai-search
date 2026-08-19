package commoncrawl

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

const collinfo = `[
  {"id":"CC-MAIN-2026-30","cdx-api":"https://index.commoncrawl.org/CC-MAIN-2026-30-index"},
  {"id":"CC-MAIN-2026-26","cdx-api":"https://index.commoncrawl.org/CC-MAIN-2026-26-index"}
]`

// CDX output=json is newline-delimited JSON objects.
func cdx(urls ...string) string {
	var b strings.Builder
	for _, u := range urls {
		fmt.Fprintf(&b, `{"urlkey":"x","timestamp":"2026","url":%q,"status":"200"}`+"\n", u)
	}
	return b.String()
}

// --------------------------------------------------------------- parse units ---

func TestParseCollinfo(t *testing.T) {
	apis, err := ParseCollinfo([]byte(collinfo))
	if err != nil {
		t.Fatal(err)
	}
	if len(apis) != 2 || apis[0] != "https://index.commoncrawl.org/CC-MAIN-2026-30-index" {
		t.Fatalf("apis = %v", apis)
	}
}

func TestParseCDXSkipsJunkAndNonHTTP(t *testing.T) {
	data := cdx("https://example.com/a", "https://example.com/b") +
		"\n" + `not json` + "\n" + `{"url":"ftp://example.com/x"}` + "\n"
	got := ParseCDX([]byte(data))
	if len(got) != 2 || got[0] != "https://example.com/a" || got[1] != "https://example.com/b" {
		t.Fatalf("cdx urls = %v (junk + non-http should be skipped)", got)
	}
}

// ------------------------------------------------------------------ Discover ---

// router returns canned bodies based on the requested URL.
func router(collinfoBody string, byIndex map[string]string) FetchFunc {
	return func(_ context.Context, u string) ([]byte, error) {
		if strings.Contains(u, "collinfo.json") {
			return []byte(collinfoBody), nil
		}
		for frag, body := range byIndex {
			if strings.Contains(u, frag) {
				return []byte(body), nil
			}
		}
		return nil, fmt.Errorf("no route for %s", u)
	}
}

func TestDiscoverResolvesCollinfoAndQueriesLatest(t *testing.T) {
	fetch := router(collinfo, map[string]string{
		"CC-MAIN-2026-30-index": cdx("https://example.com/a", "https://example.com/b"),
	})
	urls, err := Discover(context.Background(), "example.com", fetch, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 2 {
		t.Fatalf("urls = %v (default MaxIndexes=1 → only the newest crawl)", urls)
	}
}

func TestDiscoverBuildsDomainGlobQuery(t *testing.T) {
	var asked string
	fetch := func(_ context.Context, u string) ([]byte, error) {
		if strings.Contains(u, "collinfo.json") {
			return []byte(collinfo), nil
		}
		asked = u
		return []byte(cdx("https://example.com/a")), nil
	}
	if _, err := Discover(context.Background(), "example.com", fetch, Opts{}); err != nil {
		t.Fatal(err)
	}
	// domain/* url-encoded, json output, and a limit must all be present.
	for _, want := range []string{"url=example.com%2F%2A", "output=json", "limit="} {
		if !strings.Contains(asked, want) {
			t.Fatalf("query %q missing %q", asked, want)
		}
	}
}

func TestDiscoverMultiIndexDedupesAndCaps(t *testing.T) {
	fetch := router(collinfo, map[string]string{
		"CC-MAIN-2026-30-index": cdx("https://example.com/a", "https://example.com/b"),
		"CC-MAIN-2026-26-index": cdx("https://example.com/b", "https://example.com/c"),
	})
	urls, err := Discover(context.Background(), "example.com", fetch, Opts{MaxIndexes: 2})
	if err != nil {
		t.Fatal(err)
	}
	// a, b, c — b de-duplicated across the two crawls.
	if len(urls) != 3 {
		t.Fatalf("urls = %v, want 3 deduped", urls)
	}

	capped, err := Discover(context.Background(), "example.com", fetch, Opts{MaxIndexes: 2, MaxURLs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(capped) != 2 {
		t.Fatalf("urls = %v, want capped at 2", capped)
	}
}

func TestDiscoverExplicitIndexSkipsCollinfo(t *testing.T) {
	fetch := func(_ context.Context, u string) ([]byte, error) {
		if strings.Contains(u, "collinfo.json") {
			return nil, fmt.Errorf("collinfo should not be fetched")
		}
		return []byte(cdx("https://example.com/a")), nil
	}
	urls, err := Discover(context.Background(), "example.com", fetch, Opts{
		IndexURL: "https://index.commoncrawl.org/CC-MAIN-2026-30-index",
	})
	if err != nil || len(urls) != 1 {
		t.Fatalf("urls=%v err=%v", urls, err)
	}
}

func TestDiscoverEmptyDomainErrors(t *testing.T) {
	_, err := Discover(context.Background(), "  ", nil, Opts{})
	if err == nil {
		t.Fatal("expected an error for an empty domain")
	}
}

func TestDiscoverCollinfoFetchErrorFatal(t *testing.T) {
	fetch := func(_ context.Context, _ string) ([]byte, error) { return nil, fmt.Errorf("down") }
	if _, err := Discover(context.Background(), "example.com", fetch, Opts{}); err == nil {
		t.Fatal("expected a fatal error when collinfo can't be fetched")
	}
}
