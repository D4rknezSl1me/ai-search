package urlx

import "testing"

func TestCanonicalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"lowercases scheme and host", "HTTP://Example.COM/Path", "http://example.com/Path"},
		{"drops fragment", "https://example.com/a#section", "https://example.com/a"},
		{"empty path becomes slash", "https://example.com", "https://example.com/"},
		{"strips default http port", "http://example.com:80/a", "http://example.com/a"},
		{"strips default https port", "https://example.com:443/a", "https://example.com/a"},
		{"keeps non-default port", "https://example.com:8443/a", "https://example.com:8443/a"},
		{"removes tracking params", "https://example.com/a?utm_source=x&id=7", "https://example.com/a?id=7"},
		{"sorts query keys", "https://example.com/a?b=2&a=1", "https://example.com/a?a=1&b=2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Canonicalize(c.in)
			if err != nil {
				t.Fatalf("Canonicalize(%q) error: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("Canonicalize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestCanonicalizeStable(t *testing.T) {
	// Equivalent URLs must canonicalize identically so they hash the same.
	a, _ := Canonicalize("https://Example.com/p?b=2&utm_medium=cpc&a=1#frag")
	b, _ := Canonicalize("https://example.com:443/p?a=1&b=2")
	if a != b {
		t.Errorf("equivalent URLs differ: %q vs %q", a, b)
	}
	if string(Hash(a)) != string(Hash(b)) {
		t.Errorf("hashes differ for equal canonical URLs")
	}
}

func TestHost(t *testing.T) {
	if got := Host("https://Sub.Example.com:8080/x"); got != "sub.example.com" {
		t.Errorf("Host = %q, want sub.example.com", got)
	}
	if got := Host("://bad"); got != "" {
		t.Errorf("Host(bad) = %q, want empty", got)
	}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		base, href, want string
		ok               bool
	}{
		{"https://example.com/dir/page", "../other", "https://example.com/other", true},
		{"https://example.com/dir/", "sub/x", "https://example.com/dir/sub/x", true},
		{"https://example.com/", "https://other.com/y", "https://other.com/y", true},
		{"https://example.com/", "mailto:a@b.com", "", false},
		{"https://example.com/", "javascript:void(0)", "", false},
	}
	for _, c := range cases {
		got, ok := Resolve(c.base, c.href)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("Resolve(%q,%q) = (%q,%v), want (%q,%v)", c.base, c.href, got, ok, c.want, c.ok)
		}
	}
}

func TestSameRegisteredDomain(t *testing.T) {
	if !SameRegisteredDomain("a.example.com", "example.com") {
		t.Error("subdomain should share registered domain")
	}
	if !SameRegisteredDomain("news.example.com", "shop.example.com") {
		t.Error("sibling subdomains should match")
	}
	if SameRegisteredDomain("example.com", "example.org") {
		t.Error("different TLDs should not match")
	}
}
