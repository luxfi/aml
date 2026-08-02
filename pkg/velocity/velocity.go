// Package velocity answers, in constant time per transaction, what an entity has
// done across a set of sliding time windows.
//
// WHY THIS EXISTS. A rule engine that sees one transaction at a time cannot detect
// the typology regulators care most about. Structuring — splitting a payment into
// several below a reporting threshold — is invisible to any point-in-time test,
// because every individual transaction is unremarkable by construction. The EBA
// names it directly: the unusual-transaction test includes transactions "split to
// circumvent reporting limits". Detecting it requires an aggregate over a window,
// and detecting it in real time requires that aggregate to be cheap.
//
// WHY NOT A DATABASE QUERY. The obvious implementation is a GROUP BY over history
// per rule per transaction. That is O(history) per evaluation and multiplies by the
// number of windows and rules, so throughput collapses exactly when volume makes
// monitoring matter. This package holds the aggregate incrementally instead: every
// answer is a few array reads.
//
// THE STRUCTURE. Each window is a ring of time buckets. A bucket accumulates
// counters; the ring rotates as time advances, zeroing buckets it passes. Reading a
// window sums its buckets — a fixed, small number of adds independent of how many
// transactions arrived. Memory per key is therefore FIXED, not proportional to
// volume, which is what makes the cardinality bound below meaningful.
//
// The cost is quantisation: a window of W with B buckets resolves to W/B. A 24h
// window with 96 buckets is exact to 15 minutes. That error is stated, bounded and
// configurable, which is the right trade for a control whose thresholds are
// themselves approximations of a regulatory limit. Precision that outruns the
// meaning of the number it measures is not precision.
//
// EXPLAINABILITY IS NOT OPTIONAL HERE. A supervisor asks why an alert fired, so
// every observation carries the numbers it was computed from — count, sum, the
// near-threshold tally and the day spread — rather than a bare verdict. See
// Observation.
package velocity

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/luxfi/aml/pkg/roster"
)

// Window is one sliding aggregate: a duration and the resolution it is kept at.
type Window struct {
	// Name is how a rule refers to this window ("24h", "7d").
	Name string
	// Span is the width of the window.
	Span time.Duration
	// Buckets is the number of ring slots. Quantisation error is Span/Buckets.
	// More buckets cost memory per key and add-time per read, both linearly.
	Buckets int
}

// StandardWindows are the spans AML rules actually reference, at resolutions that
// keep each one's error well inside the shortest interval a rule would compare
// against: 15 minutes on a day, an hour on a week, six hours on a month.
func StandardWindows() []Window {
	return []Window{
		{Name: "1h", Span: time.Hour, Buckets: 60},
		{Name: "24h", Span: 24 * time.Hour, Buckets: 96},
		{Name: "7d", Span: 7 * 24 * time.Hour, Buckets: 168},
		{Name: "30d", Span: 30 * 24 * time.Hour, Buckets: 120},
	}
}

// Config parameterises a Store.
//
// NearThresholdBand is the fraction below a reporting threshold that counts as
// "just under" it. A transaction at 9,400 against a 10,000 threshold is the
// structuring signal; one at 400 is not. 0.10 means the top 10% below the
// threshold, so [9,000, 10,000) — a deliberate default, not a tuned one, and rules
// may override per threshold.
//
// # The bound is in bytes, and it is per tenant
//
// PerOrg is the most one institution's aggregates may occupy, in BYTES, and Orgs
// is how many institutions this process keeps aggregates for. Their product is
// the ceiling on the whole store, and it is a figure an operator can hold against
// a pod's memory limit — which is the entire point, because the bound this
// replaced could not be.
//
// It was a count: a hundred thousand keys. A key is not a pointer to something
// the caller sent; it is one ring per window, allocated whole on the first
// transaction, and the four standard windows come to 444 buckets — a MEASURED
// 22.7 KB. So the published bound implied 2,163 MiB of process memory against a
// 1 GiB limit, which means the honest reading of "MaxKeys = 100,000" was "this
// process dies at about 46,000 keys". A bound over a count of caller-sized
// things is not a bound; see [keyCost], which derives the count from the bytes
// so that the two can never disagree again.
//
// It was also shared. One map held every tenant's keys under one cap, so the
// cap was spent out of a pool and a busy institution's traffic evicted a quiet
// one's aggregates — silently, and an aggregate that reads zero is what an
// account with no activity also reads. Now each tenant has its own keyspace with
// its own byte bound: a tenant may only ever drop its OWN least recently used
// key, the drop is counted and graded ([Store.Load]), and a tenant arriving when
// the roster is full is refused and counted rather than admitted at another
// institution's expense (see pkg/roster).
//
// MaxLateness is how far behind the ring's leading edge a transaction may arrive
// and still be counted. Real feeds deliver out of order; a batch file can be hours
// late. Events later than this are counted in the current bucket rather than
// dropped, and reported via Store.Late() — losing an event from a compliance
// aggregate is worse than misplacing it in time, and silently doing either is worse
// still.
type Config struct {
	Windows           []Window
	NearThresholdBand float64
	// PerOrg is one tenant's ceiling, in bytes.
	PerOrg int64
	// Orgs is how many tenants this process holds aggregates for.
	Orgs        int
	MaxLateness time.Duration
}

// The defaults, and the arithmetic behind them.
//
// DefaultPerOrg times [roster.Default] is the ceiling on the process. At the
// standard windows that is 32 MiB per institution — about 1,400 live keys — and
// 1 GiB across thirty-two of them, which is more than a single-replica pod has.
// A deployment of this engine serves ONE financial institution, so the tenant
// count in practice is one or a small handful and the real ceiling is the
// per-tenant figure; a deployment that genuinely holds many raises one of the
// two and states the product.
//
// An institution whose live key count exceeds its bound drops its own least
// recently used keys, which is a real loss of a real aggregate. That is why it
// is graded and counted rather than absorbed: the answer is to raise PerOrg
// against a bigger pod, and an operator can only know to do that if the store
// says so.
const DefaultPerOrg = 32 << 20

func (c Config) withDefaults() Config {
	if len(c.Windows) == 0 {
		c.Windows = StandardWindows()
	}
	if c.NearThresholdBand <= 0 || c.NearThresholdBand >= 1 {
		c.NearThresholdBand = 0.10
	}
	if c.PerOrg <= 0 {
		c.PerOrg = DefaultPerOrg
	}
	if c.MaxLateness <= 0 {
		c.MaxLateness = time.Hour
	}
	return c
}

// bucket is one ring slot. Counters only — no event list, which is what keeps
// memory per key fixed.
type bucket struct {
	// index is which bucket-of-time this slot currently holds. A slot whose index
	// does not match the one being addressed is stale and reads as zero, so the
	// ring self-clears without a sweep.
	index int64
	count int
	sum   float64
	// near counts transactions falling just below a reporting threshold.
	near int
	// nearSum is their total, which is the number an investigator wants: six
	// transactions at 9,400 is 56,400 moved under a 10,000 limit.
	nearSum float64
	// days holds the distinct calendar days seen, as a bitmask over day-of-window.
	// Structuring spread across days is the classic pattern and a count alone
	// cannot distinguish it from one busy afternoon.
	days uint64
}

// ring is one window's worth of buckets for one key.
type ring struct {
	win     Window
	buckets []bucket
	// lead is the highest bucket index observed, i.e. the leading edge of time.
	lead int64
}

func newRing(w Window) *ring {
	return &ring{win: w, buckets: make([]bucket, w.Buckets)}
}

func (r *ring) bucketIndex(ts time.Time) int64 {
	step := int64(r.win.Span) / int64(r.win.Buckets)
	if step <= 0 {
		step = 1
	}
	return ts.UnixNano() / step
}

// add records one transaction. Returns true if the event was late enough to be
// placed at the leading edge rather than its own bucket.
func (r *ring) add(ts time.Time, amount float64, near bool, day int) bool {
	idx := r.bucketIndex(ts)
	displaced := false

	if idx > r.lead {
		r.lead = idx
	}
	// Anything older than the window is outside every bucket we keep. Fold it to
	// the leading edge rather than discard it, and tell the caller.
	if r.lead-idx >= int64(r.win.Buckets) {
		idx = r.lead
		displaced = true
	}

	slot := &r.buckets[((idx%int64(r.win.Buckets))+int64(r.win.Buckets))%int64(r.win.Buckets)]
	if slot.index != idx {
		// Stale slot from a previous lap: reset rather than accumulate across laps.
		*slot = bucket{index: idx}
	}
	slot.count++
	slot.sum += amount
	if near {
		slot.near++
		slot.nearSum += amount
	}
	if day >= 0 && day < 64 {
		slot.days |= 1 << uint(day)
	}
	return displaced
}

// read sums the live buckets. Cost is Buckets adds, independent of event volume.
func (r *ring) read() (count int, sum float64, near int, nearSum float64, days int) {
	oldest := r.lead - int64(r.win.Buckets) + 1
	var mask uint64
	for i := range r.buckets {
		b := &r.buckets[i]
		if b.index < oldest || b.index > r.lead {
			continue // stale lap or future; either way not in this window
		}
		count += b.count
		sum += b.sum
		near += b.near
		nearSum += b.nearSum
		mask |= b.days
	}
	// popcount
	for mask != 0 {
		mask &= mask - 1
		days++
	}
	return
}

// Observation is what a window says about a key, and why.
//
// Every field is a number a rule can test and an investigator can read back. This
// is the per-alert attribution a supervisor asks for: not "velocity exceeded" but
// "9 transactions totalling 84,600, of which 8 fell in [9,000, 10,000), spread over
// 5 distinct days".
type Observation struct {
	Window string
	Span   time.Duration
	// Count and Sum are the plain aggregate over the window.
	Count int
	Sum   float64
	// Near and NearSum cover transactions just below the reporting threshold —
	// the structuring signal. Zero when no threshold was supplied.
	Near    int
	NearSum float64
	// Days is the number of distinct calendar days contributing.
	Days int
	// Quantum is this window's resolution, so a reader knows the error bound on
	// the boundary rather than assuming exactness.
	Quantum time.Duration
}

// Key identifies what is being aggregated over. Velocity is meaningless without
// saying velocity of what: one account, one counterparty pair, one device.
type Key struct {
	OrgID string
	// Kind names the axis ("account", "counterparty", "device", "ip").
	Kind string
	// Value is the identifier on that axis.
	Value string
}

func (k Key) id() string { return k.OrgID + "\x00" + k.Kind + "\x00" + k.Value }

// entry is one key's rings, and its place in its tenant's recency order.
type entry struct {
	id      string
	rings   []*ring
	updated time.Time
	// older and newer thread every key of ONE tenant into a recency list, so the
	// key a tenant drops when it reaches its own bound is found in constant time
	// rather than by scanning the tenant's whole keyspace on every arrival.
	older, newer *entry
}

// keyCost is the memory one key occupies, in bytes.
//
// Almost all of it is buckets: one ring per window, each allocated whole on the
// key's first transaction, and everything else about a key is a fixed handful of
// words. It is DERIVED from the windows rather than written down, because the
// windows are the thing an operator tunes and a hand-written cost goes stale the
// first time a resolution moves.
//
// The allowance covers what the arithmetic cannot see: the allocator rounds each
// ring up to a size class, the map keeps its own bookkeeping, and the key string
// is as long as the identifiers the institution uses. It is deliberately generous,
// because a ceiling derived from a cost that UNDER-states the real one is a number
// an operator would size a pod from and be wrong. TestTheCeilingIsInBytesAndItIsHonest
// weighs a full store against it.
func keyCost(ws []Window) int64 {
	var buckets int64
	for _, w := range ws {
		buckets += int64(w.Buckets)
	}
	return buckets*int64(unsafe.Sizeof(bucket{})) + perKeyOverhead
}

// perKeyOverhead is the allowance above the buckets themselves. Measured at
// about 1.4 KB for the standard windows; carried at 2 KB so the ceiling stays an
// upper bound as identifiers and Go's size classes vary.
const perKeyOverhead = 2048

// Grade is how close a tenant is to its own bound, in words rather than in a
// ratio a reader has to interpret.
//
// It exists because the alternative to saying this out loud is a control that
// switches itself off quietly. A tenant at [GradeFull] is dropping aggregates it
// would otherwise have used to find structuring, and "no findings" is what a
// clean institution looks like too.
type Grade string

const (
	// GradeClear is a tenant comfortably inside its bound.
	GradeClear Grade = "clear"
	// GradeCrowded is a tenant near it: nothing is lost yet and the next
	// arrivals will start costing it keys.
	GradeCrowded Grade = "crowded"
	// GradeFull is a tenant at it, dropping its own least recently used keys.
	GradeFull Grade = "full"
)

// crowded is the share of a tenant's bound above which it is reported crowded.
const crowded = 0.9

// Load is what one tenant is holding, and what it has lost.
type Load struct {
	Org  string `json:"org"`
	Keys int    `json:"keys"`
	// Bytes and Ceiling are this tenant's own, never the process's.
	Bytes   int64 `json:"bytes"`
	Ceiling int64 `json:"ceiling"`
	// Room is how many keys the ceiling allows.
	Room int `json:"room"`
	// Dropped counts the keys this tenant has lost to its OWN bound. It is never
	// another tenant's doing: no operation in this package can reach across.
	Dropped int64 `json:"dropped"`
	Grade   Grade `json:"grade"`
}

// Pressure is what the process is holding, for the operator.
//
// It carries no tenant's name and no tenant's numbers: a caller entitled to its
// own Load is not entitled to know which other institutions this engine serves.
type Pressure struct {
	Orgs    int   `json:"orgs"`
	Room    int   `json:"room"`
	Refused int64 `json:"refused"`
	Held    int64 `json:"held"`
	Ceiling int64 `json:"ceiling"`
	Late    int64 `json:"late"`
}

// keyspace is ONE tenant's keys, its own bound, and its own recency order.
//
// One lock per tenant, so no institution ever waits on another's traffic. Within
// a tenant the aggregates are written on the same path as the durable row for
// the same transaction, which is a single writer already, so a second level of
// striping here would buy nothing and cost a map per shard per tenant.
type keyspace struct {
	room int

	mu             sync.Mutex
	keys           map[string]*entry
	newest, oldest *entry
	dropped        int64
}

// Store holds live aggregates per tenant, per key, across many windows.
//
// Safe for concurrent use.
type Store struct {
	cfg Config
	// cost is the bytes one key occupies and room is how many of them one
	// tenant's byte ceiling buys. The count is DERIVED from the bytes, in one
	// place, so there is no second bound that could disagree with the first.
	cost int64
	room int
	orgs *roster.Roster[*keyspace]

	late atomic.Int64
}

// New builds a Store. Zero-value Config is valid and yields the documented
// defaults.
func New(cfg Config) *Store {
	cfg = cfg.withDefaults()
	cost := keyCost(cfg.Windows)
	room := int(cfg.PerOrg / cost)
	if room < 1 {
		// A tenant that may hold no key at all would report every account as
		// having done nothing, which is the one answer this store must never
		// invent. One key is the floor.
		room = 1
	}
	return &Store{cfg: cfg, cost: cost, room: room, orgs: roster.New[*keyspace](cfg.Orgs)}
}

// Record adds a transaction to every window for key.
//
// threshold is the reporting limit this transaction should be judged against — the
// 10,000 of a cash-reporting rule. Pass 0 when no threshold applies and the
// near-threshold counters stay zero. Amount and threshold must share a currency;
// this package does not convert, because a silent FX assumption inside a compliance
// aggregate is a defect waiting to be discovered by an auditor.
//
// A tenant this process has no room for records nothing. That is a gap in a
// control and it is not hidden: it is counted by the roster and published by
// [Store.Pressure].
func (s *Store) Record(k Key, ts time.Time, amount, threshold float64) {
	ks, ok := s.keyspaceFor(k.OrgID)
	if !ok {
		return
	}

	near := isNear(amount, threshold, s.cfg.NearThresholdBand)
	day := int(ts.UTC().Unix() / 86400 % 64)
	id := k.id()

	ks.mu.Lock()
	e := ks.keys[id]
	if e == nil {
		for len(ks.keys) >= ks.room {
			ks.dropOldest()
		}
		e = &entry{id: id, rings: make([]*ring, len(s.cfg.Windows))}
		for i, w := range s.cfg.Windows {
			e.rings[i] = newRing(w)
		}
		ks.keys[id] = e
	}
	displaced := false
	for _, r := range e.rings {
		if r.add(ts, amount, near, day) {
			displaced = true
		}
	}
	if ts.After(e.updated) {
		e.updated = ts
	}
	ks.touch(e)
	ks.mu.Unlock()

	if displaced {
		s.late.Add(1)
	}
}

// keyspaceFor is this tenant's own keys, admitting the tenant if the process has
// room for another. It never takes another tenant's place — see pkg/roster.
func (s *Store) keyspaceFor(org string) (*keyspace, bool) {
	return s.orgs.Hold(org, func() *keyspace {
		return &keyspace{room: s.room, keys: make(map[string]*entry)}
	})
}

// touch moves a key to the newest end of its tenant's recency order. Caller
// holds ks.mu.
func (ks *keyspace) touch(e *entry) {
	if ks.newest == e {
		return
	}
	ks.unlink(e)
	e.older, e.newer = ks.newest, nil
	if ks.newest != nil {
		ks.newest.newer = e
	}
	ks.newest = e
	if ks.oldest == nil {
		ks.oldest = e
	}
}

func (ks *keyspace) unlink(e *entry) {
	if e.older != nil {
		e.older.newer = e.newer
	} else if ks.oldest == e {
		ks.oldest = e.newer
	}
	if e.newer != nil {
		e.newer.older = e.older
	} else if ks.newest == e {
		ks.newest = e.older
	}
	e.older, e.newer = nil, nil
}

// dropOldest drops this tenant's least recently used key. Caller holds ks.mu.
//
// The only key it can reach belongs to the tenant whose keyspace this is. That
// is the whole difference between a bound and a way for one institution to
// silence another's monitoring.
func (ks *keyspace) dropOldest() {
	e := ks.oldest
	if e == nil {
		return
	}
	ks.unlink(e)
	delete(ks.keys, e.id)
	ks.dropped++
}

// Observe returns one Observation per configured window. An unknown key yields
// zeroed observations rather than an error: "this entity has done nothing" is a
// legitimate and common answer, and forcing callers to distinguish it from failure
// invites them to ignore both.
func (s *Store) Observe(k Key) []Observation {
	empty := func() []Observation {
		out := make([]Observation, 0, len(s.cfg.Windows))
		for _, w := range s.cfg.Windows {
			out = append(out, Observation{Window: w.Name, Span: w.Span, Quantum: w.Span / time.Duration(w.Buckets)})
		}
		return out
	}
	ks, ok := s.orgs.Get(k.OrgID)
	if !ok {
		return empty()
	}

	id := k.id()
	ks.mu.Lock()
	defer ks.mu.Unlock()
	e := ks.keys[id]
	if e == nil {
		return empty()
	}
	out := make([]Observation, 0, len(s.cfg.Windows))
	for _, r := range e.rings {
		c, sum, near, nearSum, days := r.read()
		out = append(out, Observation{
			Window:  r.win.Name,
			Span:    r.win.Span,
			Count:   c,
			Sum:     sum,
			Near:    near,
			NearSum: nearSum,
			Days:    days,
			Quantum: r.win.Span / time.Duration(r.win.Buckets),
		})
	}
	return out
}

// Load is what this tenant is holding against its own bound.
func (s *Store) Load(org string) Load {
	l := Load{Org: org, Ceiling: int64(s.room) * s.cost, Room: s.room, Grade: GradeClear}
	ks, ok := s.orgs.Get(org)
	if !ok {
		return l
	}
	ks.mu.Lock()
	l.Keys, l.Dropped = len(ks.keys), ks.dropped
	ks.mu.Unlock()
	l.Bytes = int64(l.Keys) * s.cost
	switch {
	case l.Keys >= ks.room:
		l.Grade = GradeFull
	case float64(l.Keys) >= crowded*float64(ks.room):
		l.Grade = GradeCrowded
	}
	return l
}

// Pressure is what the whole process is holding.
func (s *Store) Pressure() Pressure {
	p := Pressure{
		Orgs:    s.orgs.Held(),
		Room:    s.orgs.Ceiling(),
		Refused: s.orgs.Refused(),
		Ceiling: s.Ceiling(),
		Late:    s.Late(),
	}
	s.orgs.Each(func(_ string, ks *keyspace) bool {
		ks.mu.Lock()
		p.Held += int64(len(ks.keys)) * s.cost
		ks.mu.Unlock()
		return true
	})
	return p
}

// Ceiling is the most this store may hold, in bytes: every tenant it will admit,
// each full. It is the number to hold against a pod's memory limit.
func (s *Store) Ceiling() int64 { return int64(s.orgs.Ceiling()) * int64(s.room) * s.cost }

// Bytes is what it is holding now.
func (s *Store) Bytes() int64 { return s.Pressure().Held }

// Room is how many keys ONE tenant may hold.
func (s *Store) Room() int { return s.room }

// Late is the number of transactions folded to the leading edge because they
// arrived older than their window. A non-zero and growing value means the feed is
// delivering outside MaxLateness and the aggregates are less precise than they
// look — which is a fact an operator must be able to see, not one to bury.
func (s *Store) Late() int64 { return s.late.Load() }

// Windows are the windows this Store keeps, so a caller that reads observations
// by name can check at construction that the names it needs exist rather than
// silently reading zero for a window nobody configured.
func (s *Store) Windows() []Window {
	return append([]Window(nil), s.cfg.Windows...)
}

// Keys is the number of live keys across every tenant, for capacity monitoring
// against [Store.Ceiling].
func (s *Store) Keys() int {
	n := 0
	s.orgs.Each(func(_ string, ks *keyspace) bool {
		ks.mu.Lock()
		n += len(ks.keys)
		ks.mu.Unlock()
		return true
	})
	return n
}

// isNear reports whether amount sits in the band just below threshold.
//
// Strictly below: a transaction AT the threshold is reported, not structured, and
// counting it as evasion would flag the honest case.
func isNear(amount, threshold, band float64) bool {
	if threshold <= 0 || amount <= 0 || amount >= threshold {
		return false
	}
	return amount >= threshold*(1-band)
}

// Structuring is a finding: several transactions deliberately kept below a
// reporting threshold.
type Structuring struct {
	Window string
	// Count and Total are the near-threshold transactions and their sum.
	Count int
	Total float64
	// Days is how many distinct days they spread over. One day is a busy
	// afternoon; five days is a pattern.
	Days int
	// Threshold is the limit being evaded, so the finding is self-describing.
	Threshold float64
}

// DetectStructuring reports windows where the near-threshold pattern is present.
//
// minCount is how many sub-threshold transactions constitute a pattern. There is no
// universally correct value — it is a risk-appetite decision, which is why it is a
// parameter and not a constant. What this function will not do is infer one.
//
// The test is deliberately count-and-spread rather than sum-over-threshold. Summing
// catches anyone who moves a lot of money in small pieces, most of whom are
// payroll; clustering just under a specific limit is the thing that only makes
// sense as evasion.
func DetectStructuring(obs []Observation, threshold float64, minCount int) []Structuring {
	if threshold <= 0 || minCount <= 0 {
		return nil
	}
	var out []Structuring
	for _, o := range obs {
		if o.Near >= minCount {
			out = append(out, Structuring{
				Window:    o.Window,
				Count:     o.Near,
				Total:     o.NearSum,
				Days:      o.Days,
				Threshold: threshold,
			})
		}
	}
	// Widest window first: a 30-day pattern is the more damning finding and should
	// lead the case narrative.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// Deviation reports how far a value sits from a baseline, in multiples of the
// baseline, for rules phrased as "unusually large for this customer".
//
// This is the statutory test, not a threshold: consistency is assessed against
// knowledge of THIS customer, so the comparison must be relative. A flat limit
// cannot express it — it flags every large customer and misses every small one
// behaving anomalously.
//
// Returns 0 when there is no baseline to compare against, so an absent history
// reads as "no deviation" rather than as infinite deviation. A new customer is not
// suspicious for being new.
func Deviation(value, baseline float64) float64 {
	if baseline <= 0 || math.IsNaN(baseline) || math.IsNaN(value) {
		return 0
	}
	return (value - baseline) / baseline
}
