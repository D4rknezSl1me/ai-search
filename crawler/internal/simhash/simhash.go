// Package simhash computes a 64-bit SimHash fingerprint for near-duplicate
// detection over document text. See docs/05-EXTRACTION.md §6.
package simhash

import (
	"hash/fnv"
	"strings"
)

// Compute returns a 64-bit SimHash of the given text using word tokens.
func Compute(text string) uint64 {
	var v [64]int
	tokens := strings.Fields(strings.ToLower(text))
	if len(tokens) == 0 {
		return 0
	}
	for _, tok := range tokens {
		h := fnv.New64a()
		_, _ = h.Write([]byte(tok))
		sum := h.Sum64()
		for i := 0; i < 64; i++ {
			if sum&(1<<uint(i)) != 0 {
				v[i]++
			} else {
				v[i]--
			}
		}
	}
	var fingerprint uint64
	for i := 0; i < 64; i++ {
		if v[i] > 0 {
			fingerprint |= 1 << uint(i)
		}
	}
	return fingerprint
}

// Distance returns the Hamming distance between two fingerprints.
func Distance(a, b uint64) int {
	x := a ^ b
	count := 0
	for x != 0 {
		x &= x - 1
		count++
	}
	return count
}
