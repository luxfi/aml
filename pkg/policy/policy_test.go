package policy

import (
	"errors"
	"testing"
	"time"
)

func ladder() Policy {
	return Policy{
		Stage: "payment", Version: 3, At: time.Unix(1, 0).UTC(), By: "ops@acme",
		Reason: "the dispute rate on new cards doubled",
		Floor:  "allow",
		Rungs: []Rung{
			{At: 0.20, Action: "challenge"},
			{At: 0.55, Action: "review"},
			{At: 0.80, Action: "restrict"},
			{At: 0.95, Action: "block"},
		},
		Cost: Cost{Miss: 42_000_000_000, Alarm: 3_000_000_000},
	}
}

func TestActionReadsTheLadder(t *testing.T) {
	p, err := Seal(ladder())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		prob float64
		want string
	}{
		{0, "allow"}, {0.1999, "allow"},
		{0.20, "challenge"}, {0.5499, "challenge"},
		{0.55, "review"}, {0.7999, "review"},
		{0.80, "restrict"}, {0.9499, "restrict"},
		{0.95, "block"}, {1, "block"},
	} {
		if got := p.Action(c.prob); got != c.want {
			t.Errorf("p=%g decided %q, want %q", c.prob, got, c.want)
		}
	}
}

// The rung a decision cites has to be the one that actually decided, or an
// adverse-action notice names a threshold the customer did not cross.
func TestReachedNamesTheDecidingRung(t *testing.T) {
	p, _ := Seal(ladder())
	if r := p.Reached(0.1); r != nil {
		t.Errorf("the floor applied and a rung was cited: %+v", r)
	}
	r := p.Reached(0.9)
	if r == nil || r.Action != "restrict" || r.At != 0.80 {
		t.Fatalf("cited %+v, want the 0.80 restrict rung", r)
	}
}

// Shadow must compute everything and act on nothing. A shadow that short-circuits
// would prove nothing about the live path.
func TestShadowAlwaysTakesTheFloor(t *testing.T) {
	l := ladder()
	l.Shadow = true
	p, _ := Seal(l)
	for _, prob := range []float64{0, 0.5, 0.99, 1} {
		if got := p.Action(prob); got != "allow" {
			t.Errorf("in shadow p=%g acted %q", prob, got)
		}
		if r := p.Reached(prob); r != nil {
			t.Errorf("in shadow p=%g cited a rung", prob)
		}
	}
}

// Each of these is a real way a ladder silently stops being a risk policy.
func TestValidateRefusesEveryBrokenLadder(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*Policy)
		want error
	}{
		{"no stage", func(p *Policy) { p.Stage = "" }, ErrNoStage},
		{"no floor", func(p *Policy) { p.Floor = "" }, ErrNoFloor},
		{"no rungs", func(p *Policy) { p.Rungs = nil }, ErrNoRungs},
		{"rung above one", func(p *Policy) { p.Rungs[3].At = 1.5 }, ErrRange},
		{"rung below zero", func(p *Policy) { p.Rungs[0].At = -0.1 }, ErrRange},
		{"descending", func(p *Policy) { p.Rungs[2].At = 0.3 }, ErrOrder},
		{"duplicate", func(p *Policy) { p.Rungs[2].At = 0.55 }, ErrDuplicate},
		{"unnamed action", func(p *Policy) { p.Rungs[1].Action = "" }, ErrNoAction},
		{"dead rung", func(p *Policy) { p.Rungs[1].Action = "challenge" }, ErrSameAction},
		{"negative cost", func(p *Policy) { p.Cost.Miss = -1 }, ErrCost},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := ladder()
			c.mut(&p)
			if err := p.Validate(); !errors.Is(err, c.want) {
				t.Fatalf("accepted (err=%v), want %v", err, c.want)
			}
			if _, err := Seal(p); err == nil {
				t.Fatal("Seal minted a digest for a ladder that does not validate")
			}
		})
	}
}

// The digest identifies what a policy DECIDES. Two policies that decide
// identically must share it, or a backtest of one is not a backtest of the
// other; two that decide differently must not.
func TestDigestIsOverTheDecisionAndNothingElse(t *testing.T) {
	a, _ := Seal(ladder())

	l := ladder()
	l.Version = 99
	l.At = time.Now()
	l.By = "someone-else@acme"
	l.Reason = "a different sentence"
	b, _ := Seal(l)
	if a.Digest != b.Digest {
		t.Error("re-saving an unchanged ladder minted a new identity, so a no-op edit looks like a policy change")
	}

	l = ladder()
	l.Rungs[0].At = 0.21
	c, _ := Seal(l)
	if a.Digest == c.Digest {
		t.Error("moving a threshold left the identity unchanged")
	}

	l = ladder()
	l.Cost.Miss = 1
	d, _ := Seal(l)
	if a.Digest == d.Digest {
		t.Error("changing the stated cost left the identity unchanged; the cost is part of what the policy decides")
	}

	l = ladder()
	l.Shadow = true
	e, _ := Seal(l)
	if a.Digest == e.Digest {
		t.Error("turning shadow on left the identity unchanged")
	}
}

// An unstated price is not a free mistake. Everything derived from it must be
// absent, and Stated is the one predicate that decides.
func TestUnstatedCostIsNotZeroCost(t *testing.T) {
	if (Cost{}).Stated() {
		t.Error("an empty cost claims to state a price")
	}
	if !(Cost{Alarm: 1}).Stated() {
		t.Error("a stated alarm price reads as unstated")
	}
}
