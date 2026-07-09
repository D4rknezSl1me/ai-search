// Package urlx canonicalizes URLs and derives stable hashes for deduplication.
package urlx

import (
	"crypto/sha256"
	"net/url"
	"sort"
	"strings"
)

// trackingParams are stripped during canonicalization (they don't change content).
var trackingParams = map[string]bool{
	"utm_source": true, "utm_medium": true, "utm_campaign": true,
	"utm_term": true, "utm_content": true, "gclid": true, "fbclid": true,
	"mc_cid": true, "mc_eid": true, "ref": true, "ref_src": true,
}

// Canonicalize normalizes a URL so equivalent URLs hash identically:
// lowercased scheme/host, no fragment, sorted query, tracking params removed,
// default ports and trailing "/" on empty paths handled.
func Canonicalize(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""

	// Drop default ports.
	host := u.Host
	if (u.Scheme == "http" && strings.HasSuffix(host, ":80")) ||
		(u.Scheme == "https" && strings.HasSuffix(host, ":443")) {
		host = host[:strings.LastIndex(host, ":")]
		u.Host = host
	}

	if u.Path == "" {
		u.Path = "/"
	}

	// Rebuild query without tracking params, sorted for stability.
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			if trackingParams[strings.ToLower(k)] {
				q.Del(k)
			}
		}
		keys := make([]string, 0, len(q))
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			vs := q[k]
			sort.Strings(vs)
			for _, v := range vs {
				if b.Len() > 0 {
					b.WriteByte('&')
				}
				b.WriteString(url.QueryEscape(k))
				b.WriteByte('=')
				b.WriteString(url.QueryEscape(v))
			}
		}
		u.RawQuery = b.String()
	}

	return u.String(), nil
}

// Hash returns the SHA-256 of a (canonical) URL as raw bytes for storage.
func Hash(canonical string) []byte {
	sum := sha256.Sum256([]byte(canonical))
	return sum[:]
}

// Host returns the lowercased host of a URL, or "" on parse failure.
func Host(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// Resolve turns a possibly-relative href into an absolute URL against base.
func Resolve(base, href string) (string, bool) {
	b, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	r, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return "", false
	}
	abs := b.ResolveReference(r)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return "", false
	}
	return abs.String(), true
}

// SameRegisteredDomain is a cheap heuristic: compares the last two labels of
// each host (e.g. "a.example.com" and "example.com" match on "example.com").
func SameRegisteredDomain(a, b string) bool {
	return registered(a) == registered(b)
}

func registered(host string) string {
	host = strings.ToLower(host)
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}
