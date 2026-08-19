package fetch

import "testing"

func TestIsHTML(t *testing.T) {
	for _, ct := range []string{"text/html", "text/html; charset=utf-8", "application/xhtml+xml"} {
		if !IsHTML(ct) {
			t.Errorf("IsHTML(%q) = false, want true", ct)
		}
	}
	for _, ct := range []string{"text/plain", "application/json", ""} {
		if IsHTML(ct) {
			t.Errorf("IsHTML(%q) = true, want false", ct)
		}
	}
}

func TestIsText(t *testing.T) {
	for _, ct := range []string{
		"text/plain", "text/plain; charset=utf-8", "text/markdown", "text/csv", "TEXT/PLAIN",
	} {
		if !IsText(ct) {
			t.Errorf("IsText(%q) = false, want true", ct)
		}
	}
	for _, ct := range []string{
		"text/html", "text/html; charset=utf-8", "application/json", "application/pdf", "image/png", "",
	} {
		if IsText(ct) {
			t.Errorf("IsText(%q) = true, want false", ct)
		}
	}
}
