package roster

import (
	"fmt"
	"sync"
	"testing"

	"github.com/luxfi/aml/internal/source"
)

// TestAdmissionNeverTakesAnothersPlace: the property the whole package exists
// for. A roster at its ceiling refuses the arrival; it does not make room.
func TestAdmissionNeverTakesAnothersPlace(t *testing.T) {
	r := New[int](2)

	for i, org := range []string{"hanzo/one", "hanzo/two"} {
		if v, ok := r.Hold(org, func() int { return i }); !ok || v != i {
			t.Fatalf("%s was not admitted into an empty roster", org)
		}
	}
	if _, ok := r.Hold("hanzo/three", func() int { return 3 }); ok {
		t.Errorf("a third tenant was admitted into a roster of two")
	}
	if r.Refused() != 1 {
		t.Errorf("refused = %d, want 1", r.Refused())
	}
	for i, org := range []string{"hanzo/one", "hanzo/two"} {
		if v, ok := r.Get(org); !ok || v != i {
			t.Errorf("%s lost its state to a tenant that was refused: %v %v", org, v, ok)
		}
	}
}

// TestMakeIsNotPaidForARefusal: building a tenant's state costs — a model is
// megabytes of trees. A roster that built one and then threw it away would let a
// refused tenant spend the memory it was refused for.
func TestMakeIsNotPaidForARefusal(t *testing.T) {
	r := New[int](1)
	r.Hold("hanzo/one", func() int { return 1 })

	built := 0
	if _, ok := r.Hold("hanzo/two", func() int { built++; return 2 }); ok {
		t.Fatal("admitted past the ceiling")
	}
	if built != 0 {
		t.Errorf("state was built %d times for a tenant that was refused", built)
	}
}

// TestPutReplacesOnlyTheTenantNamed: a restore rebuilds one tenant's state. It
// must be bounded like an admission and it must not be able to reach anybody
// else's.
func TestPutReplacesOnlyTheTenantNamed(t *testing.T) {
	r := New[int](2)
	r.Hold("hanzo/one", func() int { return 1 })
	r.Hold("hanzo/two", func() int { return 2 })

	if !r.Put("hanzo/one", 11) {
		t.Fatal("replacing a held tenant's own state was refused")
	}
	if v, _ := r.Get("hanzo/one"); v != 11 {
		t.Errorf("hanzo/one = %d after its own state was replaced, want 11", v)
	}
	if v, _ := r.Get("hanzo/two"); v != 2 {
		t.Errorf("hanzo/two = %d after another tenant was replaced, want 2", v)
	}
	if r.Put("hanzo/three", 3) {
		t.Error("a new tenant was installed past the ceiling")
	}
}

// TestConcurrentArrivalsAdmitOnce: two first transactions from one tenant arrive
// together. One state, one admission, and the ceiling still holds.
func TestConcurrentArrivalsAdmitOnce(t *testing.T) {
	r := New[*int](4)

	var wg sync.WaitGroup
	seen := make([]*int, 32)
	for i := range seen {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, _ := r.Hold("hanzo/acme", func() *int { n := 0; return &n })
			seen[i] = v
		}()
	}
	wg.Wait()
	for i, v := range seen {
		if v != seen[0] {
			t.Fatalf("goroutine %d got a different state for one tenant", i)
		}
	}
	if r.Held() != 1 {
		t.Errorf("held = %d after 32 concurrent arrivals of one tenant", r.Held())
	}
}

// TestEachSeesEveryTenant: the state views report what the process is holding
// and for whom, so a refused or absent tenant is visible rather than inferred.
func TestEachSeesEveryTenant(t *testing.T) {
	r := New[int](8)
	for i := range 5 {
		r.Hold(fmt.Sprintf("hanzo/%d", i), func() int { return i })
	}
	n := 0
	r.Each(func(string, int) bool { n++; return true })
	if n != 5 {
		t.Errorf("Each visited %d of 5", n)
	}
	n = 0
	r.Each(func(string, int) bool { n++; return false })
	if n != 1 {
		t.Errorf("Each visited %d after the visitor stopped, want 1", n)
	}
}

// TestNothingIsRemoved reads this package's own source.
//
// Admission-never-eviction is a property of the operations that EXIST. A Drop
// added here for a good reason is a Drop a cap can be built on, and the shape
// this package was written to make unreachable is reachable again.
func TestNothingIsRemoved(t *testing.T) {
	source.NoRemoval(t, "roster.go")
}
