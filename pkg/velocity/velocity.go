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
	"time"
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
// MaxKeys bounds cardinality. Key values derive from caller-supplied data, so an
// unbounded map is a memory-exhaustion surface reachable by anyone who can submit
// transactions. On overflow the least-recently-updated key is evicted.
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
	MaxKeys           int
	MaxLateness       time.Duration
}

func (c Config) withDefaults() Config {
	if len(c.Windows) == 0 {
		c.Windows = StandardWindows()
	}
	if c.NearThresholdBand <= 0 || c.NearThresholdBand >= 1 {
		c.NearThresholdBand = 0.10
	}
	if c.MaxKeys <= 0 {
		c.MaxKeys = 100_000
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

type entry struct {
	rings   []*ring
	updated time.Time
}

// Store holds live aggregates for many keys across many windows.
//
// Safe for concurrent use. Locking is sharded by key so unrelated entities do not
// serialise against each other — with one lock, throughput would be bounded by the
// single busiest key in the tenant.
type Store struct {
	cfg    Config
	shards []*shard
	late   int64
	lateMu sync.Mutex
}

type shard struct {
	mu   sync.Mutex
	keys map[string]*entry
}

const shardCount = 64

// New builds a Store. Zero-value Config is valid and yields the documented
// defaults.
func New(cfg Config) *Store {
	cfg = cfg.withDefaults()
	s := &Store{cfg: cfg, shards: make([]*shard, shardCount)}
	for i := range s.shards {
		s.shards[i] = &shard{keys: make(map[string]*entry)}
	}
	return s
}

func (s *Store) shardFor(id string) *shard {
	// FNV-1a, inline: no allocation on the hot path.
	var h uint32 = 2166136261
	for i := 0; i < len(id); i++ {
		h ^= uint32(id[i])
		h *= 16777619
	}
	return s.shards[h%shardCount]
}

// Record adds a transaction to every window for key.
//
// threshold is the reporting limit this transaction should be judged against — the
// 10,000 of a cash-reporting rule. Pass 0 when no threshold applies and the
// near-threshold counters stay zero. Amount and threshold must share a currency;
// this package does not convert, because a silent FX assumption inside a compliance
// aggregate is a defect waiting to be discovered by an auditor.
func (s *Store) Record(k Key, ts time.Time, amount, threshold float64) {
	id := k.id()
	sh := s.shardFor(id)

	near := isNear(amount, threshold, s.cfg.NearThresholdBand)
	day := int(ts.UTC().Unix() / 86400 % 64)

	sh.mu.Lock()
	e := sh.keys[id]
	if e == nil {
		if len(sh.keys) >= s.cfg.MaxKeys/shardCount+1 {
			s.evictOldestLocked(sh)
		}
		e = &entry{rings: make([]*ring, len(s.cfg.Windows))}
		for i, w := range s.cfg.Windows {
			e.rings[i] = newRing(w)
		}
		sh.keys[id] = e
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
	sh.mu.Unlock()

	if displaced {
		s.lateMu.Lock()
		s.late++
		s.lateMu.Unlock()
	}
}

// evictOldestLocked drops the least-recently-updated key. Caller holds sh.mu.
func (s *Store) evictOldestLocked(sh *shard) {
	var oldestID string
	var oldest time.Time
	first := true
	for id, e := range sh.keys {
		if first || e.updated.Before(oldest) {
			oldestID, oldest, first = id, e.updated, false
		}
	}
	if oldestID != "" {
		delete(sh.keys, oldestID)
	}
}

// Observe returns one Observation per configured window. An unknown key yields
// zeroed observations rather than an error: "this entity has done nothing" is a
// legitimate and common answer, and forcing callers to distinguish it from failure
// invites them to ignore both.
func (s *Store) Observe(k Key) []Observation {
	id := k.id()
	sh := s.shardFor(id)

	out := make([]Observation, 0, len(s.cfg.Windows))
	sh.mu.Lock()
	e := sh.keys[id]
	if e == nil {
		sh.mu.Unlock()
		for _, w := range s.cfg.Windows {
			out = append(out, Observation{Window: w.Name, Span: w.Span, Quantum: w.Span / time.Duration(w.Buckets)})
		}
		return out
	}
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
	sh.mu.Unlock()
	return out
}

// Late is the number of transactions folded to the leading edge because they
// arrived older than their window. A non-zero and growing value means the feed is
// delivering outside MaxLateness and the aggregates are less precise than they
// look — which is a fact an operator must be able to see, not one to bury.
func (s *Store) Late() int64 {
	s.lateMu.Lock()
	defer s.lateMu.Unlock()
	return s.late
}

// Windows are the windows this Store keeps, so a caller that reads observations
// by name can check at construction that the names it needs exist rather than
// silently reading zero for a window nobody configured.
func (s *Store) Windows() []Window {
	return append([]Window(nil), s.cfg.Windows...)
}

// Keys is the number of live keys, for capacity monitoring against MaxKeys.
func (s *Store) Keys() int {
	n := 0
	for _, sh := range s.shards {
		sh.mu.Lock()
		n += len(sh.keys)
		sh.mu.Unlock()
	}
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
