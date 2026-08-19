package extract

import (
	"strings"
	"testing"
)

func TestFromPlainText(t *testing.T) {
	body := "The First Title Line\n\n" +
		"This is the body of a plain text document describing various animals " +
		"such as cats, dogs, horses and rabbits in complete English sentences."
	doc, err := FromPlainText("http://example.com/notes.txt", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "The First Title Line" {
		t.Errorf("title = %q", doc.Title)
	}
	if !strings.Contains(doc.Text, "body of a plain text document") {
		t.Errorf("text missing body: %q", doc.Text)
	}
	if len(doc.ContentHash) != 32 {
		t.Errorf("content hash len = %d, want 32", len(doc.ContentHash))
	}
	if doc.Simhash == 0 {
		t.Error("simhash not computed")
	}
	if doc.Lang != "en" {
		t.Errorf("lang = %q, want en", doc.Lang)
	}
	if len(doc.Links) != 0 {
		t.Errorf("plain text should have no links, got %v", doc.Links)
	}
	if doc.Excerpt == "" {
		t.Error("excerpt empty")
	}
}

func TestFromPlainTextEmpty(t *testing.T) {
	doc, err := FromPlainText("http://example.com/e.txt", []byte("   \n  \n"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Text != "" || doc.Title != "" {
		t.Errorf("expected empty text/title, got text=%q title=%q", doc.Text, doc.Title)
	}
}

func TestTruncateRunesIsUTF8Safe(t *testing.T) {
	// Multibyte runes must not be split mid-sequence.
	s := strings.Repeat("é", 300) // each é is 2 bytes, 1 rune
	got := truncateRunes(s, 200)
	if len([]rune(got)) != 200 {
		t.Errorf("rune length = %d, want 200", len([]rune(got)))
	}
	if !isValidUTF8(got) {
		t.Error("truncation split a UTF-8 sequence")
	}
}

func TestFirstLineSkipsBlankLines(t *testing.T) {
	if got := firstLine("\n\n   \nReal Title\nmore"); got != "Real Title" {
		t.Errorf("firstLine = %q", got)
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
