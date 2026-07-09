// Package render decides whether a statically-fetched page needs a headless
// browser to render its content. It is the escalation gate at the front of the
// browser-worker path: cheap Go HTTP fetches handle the static majority, and
// only pages whose real content is locked behind JavaScript are re-routed to
// the (expensive) Playwright pool.
//
// The actual browser worker lands separately; this package is the decision
// layer so escalation is observable end-to-end before the pool exists.
package render

import (
	"bytes"
	"strings"
)

// Mode is the per-campaign render policy (the `render_js` config field).
type Mode int

const (
	// ModeAuto escalates only when heuristics flag a JS-dependent page.
	ModeAuto Mode = iota
	// ModeNever disables browser escalation entirely.
	ModeNever
	// ModeAlways routes every page through the browser worker.
	ModeAlways
)

// ParseMode maps the config string (never | auto | always) to a Mode,
// defaulting to auto for empty/unknown values.
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "never":
		return ModeNever
	case "always":
		return ModeAlways
	default:
		return ModeAuto
	}
}

func (m Mode) String() string {
	switch m {
	case ModeNever:
		return "never"
	case ModeAlways:
		return "always"
	default:
		return "auto"
	}
}

// Decision is the escalation verdict plus the signals that produced it, so the
// reasons can be stamped into document metadata and surfaced in metrics.
type Decision struct {
	Needs   bool
	Reasons []string
}

// Text-length thresholds (bytes of extracted plain text). Tuned so a normal
// article — even a short one — stays on the static path, while an app shell
// that renders "Loading…" or nothing at all escalates.
const (
	nearEmptyText = 120
	lowText       = 400
	// markerScanLimit bounds the case-insensitive marker scan; SPA roots and
	// framework bundles live in the <head>/shell near the top of the document.
	markerScanLimit = 512 * 1024
)

// spaMarkers are mount points frameworks hydrate into. Their presence together
// with little extracted text is a strong signal the content is client-rendered.
var spaMarkers = []string{
	`id="root"`, `id='root'`,
	`id="app"`, `id='app'`,
	`id="__next"`, // Next.js
	`id="__nuxt"`, `__nuxt__`, // Nuxt
	`id="___gatsby"`, // Gatsby
	`data-reactroot`, `data-react-helmet`,
	`ng-app`, `ng-version`, // Angular
	`data-server-rendered`, // Vue SSR hydration marker
	`window.__initial_state__`,
	`window.__apollo_state__`,
}

// frameworkBundles are script paths/keywords that ship a JS app.
var frameworkBundles = []string{
	`_next/static`, `/static/js/`, `webpack`, `runtime.`,
	`react-dom`, `vue.runtime`, `angular`, `svelte`, `polyfills`,
}

// NeedsRender inspects a static fetch and returns whether the page should be
// re-fetched with a JS-capable browser. rawHTML is the fetched body,
// extractedText is what the static extractor recovered, and linkCount is the
// number of discovered outlinks (an app shell typically has almost none).
//
// Bias is recall-first but escalation is costly, so ModeAuto requires BOTH
// sparse extracted text AND a positive JS-app signal before escalating.
func NeedsRender(mode Mode, rawHTML []byte, extractedText string, linkCount int) Decision {
	switch mode {
	case ModeNever:
		return Decision{Needs: false}
	case ModeAlways:
		return Decision{Needs: true, Reasons: []string{"mode=always"}}
	}

	textLen := len(strings.TrimSpace(extractedText))
	// Plenty of real text was recovered — no browser needed regardless of shell.
	if textLen >= lowText {
		return Decision{Needs: false}
	}

	scan := rawHTML
	if len(scan) > markerScanLimit {
		scan = scan[:markerScanLimit]
	}
	low := bytes.ToLower(scan)

	hasSPA := containsAny(low, spaMarkers)
	hasBundle := containsAny(low, frameworkBundles)
	hasNoscriptPrompt := bytes.Contains(low, []byte("<noscript")) &&
		bytes.Contains(low, []byte("enable")) &&
		bytes.Contains(low, []byte("javascript"))
	hasScript := bytes.Contains(low, []byte("<script"))

	var reasons []string
	if textLen < nearEmptyText {
		reasons = append(reasons, "near_empty_text")
	} else {
		reasons = append(reasons, "low_text")
	}

	needs := false
	if textLen < nearEmptyText && hasScript {
		needs = true
		reasons = append(reasons, "scripts_present")
	}
	if hasSPA {
		needs = true
		reasons = append(reasons, "spa_root_marker")
	}
	if hasNoscriptPrompt {
		needs = true
		reasons = append(reasons, "noscript_prompt")
	}
	if linkCount <= 3 && hasBundle {
		needs = true
		reasons = append(reasons, "framework_bundle")
	}

	if !needs {
		return Decision{Needs: false}
	}
	return Decision{Needs: true, Reasons: reasons}
}

func containsAny(haystack []byte, needles []string) bool {
	for _, n := range needles {
		if bytes.Contains(haystack, []byte(n)) {
			return true
		}
	}
	return false
}
