package simhash

import "testing"

func TestComputeDeterministic(t *testing.T) {
	text := "the quick brown fox jumps over the lazy dog"
	if Compute(text) != Compute(text) {
		t.Error("Compute is not deterministic")
	}
}

func TestComputeEmpty(t *testing.T) {
	if Compute("") != 0 {
		t.Error("empty text should hash to 0")
	}
	if Compute("   \n\t ") != 0 {
		t.Error("whitespace-only text should hash to 0")
	}
}

func TestDistanceSelf(t *testing.T) {
	h := Compute("some representative document text here")
	if d := Distance(h, h); d != 0 {
		t.Errorf("Distance to self = %d, want 0", d)
	}
}

func TestNearDuplicateIsClose(t *testing.T) {
	// A one-word edit in a longer document should stay within a small Hamming
	// distance — that's the property near-dup detection relies on.
	base := "artificial intelligence is transforming how we search and organize information across the web every day"
	edited := "artificial intelligence is transforming how we search and organize knowledge across the web every day"
	d := Distance(Compute(base), Compute(edited))
	if d > 12 {
		t.Errorf("near-duplicate distance = %d, expected small (<=12)", d)
	}
}

func TestUnrelatedIsFar(t *testing.T) {
	a := Compute("quarterly financial earnings report for the technology sector")
	b := Compute("a poem about springtime flowers rivers and quiet mountains")
	if d := Distance(a, b); d < 12 {
		t.Errorf("unrelated distance = %d, expected large (>=12)", d)
	}
}

func TestDistanceSymmetric(t *testing.T) {
	a := Compute("first document about oceans")
	b := Compute("second document about deserts")
	if Distance(a, b) != Distance(b, a) {
		t.Error("Distance should be symmetric")
	}
}
