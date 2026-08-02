package velocity

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func key() Key { return Key{OrgID: "acme", Kind: "account", Value: "A-1"} }

func find(obs []Observation, name string) Observation {
	for _, o := range obs {
		if o.Window == name {
			return o
		}
	}
	return Observation{}
}

// The typology this package exists for. Nine transactions at 9,400 against a
// 10,000 limit: every one is unremarkable alone, which is the whole point of
// structuring, and the aggregate is the only thing that can see it.
func TestStructuringIsDetectedWhenNoSingleTransactionIs(t *testing.T) {
	s := New(Config{})
	const threshold = 10_000.0

	for i := 0; i < 9; i++ {
		s.Record(key(), t0.Add(time.Duration(i)*3*time.Hour), 9_400, threshold)
	}

	obs := s.Observe(key())
	day := find(obs, "24h")
	if day.Near == 0 {
		t.Fatal("no near-threshold transactions counted — structuring is invisible")
	}

	found := DetectStructuring(obs, threshold, 5)
	if len(found) == 0 {
		t.Fatal("nine transactions at 9,400 under a 10,000 limit produced no finding")
	}
	// The finding must carry the numbers an investigator reads back, not a verdict.
	f := found[0]
	if f.Total <= 0 || f.Count < 5 || f.Threshold != threshold {
		t.Fatalf("finding is not self-describing: %+v", f)
	}
	if f.Days < 2 {
		t.Fatalf("spread across 27 hours reported as %d days", f.Days)
	}
}

// A transaction AT or ABOVE the threshold is reported, not structured. Counting it
// would flag the honest case, which is the expensive kind of false positive.
func TestAtOrAboveThresholdIsNotStructuring(t *testing.T) {
	s := New(Config{})
	const threshold = 10_000.0
	for i := 0; i < 6; i++ {
		s.Record(key(), t0.Add(time.Duration(i)*time.Hour), 10_000, threshold) // exactly at
		s.Record(key(), t0.Add(time.Duration(i)*time.Hour), 25_000, threshold) // above
	}
	obs := s.Observe(key())
	if n := find(obs, "24h").Near; n != 0 {
		t.Fatalf("at/above-threshold transactions counted as near: %d", n)
	}
	if len(DetectStructuring(obs, threshold, 3)) != 0 {
		t.Fatal("reportable transactions produced a structuring finding")
	}
	// They must still count toward the plain aggregate — they are not invisible.
	if c := find(obs, "24h").Count; c != 12 {
		t.Fatalf("plain count = %d, want 12", c)
	}
}

// Small amounts far below the limit are ordinary activity, not evasion.
func TestFarBelowThresholdIsNotNear(t *testing.T) {
	s := New(Config{})
	for i := 0; i < 20; i++ {
		s.Record(key(), t0.Add(time.Duration(i)*time.Minute), 40, 10_000)
	}
	if n := find(s.Observe(key()), "24h").Near; n != 0 {
		t.Fatalf("20 transactions at 40 against a 10,000 limit gave near=%d", n)
	}
}

// The window must actually slide: activity older than the span leaves it.
func TestWindowExpiresOldActivity(t *testing.T) {
	s := New(Config{Windows: []Window{{Name: "1h", Span: time.Hour, Buckets: 60}}})
	s.Record(key(), t0, 500, 0)
	if c := find(s.Observe(key()), "1h").Count; c != 1 {
		t.Fatalf("immediately after recording, count = %d, want 1", c)
	}
	// Advance well past the span with a fresh event; the old one must be gone.
	//
	// The offset must NOT be a whole multiple of the span, or the new event lands on
	// the SAME ring slot as the old one and overwrites it — the test would then pass
	// on slot aliasing rather than on expiry. Mutation-proved: neutering the
	// staleness check survived a 3h offset (180 buckets, 180%60==0) and dies on this
	// one (210 buckets, 210%60==30, a different slot).
	s.Record(key(), t0.Add(3*time.Hour+30*time.Minute), 500, 0)
	o := find(s.Observe(key()), "1h")
	if o.Count != 1 {
		t.Fatalf("after sliding 3h past a 1h window, count = %d, want 1 (only the new event)", o.Count)
	}
	if o.Sum != 500 {
		t.Fatalf("expired amount still in sum: %v", o.Sum)
	}
}

// Keys must not bleed into each other, and org is part of the key. Two accounts
// with the same value in different orgs are different entities — treating them as
// one would be a cross-tenant leak in a compliance aggregate.
func TestKeysAndOrgsAreIsolated(t *testing.T) {
	s := New(Config{})
	a := Key{OrgID: "acme", Kind: "account", Value: "SHARED"}
	b := Key{OrgID: "other", Kind: "account", Value: "SHARED"}
	c := Key{OrgID: "acme", Kind: "device", Value: "SHARED"}

	for i := 0; i < 5; i++ {
		s.Record(a, t0.Add(time.Duration(i)*time.Minute), 100, 0)
	}
	if n := find(s.Observe(b), "24h").Count; n != 0 {
		t.Fatalf("another ORG saw %d of acme's transactions", n)
	}
	if n := find(s.Observe(c), "24h").Count; n != 0 {
		t.Fatalf("another KIND saw %d of the account's transactions", n)
	}
	if n := find(s.Observe(a), "24h").Count; n != 5 {
		t.Fatalf("own key count = %d, want 5", n)
	}
}

// Cardinality must be bounded. Key values come from caller data, so an unbounded
// map is a memory-exhaustion surface reachable by anyone who can submit a
// transaction. The bound is stated in BYTES and the key count is derived from
// it — see Config and bound_test.go for why a count on its own was not one.
func TestKeyCardinalityIsBounded(t *testing.T) {
	s := New(Config{PerOrg: 640 * keyCost(StandardWindows())})
	for i := 0; i < 20_000; i++ {
		s.Record(Key{OrgID: "acme", Kind: "account", Value: fmt.Sprintf("A-%d", i)},
			t0.Add(time.Duration(i)*time.Second), 10, 0)
	}
	if got := s.Keys(); got > 640 {
		t.Fatalf("20,000 distinct keys grew the store to %d entries — cardinality is unbounded", got)
	}
	if s.Bytes() > s.Ceiling() {
		t.Fatalf("the store holds %d bytes against its published ceiling of %d", s.Bytes(), s.Ceiling())
	}
}

// Late arrival must be counted somewhere and surfaced. Dropping an event from a
// compliance aggregate is worse than misplacing it in time; doing either silently
// is worse still.
func TestLateArrivalsAreCountedAndReported(t *testing.T) {
	s := New(Config{Windows: []Window{{Name: "1h", Span: time.Hour, Buckets: 60}}})
	s.Record(key(), t0.Add(10*time.Hour), 100, 0) // establish the leading edge
	before := find(s.Observe(key()), "1h").Count

	s.Record(key(), t0, 100, 0) // 10 hours late against a 1h window

	if s.Late() == 0 {
		t.Fatal("a 10-hour-late event was not reported via Late()")
	}
	if after := find(s.Observe(key()), "1h").Count; after != before+1 {
		t.Fatalf("late event was DROPPED: count %d -> %d", before, after)
	}
}

// Deviation is the statutory test — relative to this customer, not to a constant.
func TestDeviationIsRelativeAndSafeOnEmptyHistory(t *testing.T) {
	if d := Deviation(300, 100); d != 2 {
		t.Fatalf("300 against a baseline of 100 = %v, want 2", d)
	}
	// A new customer has no baseline and must not read as infinitely anomalous.
	if d := Deviation(5_000_000, 0); d != 0 {
		t.Fatalf("no baseline gave deviation %v, want 0 — a new customer is not suspicious for being new", d)
	}
}

// Observations must carry their own error bound, so a reader is not misled into
// treating a quantised boundary as exact.
func TestObservationStatesItsResolution(t *testing.T) {
	for _, o := range New(Config{}).Observe(key()) {
		if o.Quantum <= 0 {
			t.Fatalf("window %q reports no quantum, so its boundary error is unstated", o.Window)
		}
		if o.Quantum > o.Span {
			t.Fatalf("window %q quantum %v exceeds its span %v", o.Window, o.Quantum, o.Span)
		}
	}
}

// The hot path is concurrent by construction; -race must be clean.
func TestConcurrentRecordAndObserve(t *testing.T) {
	s := New(Config{})
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			k := Key{OrgID: "acme", Kind: "account", Value: fmt.Sprintf("A-%d", w%3)}
			for i := 0; i < 2_000; i++ {
				s.Record(k, t0.Add(time.Duration(i)*time.Second), 9_400, 10_000)
				if i%64 == 0 {
					_ = s.Observe(k)
				}
			}
		}(w)
	}
	wg.Wait()
	if find(s.Observe(Key{OrgID: "acme", Kind: "account", Value: "A-0"}), "24h").Count == 0 {
		t.Fatal("concurrent records produced an empty aggregate")
	}
}

// The claim that makes this design worth its complexity: cost per transaction is
// independent of how many transactions preceded it. A query-per-evaluation design
// degrades with history; this must not.
func TestCostDoesNotGrowWithHistory(t *testing.T) {
	s := New(Config{})
	k := key()
	measure := func(n int) time.Duration {
		start := time.Now()
		for i := 0; i < n; i++ {
			s.Record(k, t0.Add(time.Duration(i)*time.Millisecond), 9_400, 10_000)
			_ = s.Observe(k)
		}
		return time.Since(start) / time.Duration(n)
	}
	first := measure(20_000)  // cold
	later := measure(200_000) // after 10x the history
	if later > first*4 {
		t.Fatalf("per-transaction cost grew with history: %v -> %v", first, later)
	}
}

func BenchmarkRecord(b *testing.B) {
	s := New(Config{})
	k := key()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Record(k, t0.Add(time.Duration(i)*time.Millisecond), 9_400, 10_000)
	}
}

func BenchmarkObserve(b *testing.B) {
	s := New(Config{})
	k := key()
	for i := 0; i < 100_000; i++ {
		s.Record(k, t0.Add(time.Duration(i)*time.Millisecond), 9_400, 10_000)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Observe(k)
	}
}
