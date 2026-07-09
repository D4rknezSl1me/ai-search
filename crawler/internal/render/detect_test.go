package render

import (
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"never": ModeNever, "NEVER": ModeNever,
		"always": ModeAlways, " Always ": ModeAlways,
		"auto": ModeAuto, "": ModeAuto, "garbage": ModeAuto,
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestModeString(t *testing.T) {
	for _, m := range []Mode{ModeNever, ModeAuto, ModeAlways} {
		if ParseMode(m.String()) != m {
			t.Errorf("round-trip failed for %v (%q)", m, m.String())
		}
	}
}

// A rich article stays on the static path even under auto.
func TestNeedsRender_ArticleStaysStatic(t *testing.T) {
	html := []byte(`<html><body><article><p>` +
		strings.Repeat("Real content here. ", 100) + `</p></article></body></html>`)
	text := strings.Repeat("Real content here. ", 100)
	d := NeedsRender(ModeAuto, html, text, 12)
	if d.Needs {
		t.Fatalf("article should not escalate, reasons=%v", d.Reasons)
	}
}

// A Next.js app shell with an empty root and no text escalates.
func TestNeedsRender_NextShell(t *testing.T) {
	html := []byte(`<html><head><script src="/_next/static/chunks/main.js"></script></head>` +
		`<body><div id="__next"></div></body></html>`)
	d := NeedsRender(ModeAuto, html, "", 0)
	if !d.Needs {
		t.Fatalf("Next.js shell should escalate")
	}
	if !hasReason(d.Reasons, "spa_root_marker") {
		t.Errorf("expected spa_root_marker, got %v", d.Reasons)
	}
}

// A React root with a hydration marker but almost no text escalates.
func TestNeedsRender_ReactRoot(t *testing.T) {
	html := []byte(`<html><body><div id="root" data-reactroot></div>` +
		`<script src="/static/js/bundle.js"></script></body></html>`)
	d := NeedsRender(ModeAuto, html, "Loading", 0)
	if !d.Needs {
		t.Fatalf("React root should escalate")
	}
}

// A noscript "please enable JavaScript" prompt with no text escalates.
func TestNeedsRender_NoscriptPrompt(t *testing.T) {
	html := []byte(`<html><body><noscript>You need to enable JavaScript to run this app.</noscript>` +
		`<div id="mount"></div></body></html>`)
	d := NeedsRender(ModeAuto, html, "", 0)
	if !d.Needs {
		t.Fatalf("noscript prompt should escalate")
	}
	if !hasReason(d.Reasons, "noscript_prompt") {
		t.Errorf("expected noscript_prompt, got %v", d.Reasons)
	}
}

// Sparse text but a normal link-rich page (e.g. a nav/index) does NOT escalate
// just for being short — there must be a JS-app signal.
func TestNeedsRender_ShortButNoSignal(t *testing.T) {
	html := []byte(`<html><body><ul>` +
		`<li><a href="/a">A</a></li><li><a href="/b">B</a></li></ul></body></html>`)
	d := NeedsRender(ModeAuto, html, "A B", 8)
	if d.Needs {
		t.Fatalf("short link page without JS signal should not escalate, reasons=%v", d.Reasons)
	}
}

func TestNeedsRender_ModeNever(t *testing.T) {
	html := []byte(`<div id="__next"></div>`)
	if NeedsRender(ModeNever, html, "", 0).Needs {
		t.Fatal("ModeNever must never escalate")
	}
}

func TestNeedsRender_ModeAlways(t *testing.T) {
	d := NeedsRender(ModeAlways, []byte(`<article>plenty of text...</article>`),
		strings.Repeat("x", 5000), 50)
	if !d.Needs {
		t.Fatal("ModeAlways must always escalate")
	}
	if !hasReason(d.Reasons, "mode=always") {
		t.Errorf("expected mode=always reason, got %v", d.Reasons)
	}
}

func hasReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}
