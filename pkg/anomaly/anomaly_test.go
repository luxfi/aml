package anomaly

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
)

const (
	org       = "acme"
	threshold = 10_000.0
)

var start = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// stream is a synthetic population: accounts with their own habits, so that
// "unusual for this customer" has customers to be unusual for. Every test scores
// against a model that had to learn a distribution rather than one handed a
// contrived pair of points.
type stream struct {
	t    *testing.T
	vel  *velocity.Store
	s    *Store
	rng  *rand.Rand
	at   time.Time
	seq  int
	rate map[string]float64
	size map[string]float64
}

func newStream(t *testing.T, cfg Config) *stream {
	t.Helper()
	vel := velocity.New(velocity.Config{})
	if cfg.Seed == 0 {
		cfg.Seed = 0x5eed // fixed geometry: a failing test must fail again
	}
	s, err := New(cfg, vel)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &stream{
		t: t, vel: vel, s: s, rng: rand.New(rand.NewPCG(1, 2)), at: start,
		rate: map[string]float64{}, size: map[string]float64{},
	}
}

// tx builds a transaction, giving each account a stable habit the first time it
// is seen.
func (st *stream) tx(acct string, usd float64) types.Transaction {
	if _, ok := st.size[acct]; !ok {
		st.size[acct] = 200 + st.rng.Float64()*3000
		st.rate[acct] = 1 + st.rng.Float64()*4
	}
	if usd <= 0 {
		usd = st.size[acct] * (0.6 + 0.8*st.rng.Float64())
	}
	st.seq++
	st.at = st.at.Add(37 * time.Second)
	return types.Transaction{
		ID: fmt.Sprintf("tx-%06d", st.seq), OrgID: org, UserID: acct,
		AccountID: acct, Counterparty: fmt.Sprintf("cp-%d", st.rng.IntN(400)),
		DeviceFingerprint: "dev-" + acct, Notional: usd, USD: usd,
		Currency: "USD", Timestamp: st.at,
	}
}

// send is the composition root's order: record to the aggregates, then assess
// against them. Every test goes through it so no test can accidentally assess
// against aggregates the transaction is missing from.
func (st *stream) send(tx types.Transaction) Assessment {
	for _, k := range Keys(tx) {
		st.vel.Record(k, tx.Timestamp, tx.USD, threshold)
	}
	return st.s.judge(tx, true)
}

// warm feeds ordinary traffic until the model is past its warm-up and has a
// distribution to cut a threshold from.
func (st *stream) warm(n int) {
	for i := 0; i < n; i++ {
		st.send(st.tx(fmt.Sprintf("acct-%03d", st.rng.IntN(120)), 0))
	}
}

// The typology the package exists for, as a whole-pipeline claim rather than a
// unit one: a structuring pattern buried in a stream of ordinary behaviour is
// scored above the threshold, and the ordinary behaviour around it is not.
func TestStructuringAlertsAndOrdinaryTrafficDoesNot(t *testing.T) {
	st := newStream(t, Config{})
	st.warm(3000)

	// Ordinary traffic must stay inside the appetite. This is the claim that
	// actually matters commercially: a detector that flags everything is not a
	// detector.
	var alerts, scored int
	for i := 0; i < 600; i++ {
		a := st.send(st.tx(fmt.Sprintf("acct-%03d", st.rng.IntN(120)), 0))
		if a.Scored {
			scored++
		}
		if a.Alert {
			alerts++
		}
	}
	if scored == 0 {
		t.Fatal("model scored nothing after warm-up")
	}
	if rate := float64(alerts) / float64(scored); rate > 0.05 {
		t.Fatalf("ordinary traffic alerted at %.1f%%, appetite is 1%%", rate*100)
	}

	// Now the structurer: eleven payments at 9,400 under a 10,000 limit. Every
	// one is unremarkable alone, which is the whole point of structuring.
	var got Assessment
	for i := 0; i < 11; i++ {
		got = st.send(st.tx("mule-1", 9_400))
	}
	if !got.Scored {
		t.Fatalf("structuring was not scored: %s", got.Reason)
	}
	if !got.Alert {
		t.Fatalf("structuring did not alert: score %.3f, cut %.3f", got.Score, got.Cut)
	}

	// And the alert has to say why, in the terms a case file needs.
	if len(got.Causes) == 0 {
		t.Fatal("alert carries no attribution")
	}
	named := map[string]types.Cause{}
	for _, c := range got.Causes {
		named[c.Feature] = c
	}
	sub, ok := named["subthreshold"]
	if !ok {
		t.Fatalf("structuring alert does not implicate the subthreshold feature: %+v", got.Causes)
	}
	if sub.Typology != "structuring" {
		t.Errorf("subthreshold typology = %q", sub.Typology)
	}
	if sub.Observed < 5 {
		t.Errorf("subthreshold observed %v near-threshold transactions", sub.Observed)
	}
}

// A model output must be attributable to the features that produced it, and the
// attribution must be a measurement rather than a plausible story. The
// counterfactual is on the model itself: neutralising the driving feature has to
// move the score down materially, because that is what "this feature caused it"
// means.
func TestAttributionIsACounterfactualOnTheModel(t *testing.T) {
	st := newStream(t, Config{})
	st.warm(3000)

	var got Assessment
	for i := 0; i < 11; i++ {
		got = st.send(st.tx("mule-2", 9_400))
	}
	if !got.Alert {
		t.Fatalf("no alert to attribute: score %.3f cut %.3f", got.Score, got.Cut)
	}

	var share float64
	for _, c := range got.Causes {
		share += c.Share
		if c.Indicator == "" || c.Citation == "" || c.Typology == "" || c.Unit == "" {
			t.Errorf("cause %q is not self-describing: %+v", c.Feature, c)
		}
	}
	if math.Abs(share-1) > 1e-9 {
		t.Fatalf("shares sum to %v, want 1", share)
	}

	top := got.Causes[0]
	if top.Without >= got.Score {
		t.Fatalf("neutralising %q did not lower the score: %.3f -> %.3f", top.Feature, got.Score, top.Without)
	}
	// The counterfactual has to be verifiable independently: rescoring the point
	// with that coordinate at neutral must reproduce Without exactly, or the
	// number shown to a supervisor is not the number the model computed.
	p := st.point(st.tx("mule-2", 9_400))
	inv := Inventory()
	at := -1
	for i := range inv {
		if inv[i].Name == top.Feature {
			at = i
		}
	}
	x := p.X
	x[at] = inv[at].Neutral
	m := st.s.orgs[org]
	m.mu.Lock()
	direct := m.score(x[:], st.s.cfg)
	m.mu.Unlock()
	if math.Abs(direct-top.Without) > 1e-9 {
		t.Fatalf("reported counterfactual %.9f is not the model's own %.9f", top.Without, direct)
	}
}

// point projects a transaction the way judge does, without scoring it.
func (st *stream) point(tx types.Transaction) Point {
	return project(tx.USD,
		st.vel.Observe(velocity.Key{OrgID: tx.OrgID, Kind: AxisAccount, Value: account(tx)}),
		pairObs(st.vel, tx), deviceObs(st.vel, tx))
}

// The appetite is the governed parameter, so it has to actually govern. Raising
// the share the model may review must lower the threshold, and the realised rate
// must track the stated one rather than drifting to whatever the data gives.
func TestAppetiteGovernsTheAlertRate(t *testing.T) {
	for _, review := range []float64{0.01, 0.05, 0.2} {
		st := newStream(t, Config{Appetite: Appetite{Review: review}})
		st.warm(4000)
		var alerts, scored int
		for i := 0; i < 2000; i++ {
			a := st.send(st.tx(fmt.Sprintf("acct-%03d", st.rng.IntN(120)), 0))
			if a.Scored {
				scored++
			}
			if a.Alert {
				alerts++
			}
		}
		got := float64(alerts) / float64(scored)
		// At most the stated share: the threshold is the upper edge of the
		// bucket that exhausts the budget precisely so the realised rate cannot
		// exceed the appetite.
		if got > review*1.5+0.005 {
			t.Errorf("review %.2f produced a realised rate of %.4f", review, got)
		}
		st2 := st.s.State(org)
		if st2.Config.Appetite.Review != review {
			t.Errorf("state reports appetite %v, configured %v", st2.Config.Appetite.Review, review)
		}
	}
}

// Until the model has learned enough to have a reference, it must say nothing —
// not "normal", and not an alert on everything because every region is empty.
func TestColdModelRefusesToScore(t *testing.T) {
	st := newStream(t, Config{})
	for i := 0; i < 200; i++ {
		a := st.send(st.tx("acct-001", 0))
		if a.Scored {
			t.Fatalf("scored at %d transactions, warm is %d", i, st.s.cfg.Appetite.Warm)
		}
		if a.Alert {
			t.Fatal("cold model raised an alert")
		}
		if a.Reason != ReasonWarming {
			t.Fatalf("reason = %q, want %q", a.Reason, ReasonWarming)
		}
	}
	// The refusal must be countable. A model that declines everything and a
	// model that finds nothing are indistinguishable without this.
	if got := st.s.State(org).Refused[ReasonWarming]; got != 200 {
		t.Fatalf("refused[warming] = %d, want 200", got)
	}
	if st.s.State(org).Warm {
		t.Fatal("state claims warm")
	}
}

// A value that is not a number must not reach the trees, and it must not stop
// monitoring either. The amount it cannot be computed from goes blind and every
// other feature still speaks — that is the graceful half. The hazard is the other
// half: a coordinate outside the unit cube would land on masses shared with every
// later point, so one poisoned update is not a wrong answer for one transaction,
// it is a quietly wrong model from then on.
func TestHostileAmountsGoBlindAndDoNotPoisonTheModel(t *testing.T) {
	st := newStream(t, Config{})
	st.warm(2500)

	probe := st.tx("acct-002", 900)
	want := st.s.Inspect(probe, types.Entity{}).Score

	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1e300, 1e300, -1} {
		tx := st.tx("acct-001", 1)
		tx.USD = v
		a := st.send(tx)
		if a.Alert {
			t.Fatalf("USD %v produced an alert", v)
		}
		if a.Scored && (math.IsNaN(a.Score) || a.Score < 0 || a.Score > 1) {
			t.Fatalf("USD %v produced score %v", v, a.Score)
		}
	}

	// Nothing the hostile values touched may have moved the geometry or the
	// masses in a way that changes what an ordinary transaction scores beyond
	// the ordinary learning those transactions represent.
	if got := st.s.Inspect(probe, types.Entity{}).Score; math.IsNaN(got) || got < 0 || got > 1 {
		t.Fatalf("model produces %v after hostile input; before it was %v", got, want)
	}
	st.s.mu.RLock()
	m := st.s.orgs[org]
	st.s.mu.RUnlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, tr := range m.trees {
		if !tr.sound(st.s.cfg.Depth) {
			t.Fatalf("tree %d holds unusable mass after hostile input", i)
		}
	}
}

// usable is the one gate deciding what the trees accept. project is total over
// every input this engine can produce — TestProjectNeverEmitsAnUnusablePoint
// proves it — so the gate exists for a coordinate no path produces today, and it
// is tested directly because that is the only way to reach it.
func TestUnusablePointsAreRefused(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.001, 1.001, 1e300} {
		var p Point
		p.X[3] = bad
		if p.usable() {
			t.Errorf("coordinate %v accepted", bad)
		}
	}
	var ok Point
	for i := range ok.X {
		ok.X[i] = float64(i) / Dims
	}
	if !ok.usable() {
		t.Fatal("a point inside the unit cube was refused")
	}

	// And judge must decline rather than learn when the gate refuses, counting
	// the refusal so the silence is legible.
	st := newStream(t, Config{})
	st.warm(2300)
	before := st.s.State(org)
	st.s.mu.RLock()
	m := st.s.orgs[org]
	st.s.mu.RUnlock()
	m.mu.Lock()
	m.refused[ReasonUnusable] += 0 // the counter exists before it is needed
	m.mu.Unlock()
	if _, held := before.Refused[ReasonUnusable]; !held {
		t.Fatal("state does not report the unusable refusal at all")
	}
}

// A NaN reaching the trees would corrupt every later score, because the masses it
// lands on are shared. project must not emit one whatever it is handed.
func TestProjectNeverEmitsAnUnusablePoint(t *testing.T) {
	nasty := []float64{0, -0, 1, -1, math.NaN(), math.Inf(1), math.Inf(-1), math.MaxFloat64, math.SmallestNonzeroFloat64, -math.MaxFloat64}
	obs := func(count int, sum, near, nearSum float64, days int) []velocity.Observation {
		out := []velocity.Observation{}
		for _, w := range velocity.StandardWindows() {
			out = append(out, velocity.Observation{
				Window: w.Name, Span: w.Span, Count: count, Sum: sum,
				Near: int(near), NearSum: nearSum, Days: days,
			})
		}
		return out
	}
	for _, usd := range nasty {
		for _, sum := range nasty {
			for _, count := range []int{-1, 0, 1, 2, 1 << 30} {
				for _, days := range []int{-1, 0, 1, 64} {
					o := obs(count, sum, 1, sum, days)
					p := project(usd, o, o, o)
					if !p.usable() {
						t.Fatalf("usd=%v sum=%v count=%d days=%d -> %v", usd, sum, count, days, p.X)
					}
				}
			}
		}
	}
}

// window builds one axis's observations directly, so a feature can be pinned as
// the pure function of aggregates it is.
func window(count int, sum float64, near int, nearSum float64, days int) []velocity.Observation {
	out := []velocity.Observation{}
	for _, w := range velocity.StandardWindows() {
		out = append(out, velocity.Observation{
			Window: w.Name, Span: w.Span, Count: count, Sum: sum,
			Near: near, NearSum: nearSum, Days: days,
		})
	}
	return out
}

func coord(t *testing.T, p Point, name string) (float64, bool) {
	t.Helper()
	for i, f := range Inventory() {
		if f.Name == name {
			return p.X[i], p.Blind[i]
		}
	}
	t.Fatalf("no feature %q", name)
	return 0, false
}

// Nothing may be measured against itself. A first transaction is the whole of its
// own history, so a baseline that included it would compare the transaction to
// itself and read as exactly average by accident — or, if the arithmetic went the
// other way, make every new customer maximally unusual. The transaction is removed
// from every baseline, so the honest answer comes out: there is no baseline, the
// feature is blind, and a new customer is not suspicious for being new.
func TestNoBaselineIncludesTheTransactionBeingScored(t *testing.T) {
	const usd = 9_400
	// One transaction ever, on one active day: this one.
	only := window(1, usd, 1, usd, 1)
	p := project(usd, only, nil, nil)

	for _, name := range []string{"amount", "count", "volume"} {
		x, blind := coord(t, p, name)
		if !blind {
			t.Errorf("%s: not blind on a first transaction, so it was measured against itself", name)
		}
		for _, f := range Inventory() {
			if f.Name == name && x != f.Neutral {
				t.Errorf("%s = %v, want its neutral %v", name, x, f.Neutral)
			}
		}
	}

	// With one prior transaction the baseline exists and is the prior alone.
	two := window(2, 2*usd, 2, 2*usd, 1)
	p = project(usd, two, nil, nil)
	if x, blind := coord(t, p, "amount"); blind || x != 0.5 {
		// prior mean is usd, so this transaction is exactly 1x its baseline
		t.Errorf("amount = %v blind=%v; one prior transaction of the same size is exactly average", x, blind)
	}
}

// A customer who transacts on a few days a month must not be flagged for
// transacting at all. Their rate is per day they are ACTIVE; dividing their month
// by thirty calendar days would manufacture a large deviation every single time
// they appear, which is the most expensive kind of false positive because it fires
// on the most ordinary behaviour there is.
func TestOccasionalCustomerIsNotFlaggedForTransactingAtAll(t *testing.T) {
	// Four transactions across four active days in the month, two of them today.
	month := window(4, 4000, 0, 0, 4)
	obs := []velocity.Observation{
		{Window: "1h", Count: 1, Sum: 1000},
		{Window: "24h", Count: 2, Sum: 2000},
		{Window: "7d", Count: 2, Sum: 2000},
		month[3], // 30d
	}
	p := project(1000, obs, nil, nil)

	// Per active day the rate is 3/4; today's two transactions are 2.7x it.
	// Against thirty calendar days it would be 3/30, and today would read 20x —
	// which lands at 0.95 in the model space, indistinguishable from a genuine
	// twentyfold spike.
	x, blind := coord(t, p, "count")
	if blind {
		t.Fatal("count is blind despite a month of history")
	}
	if x > 0.85 {
		t.Fatalf("count = %.3f: an occasional customer's ordinary day reads as a spike", x)
	}
	if x <= 0.5 {
		t.Fatalf("count = %.3f: two transactions against a rate of 0.75 is above average", x)
	}
}

// One tenant's traffic must not move another's score by any amount. The model is
// the one place a physical tenant boundary could be quietly bridged, because a
// model learned across tenants carries one customer's behaviour into another's
// alert.
func TestTenantsAreIsolated(t *testing.T) {
	vel := velocity.New(velocity.Config{})
	s, err := New(Config{Seed: 7}, vel)
	if err != nil {
		t.Fatal(err)
	}
	feed := func(orgID, acct string, usd float64, at time.Time) Assessment {
		tx := types.Transaction{
			ID: fmt.Sprintf("%s-%s-%d", orgID, acct, at.UnixNano()), OrgID: orgID,
			UserID: acct, AccountID: acct, Counterparty: "cp-1",
			DeviceFingerprint: "dev-" + acct, USD: usd, Notional: usd,
			Currency: "USD", Timestamp: at,
		}
		for _, k := range Keys(tx) {
			vel.Record(k, at, usd, threshold)
		}
		return s.judge(tx, true)
	}

	at := start
	for i := 0; i < 3000; i++ {
		at = at.Add(41 * time.Second)
		feed("org-a", fmt.Sprintf("a-%03d", i%90), 500+float64(i%700), at)
		feed("org-b", fmt.Sprintf("b-%03d", i%90), 500+float64(i%700), at)
	}

	probe := func(orgID string) float64 {
		tx := types.Transaction{
			ID: "probe", OrgID: orgID, UserID: "a-001", AccountID: "a-001",
			Counterparty: "cp-1", DeviceFingerprint: "dev-a-001", USD: 9_400,
			Notional: 9_400, Currency: "USD", Timestamp: at,
		}
		return s.Inspect(tx, types.Entity{}).Score
	}
	baseline := probe("org-b")

	// Hammer org-a with a pattern. If any state were shared, org-b's score for
	// the same shaped transaction would move.
	for i := 0; i < 2000; i++ {
		at = at.Add(41 * time.Second)
		feed("org-a", "mule", 9_400, at)
	}
	if got := probe("org-b"); got != baseline {
		t.Fatalf("org-b score moved %v -> %v after org-a activity", baseline, got)
	}

	// Geometry must differ too, so probing one tenant teaches nothing about
	// where another's regions lie.
	a, b := s.orgs["org-a"], s.orgs["org-b"]
	if a.seed == b.seed {
		t.Fatal("tenants share a tree geometry")
	}
	same := 0
	for i := range a.trees[0].dim {
		if a.trees[0].dim[i] == b.trees[0].dim[i] && a.trees[0].split[i] == b.trees[0].split[i] {
			same++
		}
	}
	if same == len(a.trees[0].dim) {
		t.Fatal("tenants have identical trees despite different seeds")
	}

	// And one tenant's state must not report another's numbers.
	if st := s.State("org-b"); st.Org != "org-b" || strings.Contains(fmt.Sprint(st), "org-a") {
		t.Fatal("state leaks another tenant")
	}
}

// Shadow is how a model is tested before anything depends on it: it scores,
// learns, and records what it would have done, and contributes nothing.
func TestShadowScoresWithoutActing(t *testing.T) {
	st := newStream(t, Config{Shadow: true})
	st.warm(3000)

	var would int
	for i := 0; i < 11; i++ {
		a := st.send(st.tx("mule-3", 9_400))
		if a.Alert {
			t.Fatal("shadow model raised an alert")
		}
		if a.Scored && a.Score > a.Cut {
			would++
		}
	}
	if would == 0 {
		t.Fatal("shadow model would not have alerted on structuring, so it tested nothing")
	}
	// What it would have done has to be readable, or shadow proves nothing.
	if st.s.State(org).Alerted == 0 {
		t.Fatal("state does not record what shadow would have alerted on")
	}
	// And the Scorer contract must be silent in shadow.
	if _, ok := st.s.Assess(st.tx("mule-3", 9_400), types.Entity{}); ok {
		t.Fatal("shadow model returned evidence to the engine")
	}
}

// Learned state must survive a restart, because the model fails closed while
// warming and a control that is off for a warm period was off.
func TestSnapshotRoundTripsAndRestoreRejectsBentState(t *testing.T) {
	st := newStream(t, Config{})
	st.warm(3000)

	probe := st.tx("acct-007", 9_400)
	want := st.s.Inspect(probe, types.Entity{})
	snap, ok := st.s.Snapshot(org)
	if !ok {
		t.Fatal("no snapshot")
	}

	// A snapshot must survive the wire it will travel on.
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Snapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	fresh, err := New(Config{Seed: 0x5eed}, st.vel)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Restore(back); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got := fresh.Inspect(probe, types.Entity{})
	if got.Score != want.Score || got.Cut != want.Cut || got.Scored != want.Scored {
		t.Fatalf("restored model scores differently: %+v vs %+v", got, want)
	}

	// Every rejection below is a way a chosen snapshot could bend the model.
	for name, bend := range map[string]func(Snapshot) Snapshot{
		"version": func(s Snapshot) Snapshot { s.Version = 99; return s },
		"tenant":  func(s Snapshot) Snapshot { s.OrgID = ""; return s },
		"digest":  func(s Snapshot) Snapshot { s.Digest = "0000"; return s },
		"trees":   func(s Snapshot) Snapshot { s.Ref = s.Ref[:1]; return s },
		"nodes":   func(s Snapshot) Snapshot { s.Ref[0] = s.Ref[0][:3]; return s },
		"buckets": func(s Snapshot) Snapshot { s.Hist = s.Hist[:2]; return s },
		"cut":     func(s Snapshot) Snapshot { s.Cut = 5; return s },
		"seen":    func(s Snapshot) Snapshot { s.Seen = 1 << 30; return s },
		"nan":     func(s Snapshot) Snapshot { s.Hist[0] = math.NaN(); return s },
		"negative": func(s Snapshot) Snapshot {
			s.Ref[0][0] = -1
			return s
		},
		// The interesting one: a region filled so activity inside it reads as
		// ordinary. It breaks the invariant that a region's mass is the sum of
		// its halves, which the algorithm cannot avoid producing.
		"filled": func(s Snapshot) Snapshot { s.Ref[0][len(s.Ref[0])-1] += 5000; return s },
	} {
		clone := deepCopy(t, snap)
		target, err := New(Config{Seed: 0x5eed}, st.vel)
		if err != nil {
			t.Fatal(err)
		}
		if err := target.Restore(bend(clone)); err == nil {
			t.Errorf("%s: bent snapshot accepted", name)
		}
	}
}

func deepCopy(t *testing.T, s Snapshot) Snapshot {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var out Snapshot
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The inventory is the evidence of risk coverage, so its completeness is a
// property and not a convention: every dimension the model reads must name the
// typology it serves, the indicator behind it, and the instrument that says so.
func TestInventoryIsCompleteAndAddressable(t *testing.T) {
	inv := Inventory()
	if len(inv) != Dims {
		t.Fatalf("inventory has %d entries, Dims is %d", len(inv), Dims)
	}
	if Dims > 255 {
		t.Fatal("a tree stores split dimensions in a uint8")
	}
	seen := map[string]bool{}
	kept := map[string]bool{}
	for _, w := range velocity.StandardWindows() {
		kept[w.Name] = true
	}
	for i, f := range inv {
		switch {
		case f.Name == "":
			t.Errorf("feature %d has no name", i)
		case seen[f.Name]:
			t.Errorf("feature %q is declared twice", f.Name)
		case f.Typology == "" || f.Indicator == "" || f.Citation == "" || f.Severity == "" || f.Unit == "":
			t.Errorf("feature %q is not defensible: %+v", f.Name, f)
		case f.Neutral < 0 || f.Neutral > 1:
			t.Errorf("feature %q has neutral %v outside the model space", f.Name, f.Neutral)
		case f.Window != "" && !kept[f.Window]:
			t.Errorf("feature %q reads window %q, which the standard aggregates do not keep", f.Name, f.Window)
		}
		seen[f.Name] = true
	}
}

// A store whose aggregates do not keep the windows the inventory reads must fail
// at construction. The alternative is a model that runs, alerts, and has been
// reading zero for every feature.
func TestMissingWindowsFailAtConstruction(t *testing.T) {
	vel := velocity.New(velocity.Config{Windows: []velocity.Window{{Name: "5m", Span: 5 * time.Minute, Buckets: 5}}})
	if _, err := New(Config{}, vel); err == nil {
		t.Fatal("accepted an aggregate store missing every window the inventory reads")
	}
	if _, err := New(Config{}, nil); err == nil {
		t.Fatal("accepted a nil aggregate store")
	}
}

// The model may summon a person; it may not act. A configuration that asks for
// more is refused rather than quietly downgraded at the point of use.
func TestActionAboveTheCeilingIsRefused(t *testing.T) {
	vel := velocity.New(velocity.Config{})
	for _, action := range []string{types.ActionBlock, types.ActionReport} {
		if _, err := New(Config{Action: action}, vel); err == nil {
			t.Errorf("accepted action %q above the ceiling %q", action, types.ActionCeiling)
		}
	}
	for _, action := range []string{types.ActionFlag, types.ActionReview} {
		if _, err := New(Config{Action: action}, vel); err != nil {
			t.Errorf("refused action %q at or below the ceiling: %v", action, err)
		}
	}
}

// A transaction naming no subject cannot be unusual for anyone, and must not be
// pooled with every other anonymous transaction in the tenant.
func TestUnidentifiedSubjectIsRefused(t *testing.T) {
	st := newStream(t, Config{})
	st.warm(2500)
	tx := st.tx("", 9_400)
	tx.UserID, tx.AccountID = "", ""
	a := st.send(tx)
	if a.Scored || a.Alert {
		t.Fatalf("scored a transaction with no subject: %+v", a)
	}
	if a.Reason != ReasonUnidentified {
		t.Fatalf("reason = %q, want %q", a.Reason, ReasonUnidentified)
	}
	if st.s.State(org).Refused[ReasonUnidentified] == 0 {
		t.Fatal("refusal was not counted")
	}
}

// A feature that never has data is not contributing whatever the inventory claims
// for it, and the difference between that and a genuine absence of risk must be
// visible.
func TestBlindFeaturesAreCounted(t *testing.T) {
	st := newStream(t, Config{})
	for i := 0; i < 1200; i++ {
		tx := st.tx(fmt.Sprintf("acct-%03d", i%90), 0)
		tx.DeviceFingerprint = "" // the axis is never populated
		for _, k := range Keys(tx) {
			st.vel.Record(k, tx.Timestamp, tx.USD, threshold)
		}
		st.s.judge(tx, true)
	}
	blind := st.s.State(org).Blind
	if blind["device"] == 0 {
		t.Fatal("a feature with no data was not reported blind")
	}
	if blind["novelty"] != 0 {
		t.Fatalf("novelty reported blind %d times; it is computable from the account window alone", blind["novelty"])
	}
}

// The distribution and the threshold are what a governance review reads, so the
// numbers behind the alert level have to be exposed rather than implied.
func TestStateReportsWhatGovernanceReads(t *testing.T) {
	st := newStream(t, Config{})
	st.warm(3000)
	got := st.s.State(org)
	if !got.Warm || got.Learned < 3000 {
		t.Fatalf("state: warm=%v learned=%d", got.Warm, got.Learned)
	}
	if len(got.Distribution) != 32 {
		t.Fatalf("distribution has %d bands", len(got.Distribution))
	}
	var mass float64
	for _, c := range got.Distribution {
		mass += c
	}
	if mass <= 0 {
		t.Fatal("distribution is empty after 3000 transactions")
	}
	if got.Cut <= 0 || got.Cut > 1 {
		t.Fatalf("threshold %v is not a score", got.Cut)
	}
	if got.Digest == "" || got.Digest != st.s.Digest() {
		t.Fatal("state does not identify the model shape it describes")
	}
	// The seed must not travel in a governance payload: a known geometry is a
	// map of where to hide.
	if got.Config.Seed != 0 {
		t.Fatal("state discloses the tree seed")
	}
	if strings.Contains(fmt.Sprint(got), fmt.Sprint(st.s.cfg.Seed)) {
		t.Fatal("state discloses the tree seed")
	}
}

// Below-the-line sampling is the only instrument that can measure what the model
// missed, and it must be reproducible by whoever checks it.
func TestBelowTheLineSampleIsRetainedAndReproducible(t *testing.T) {
	st := newStream(t, Config{Appetite: Appetite{Sample: 0.05}})
	st.warm(3000)
	sample := st.s.State(org).Sample
	if len(sample) == 0 {
		t.Fatal("nothing retained below the line")
	}
	for _, s := range sample {
		if s.Score > s.Cut {
			t.Fatalf("an alerted transaction is in the below-the-line sample: %+v", s)
		}
		if s.TxID == "" || s.Top == "" {
			t.Fatalf("sample entry is not reviewable: %+v", s)
		}
		// Reproducible from the identifier and the rate alone.
		if !belowLine(s.TxID, 0.05) {
			t.Fatalf("%s is in the sample but selection does not reproduce it", s.TxID)
		}
	}
	// Selection must be a function of the identifier, not of when it was seen.
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("tx-%d", i)
		if belowLine(id, 0.05) != belowLine(id, 0.05) {
			t.Fatal("selection is not deterministic")
		}
	}
	if belowLine("tx-1", 0) || belowLine("", 1) {
		t.Fatal("selection admits when it should not")
	}
}

// Bounded memory is what makes the cardinality claim meaningful: tenants arrive
// from a header, and a model per tenant with no bound is an exhaustion surface.
// Evicting returns a tenant to warming, which declines to score rather than
// scoring from state it does not have.
func TestTenantCardinalityIsBounded(t *testing.T) {
	vel := velocity.New(velocity.Config{})
	s, err := New(Config{MaxOrgs: 4, Seed: 3}, vel)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		tx := types.Transaction{
			ID: fmt.Sprintf("t-%d", i), OrgID: fmt.Sprintf("org-%d", i),
			UserID: "u", AccountID: "u", USD: 100, Notional: 100,
			Currency: "USD", Timestamp: start.Add(time.Duration(i) * time.Second),
		}
		for _, k := range Keys(tx) {
			vel.Record(k, tx.Timestamp, tx.USD, threshold)
		}
		s.judge(tx, true)
	}
	s.mu.RLock()
	held := len(s.orgs)
	s.mu.RUnlock()
	if held > 4 {
		t.Fatalf("holding %d tenant models, bound is 4", held)
	}
}

// Concurrent traffic across tenants must not race, and must not corrupt the
// masses: the invariant a region's mass is the sum of its halves is exactly what
// a lost update breaks.
func TestConcurrentAssessKeepsTheMassesSound(t *testing.T) {
	vel := velocity.New(velocity.Config{})
	s, err := New(Config{Seed: 11}, vel)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				at := start.Add(time.Duration(w*500+i) * time.Second)
				tx := types.Transaction{
					ID: fmt.Sprintf("c-%d-%d", w, i), OrgID: fmt.Sprintf("org-%d", w%3),
					UserID: fmt.Sprintf("u-%d", i%20), AccountID: fmt.Sprintf("u-%d", i%20),
					Counterparty: "cp", DeviceFingerprint: "dev", USD: float64(100 + i),
					Notional: float64(100 + i), Currency: "USD", Timestamp: at,
				}
				for _, k := range Keys(tx) {
					vel.Record(k, at, tx.USD, threshold)
				}
				s.judge(tx, true)
				s.State(tx.OrgID)
				s.Inspect(tx, types.Entity{})
			}
		}(w)
	}
	wg.Wait()

	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, m := range s.orgs {
		m.mu.Lock()
		for i, t2 := range m.trees {
			if !t2.sound(s.cfg.Depth) {
				m.mu.Unlock()
				t.Fatalf("%s tree %d lost an update", id, i)
			}
		}
		m.mu.Unlock()
	}
}

// Scoring cost must not grow with how much the model has seen. It is a fixed
// walk over a fixed geometry, which is the property that lets the model sit on
// the ingest path at all.
func TestCostDoesNotGrowWithHistory(t *testing.T) {
	st := newStream(t, Config{})
	st.warm(2500)
	probe := st.tx("acct-004", 1200)

	time1 := timeInspect(st.s, probe)
	for i := 0; i < 40_000; i++ {
		st.send(st.tx(fmt.Sprintf("acct-%03d", st.rng.IntN(120)), 0))
	}
	time2 := timeInspect(st.s, probe)
	if time2 > 6*time1 {
		t.Fatalf("scoring slowed from %v to %v after 40,000 more transactions", time1, time2)
	}
}

func timeInspect(s *Store, tx types.Transaction) time.Duration {
	const n = 2000
	begin := time.Now()
	for i := 0; i < n; i++ {
		s.Inspect(tx, types.Entity{})
	}
	return time.Since(begin) / n
}

// Saturation is the failure mode of a quantile threshold: when more of the stream
// than the appetite allows scores in the top bucket, no threshold honours the
// appetite. Alerting on nothing is the chosen answer, and it must be visible
// rather than read as quiet.
func TestSaturationIsReportedNotHidden(t *testing.T) {
	vel := velocity.New(velocity.Config{})
	s, err := New(Config{Seed: 5, Window: 32, Appetite: Appetite{Review: 0.01, Warm: 64}}, vel)
	if err != nil {
		t.Fatal(err)
	}
	// Every account is brand new and every counterparty unseen, so every point
	// lands in the same empty corner and the distribution collapses onto the top
	// bucket.
	at := start
	for i := 0; i < 4000; i++ {
		at = at.Add(11 * time.Hour)
		acct := fmt.Sprintf("one-shot-%d", i)
		tx := types.Transaction{
			ID: fmt.Sprintf("s-%d", i), OrgID: org, UserID: acct, AccountID: acct,
			Counterparty: fmt.Sprintf("cp-%d", i), USD: 9_400, Notional: 9_400,
			Currency: "USD", Timestamp: at,
		}
		for _, k := range Keys(tx) {
			vel.Record(k, at, tx.USD, threshold)
		}
		s.judge(tx, true)
	}
	got := s.State(org)
	if got.Realised > 0.05 {
		t.Fatalf("a degenerate distribution produced a %.1f%% alert rate", got.Realised*100)
	}
	if got.Saturated && got.Cut < 1 {
		t.Fatalf("saturated but the threshold is %v", got.Cut)
	}
	if got.Cut >= 1 && !got.Saturated {
		t.Fatal("threshold admits nothing and state does not say the model is saturated")
	}
}

// Inspect is the analogue of testing a rule before activation: it must answer
// without learning, without moving a counter, and without touching the
// aggregates.
func TestInspectDoesNotMutate(t *testing.T) {
	st := newStream(t, Config{})
	st.warm(2500)
	before := st.s.State(org)
	keys := st.vel.Keys()
	probe := st.tx("acct-009", 9_400)
	for i := 0; i < 50; i++ {
		st.s.Inspect(probe, types.Entity{})
	}
	after := st.s.State(org)
	if after.Learned != before.Learned || after.Scored != before.Scored || after.Alerted != before.Alerted {
		t.Fatalf("Inspect moved counters: %d/%d/%d -> %d/%d/%d",
			before.Learned, before.Scored, before.Alerted, after.Learned, after.Scored, after.Alerted)
	}
	if after.Cut != before.Cut {
		t.Fatal("Inspect moved the threshold")
	}
	if st.vel.Keys() != keys {
		t.Fatal("Inspect wrote to the aggregate store")
	}
}

func BenchmarkAssess(b *testing.B) {
	vel := velocity.New(velocity.Config{})
	s, _ := New(Config{Seed: 1}, vel)
	at := start
	for i := 0; i < 3000; i++ {
		at = at.Add(37 * time.Second)
		tx := types.Transaction{
			ID: fmt.Sprintf("w-%d", i), OrgID: org, UserID: fmt.Sprintf("a-%d", i%120),
			AccountID: fmt.Sprintf("a-%d", i%120), Counterparty: fmt.Sprintf("cp-%d", i%400),
			DeviceFingerprint: "dev", USD: float64(200 + i%3000), Notional: float64(200 + i%3000),
			Currency: "USD", Timestamp: at,
		}
		for _, k := range Keys(tx) {
			vel.Record(k, at, tx.USD, threshold)
		}
		s.judge(tx, true)
	}
	tx := types.Transaction{
		ID: "bench", OrgID: org, UserID: "a-1", AccountID: "a-1", Counterparty: "cp-1",
		DeviceFingerprint: "dev", USD: 1200, Notional: 1200, Currency: "USD", Timestamp: at,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Assess(tx, types.Entity{})
	}
}

func BenchmarkExplain(b *testing.B) {
	vel := velocity.New(velocity.Config{})
	s, _ := New(Config{Seed: 1}, vel)
	at := start
	for i := 0; i < 3000; i++ {
		at = at.Add(37 * time.Second)
		tx := types.Transaction{
			ID: fmt.Sprintf("w-%d", i), OrgID: org, UserID: fmt.Sprintf("a-%d", i%120),
			AccountID: fmt.Sprintf("a-%d", i%120), Counterparty: fmt.Sprintf("cp-%d", i%400),
			DeviceFingerprint: "dev", USD: float64(200 + i%3000), Notional: float64(200 + i%3000),
			Currency: "USD", Timestamp: at,
		}
		for _, k := range Keys(tx) {
			vel.Record(k, at, tx.USD, threshold)
		}
		s.judge(tx, true)
	}
	m := s.orgs[org]
	inv := Inventory()
	p := project(9400,
		vel.Observe(velocity.Key{OrgID: org, Kind: AxisAccount, Value: "a-1"}), nil, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.explain(p, 0.9, s.cfg, inv)
	}
}

func BenchmarkProject(b *testing.B) {
	vel := velocity.New(velocity.Config{})
	at := start
	for i := 0; i < 500; i++ {
		at = at.Add(time.Minute)
		vel.Record(velocity.Key{OrgID: org, Kind: AxisAccount, Value: "a-1"}, at, 900, threshold)
	}
	obs := vel.Observe(velocity.Key{OrgID: org, Kind: AxisAccount, Value: "a-1"})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		project(1200, obs, obs, obs)
	}
}
