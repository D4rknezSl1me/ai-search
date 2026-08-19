package extract

import (
	"regexp"
	"sort"
	"strings"
)

// Keyphrase extraction via RAKE (Rapid Automatic Keyword Extraction): split the
// text into candidate phrases at stopwords and punctuation, score each word by
// degree/frequency, and rank phrases by the sum of their word scores. Pure and
// dependency-free (docs/05 §9 enrichment). Recall-first: cheap topic tags that
// enrich a document for faceting and lexical matching, computed for every doc.

// Punctuation runs (but NOT whitespace) mark phrase breaks, so multi-word
// phrases stay intact while sentence/clause boundaries split them.
var kpNonWord = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)

const kpMaxWords = 4 // drop over-long candidate phrases (RAKE noise)

// A compact English stopword set — enough to break phrases at function words.
var kpStopwords = map[string]bool{
	"a": true, "about": true, "above": true, "after": true, "again": true, "all": true,
	"am": true, "an": true, "and": true, "any": true, "are": true, "as": true, "at": true,
	"be": true, "because": true, "been": true, "before": true, "being": true, "below": true,
	"between": true, "both": true, "but": true, "by": true, "can": true, "did": true, "do": true,
	"does": true, "doing": true, "down": true, "during": true, "each": true, "few": true,
	"for": true, "from": true, "further": true, "had": true, "has": true, "have": true,
	"having": true, "he": true, "her": true, "here": true, "hers": true, "him": true, "his": true,
	"how": true, "i": true, "if": true, "in": true, "into": true, "is": true, "it": true,
	"its": true, "just": true, "me": true, "more": true, "most": true, "my": true, "no": true,
	"nor": true, "not": true, "now": true, "of": true, "off": true, "on": true, "once": true,
	"only": true, "or": true, "other": true, "our": true, "out": true, "over": true, "own": true,
	"per": true, "s": true, "same": true, "she": true, "should": true, "so": true, "some": true,
	"such": true, "t": true, "than": true, "that": true, "the": true, "their": true, "them": true,
	"then": true, "there": true, "these": true, "they": true, "this": true, "those": true,
	"through": true, "to": true, "too": true, "under": true, "until": true, "up": true,
	"very": true, "was": true, "we": true, "were": true, "what": true, "when": true, "where": true,
	"which": true, "while": true, "who": true, "whom": true, "why": true, "will": true,
	"with": true, "would": true, "you": true, "your": true,
}

// Keyphrases returns up to max ranked keyphrases (lowercased) for the text.
func Keyphrases(text string, max int) []string {
	if max <= 0 {
		max = 8
	}
	// Mark hard breaks (punctuation) so candidate phrases don't cross them.
	marked := kpNonWord.ReplaceAllString(strings.ToLower(text), " | ")
	fields := strings.Fields(marked)

	var phrases [][]string
	var cur []string
	flush := func() {
		if len(cur) > 0 && len(cur) <= kpMaxWords {
			phrases = append(phrases, cur)
		}
		cur = nil
	}
	for _, f := range fields {
		if f == "|" || kpStopwords[f] || len(f) < 2 {
			flush()
			continue
		}
		cur = append(cur, f)
	}
	flush()

	// Word scores: degree (sum of phrase lengths a word appears in) / frequency.
	freq := map[string]int{}
	degree := map[string]int{}
	for _, p := range phrases {
		for _, w := range p {
			freq[w]++
			degree[w] += len(p)
		}
	}

	type scored struct {
		phrase string
		score  float64
	}
	seen := map[string]bool{}
	var ranked []scored
	for _, p := range phrases {
		key := strings.Join(p, " ")
		if seen[key] {
			continue
		}
		seen[key] = true
		var s float64
		for _, w := range p {
			s += float64(degree[w]) / float64(freq[w])
		}
		ranked = append(ranked, scored{key, s})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	out := make([]string, 0, max)
	for _, r := range ranked {
		out = append(out, r.phrase)
		if len(out) >= max {
			break
		}
	}
	return out
}
