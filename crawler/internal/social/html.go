package social

import (
	"strings"

	"golang.org/x/net/html"
)

// stripTags removes HTML tags from a fragment, unescaping entities and turning
// line-breaking tags (<br>, </p>) into spaces. Whitespace is not yet collapsed;
// callers wrap this with collapseWS.
func stripTags(s string) string {
	if s == "" {
		return ""
	}
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break // includes io.EOF
		}
		switch tt {
		case html.TextToken:
			b.Write(z.Text()) // Text() returns unescaped text
		case html.StartTagToken, html.SelfClosingTagToken:
			if name, _ := z.TagName(); string(name) == "br" {
				b.WriteByte(' ')
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); string(name) == "p" {
				b.WriteByte(' ')
			}
		}
	}
	return b.String()
}
