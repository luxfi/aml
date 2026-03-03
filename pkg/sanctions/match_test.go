package sanctions

import (
	"math"
	"testing"
)

func TestJaroWinklerExact(t *testing.T) {
	score := JaroWinkler("john smith", "john smith")
	if score != 1.0 {
		t.Errorf("exact match: got %f, want 1.0", score)
	}
}

func TestJaroWinklerEmpty(t *testing.T) {
	if JaroWinkler("", "abc") != 0.0 {
		t.Error("empty s1 should return 0")
	}
	if JaroWinkler("abc", "") != 0.0 {
		t.Error("empty s2 should return 0")
	}
}

func TestJaroWinklerSimilar(t *testing.T) {
	score := JaroWinkler("john smith", "jon smith")
	if score < 0.9 {
		t.Errorf("similar names: got %f, want >= 0.9", score)
	}
}

func TestJaroWinklerDifferent(t *testing.T) {
	score := JaroWinkler("john smith", "alice jones")
	if score > 0.7 {
		t.Errorf("different names: got %f, want < 0.7", score)
	}
}

func TestJaroWinklerCaseInsensitive(t *testing.T) {
	s1 := JaroWinkler("John Smith", "john smith")
	s2 := JaroWinkler("JOHN SMITH", "john smith")
	if s1 != 1.0 || s2 != 1.0 {
		t.Errorf("case insensitive: got %f, %f — want 1.0 for both", s1, s2)
	}
}

func TestJaroWinklerCommonPrefix(t *testing.T) {
	// Winkler bonus rewards common prefix.
	withPrefix := JaroWinkler("johnson", "johnsn")
	noPrefix := JaroWinkler("nohnson", "noahson")
	if withPrefix <= noPrefix {
		t.Errorf("common prefix should score higher: withPrefix=%f, noPrefix=%f", withPrefix, noPrefix)
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"John Smith", "johnsmith"},
		{"O'Brien", "obrien"},
		{"Al-Rashid", "alrashid"},
		{"  spaces  ", "spaces"},
		{"Números123", "números123"},
	}
	for _, tt := range tests {
		got := normalize(tt.in)
		if got != tt.want {
			t.Errorf("normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTokenMatch(t *testing.T) {
	score := TokenMatch("John Smith", "Smith John")
	if score < 0.95 {
		t.Errorf("reordered tokens: got %f, want >= 0.95", score)
	}
}

func TestTokenMatchPartial(t *testing.T) {
	score := TokenMatch("John A Smith", "John Smith")
	// "John" matches perfectly, "A" doesn't match well, "Smith" matches perfectly.
	if score < 0.6 {
		t.Errorf("partial token match: got %f, want >= 0.6", score)
	}
}

func TestTokenMatchEmpty(t *testing.T) {
	if TokenMatch("", "anything") != 0.0 {
		t.Error("empty input should return 0")
	}
	if TokenMatch("anything", "") != 0.0 {
		t.Error("empty candidate should return 0")
	}
}

func approxEqual(a, b, tolerance float64) bool {
	return math.Abs(a-b) < tolerance
}

func TestJaroWinklerSymmetric(t *testing.T) {
	s1 := JaroWinkler("martha", "marhta")
	s2 := JaroWinkler("marhta", "martha")
	if !approxEqual(s1, s2, 0.001) {
		t.Errorf("JaroWinkler should be symmetric: %f vs %f", s1, s2)
	}
}

func TestJaroWinklerKnownValue(t *testing.T) {
	// Classic Jaro-Winkler example: MARTHA/MARHTA.
	score := JaroWinkler("martha", "marhta")
	if score < 0.96 {
		t.Errorf("MARTHA/MARHTA: got %f, want >= 0.96", score)
	}
}
