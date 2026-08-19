package extract

import (
	"strings"
	"testing"
)

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestKeyphrasesSurfacesSalientMultiwordTerms(t *testing.T) {
	text := "Machine learning models require large training datasets. " +
		"Deep learning is a subfield of machine learning. " +
		"Training datasets for machine learning must be carefully curated. " +
		"Neural networks power modern deep learning systems."
	kps := Keyphrases(text, 8)
	if len(kps) == 0 {
		t.Fatal("expected some keyphrases")
	}
	// "machine learning" and "training datasets" recur and should rank as phrases.
	if !contains(kps, "machine learning") {
		t.Errorf("expected 'machine learning' in %v", kps)
	}
	if !contains(kps, "training datasets") {
		t.Errorf("expected 'training datasets' in %v", kps)
	}
}

func TestKeyphrasesBreaksAtStopwordsAndPunctuation(t *testing.T) {
	// Stopwords ("of", "the") and punctuation must not appear inside a phrase.
	for _, kp := range Keyphrases("the quality of service, and the cost of goods.", 8) {
		for _, w := range strings.Fields(kp) {
			if kpStopwords[w] {
				t.Errorf("keyphrase %q contains a stopword %q", kp, w)
			}
		}
		if strings.ContainsAny(kp, ",.") {
			t.Errorf("keyphrase %q contains punctuation", kp)
		}
	}
}

func TestKeyphrasesRespectsMaxAndEmpty(t *testing.T) {
	if got := Keyphrases("", 8); len(got) != 0 {
		t.Errorf("empty text → %v, want none", got)
	}
	long := strings.Repeat("alpha beta gamma delta epsilon zeta eta theta. ", 20)
	if got := Keyphrases(long, 3); len(got) > 3 {
		t.Errorf("returned %d keyphrases, want ≤3", len(got))
	}
}

func TestKeyphrasesDropsOverLongCandidates(t *testing.T) {
	// A run of >kpMaxWords content words with no stopword/punctuation is dropped.
	text := "one two three four five six seven eight nine ten eleven twelve"
	for _, kp := range Keyphrases(text, 8) {
		if n := len(strings.Fields(kp)); n > kpMaxWords {
			t.Errorf("keyphrase %q has %d words, want ≤%d", kp, n, kpMaxWords)
		}
	}
}
