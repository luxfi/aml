// Package anomaly scores whether a transaction is unusual for the entity that
// made it, as a complement to the rule library rather than a replacement for it.
//
// WHY THIS EXISTS. Rules encode typologies someone has already named. They are
// precise, defensible and blind to anything nobody has written down yet, and
// because each one is a fixed predicate, the only way to widen coverage is to
// widen the predicate — which is how a monitoring system arrives at a volume of
// alerts that does not repay the cost of reading them. A density model answers a
// different question: not "does this match a known pattern" but "is this where
// this customer's behaviour normally lives". That is the statutory question. What
// must be monitored is consistency with the customer's own profile, and no fixed
// threshold can express it: a limit that fits a large customer misses a small one
// behaving strangely, and one that fits the small customer buries the large one.
//
// WHY ATTRIBUTABILITY DECIDED THE ALGORITHM. Machine detection is admissible
// here only behind three things: a mapping from typologies to the data features
// that express them, a stated appetite for how much the model may miss, and, for
// every alert that reaches an investigator, an explanation illustrated by the
// features that influenced it and the risk indicators behind them. The third
// requirement eliminates most of the field. A neural network's contribution to a
// score is not naturally attributable to an input, so anything built on one needs
// a second model fitted afterwards to guess at its own reasons — and a supervisor
// is then being shown an approximation of an explanation, which is a weaker thing
// than it appears. A tree over half-spaces can be interrogated directly: the
// score is a pure function of the coordinates, so moving one coordinate to its
// neutral value and rescoring gives that feature's contribution exactly, on the
// model that raised the alert. See Store.explain. Accuracy was not the deciding
// criterion; the ability to answer the question a regulator actually asks was.
//
// WHAT IT CANNOT DO, BY CONSTRUCTION. The model may summon a human and may not
// act: its evidence is capped below any enforcement action, it can only add to a
// transaction's score and never subtract, and where it cannot score, the
// transaction is evaluated on rules alone and the refusal is counted rather than
// passed off as a clean result. Silence is never evidence of innocence here, and
// nothing downstream is allowed to read it as such.
//
// Detector: Tan, Ting & Liu, "Fast Anomaly Detection for Streaming Data",
// IJCAI 2011. Requirements: HIP-0518 section 6.
package anomaly

import (
	crypto "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
)

// RuleID and RuleName are what the model's evidence carries into an alert.
//
// The model does not get its own alert type, its own store or its own review
// queue. It produces the same alert a rule produces, so case management,
// triage, webhooks and the reviewer's screen need to know nothing about how it
// was reached — which is also what keeps the promise that a model alert is
// reviewed by a person on the same terms as any other.
const (
	RuleID   = "anomaly"
	RuleName = "Unusual for this customer"
)

// Reasons a transaction was not scored. Both are ordinary and neither is a
// clean bill of health.
const (
	// ReasonWarming means the model has not yet learned enough to have an
	// opinion. It is the state after every start and every restore.
	ReasonWarming = "warming"
	// ReasonUnusable means the features could not be computed to numbers the
	// model can accept, so the point was neither scored nor learned from.
	ReasonUnusable = "unusable"
	// ReasonUnidentified means the transaction names no account and no user, so
	// there is no subject whose behaviour this could be unusual for. Scoring it
	// against an aggregate keyed on nothing would pool every anonymous
	// transaction in the tenant into one imaginary customer.
	ReasonUnidentified = "unidentified"
)

// histBuckets is the resolution of the score distribution the alert threshold is
// derived from. 256 puts the threshold within 0.4% of the intended quantile,
// which is finer than the appetite it serves is ever set.
const histBuckets = 256

// snapshotVersion is the layout of persisted state. State from another version is
// rejected rather than reinterpreted.
const snapshotVersion = 1

// Appetite is the risk-appetite decision the model is not permitted to make for
// itself.
//
// A model's output is a probability, so how likely it is to MISS suspicious
// activity is a matter of appetite that has to be explained and agreed rather
// than absorbed into a constant. This is that appetite, stated where it can be
// read, changed and reviewed.
//
// Review is the lever. It is the share of the stream the model may send for
// examination, and the alert threshold is derived from it as a quantile of the
// scores actually observed rather than fixed at a number someone liked. That
// matters for two reasons. An alert level must be governed rather than tuned, and
// must not be set to fit the size of the team or to generate volume nobody reads;
// a stated share with a measured realised share is exactly the governed form. And
// a fixed threshold on a drifting distribution silently becomes either silence or
// a flood, whereas a quantile holds its meaning.
//
// The miss rate is the consequence, and it cannot be computed from the stream:
// there are no labels, so nothing in this package knows what it missed. Sample is
// the instrument that measures it. It retains a share of the transactions the
// model did NOT alert on for review below the line, which is the standard way the
// question is answered and is separately required of any monitoring system —
// ex-post review of a sample of all processed transactions, to test and improve
// the system. Selection is by hash of the transaction ID, so the sample is
// reproducible by anyone holding the same appetite and cannot be steered.
//
// Warm is how many transactions the model must learn before it may alert at all.
// Below it the model has no reference to compare against and its scores mean
// nothing, so it says nothing.
type Appetite struct {
	Review float64 `json:"review"`
	Sample float64 `json:"sample"`
	Warm   int     `json:"warm"`
}

// Config parameterises a Store. The zero value is valid and yields the
// documented defaults.
//
// Trees, Depth and Window are the detector's shape. Window is how many
// transactions make up one reference window; Blend is how much of a closing
// window folds into the reference, where 1 is the published replacement and the
// default is lower to make the reference expensive to move. Weight is how much
// evidence one alert contributes to a transaction's score, and Action is what the
// alert asks for — capped, because a statistical judgement may ask for a person
// and may not stand in for one.
//
// Shadow is how the model is tested before it is trusted. A monitoring system has
// to be able to implement and test new detection before live activation; in
// shadow the model scores, learns, and records everything it would have alerted
// on, and contributes nothing to any transaction's outcome. What it would have
// done is then readable in State, over real traffic, before anyone depends on it.
type Config struct {
	Trees    int      `json:"trees"`
	Depth    int      `json:"depth"`
	Window   int      `json:"window"`
	Blend    float64  `json:"blend"`
	Appetite Appetite `json:"appetite"`
	Weight   float64  `json:"weight"`
	Severity string   `json:"severity"`
	Action   string   `json:"action"`
	Shadow   bool     `json:"shadow"`
	// MaxOrgs bounds how many tenants' models are held at once. Each costs
	// Trees * (2^(Depth+1)-1) nodes — a measured 336 KB at the defaults — and
	// models are created on a tenant's first transaction, so idle tenants cost
	// nothing. On overflow the least recently used is dropped, which returns
	// that tenant to warming: the model then declines to score rather than
	// scoring from state it does not have.
	MaxOrgs int `json:"max_orgs"`
	// Seed fixes the tree geometry. Left at zero it is drawn from the system
	// CSPRNG at construction, which is the right default: geometry that cannot
	// be predicted cannot be probed for a region to hide in. Setting it makes
	// scores reproducible across restarts at the cost of making the geometry
	// guessable from the seed, so a fixed seed belongs in KMS and not in a
	// manifest. Reproducing a past score does not need it in any case — a
	// Snapshot carries the seed it was built with.
	Seed uint64 `json:"-"`
}

func (c Config) withDefaults() Config {
	if c.Trees <= 0 {
		c.Trees = 25
	}
	if c.Depth <= 0 {
		c.Depth = 8
	}
	if c.Window <= 0 {
		c.Window = 256
	}
	if c.Blend <= 0 || c.Blend > 1 {
		c.Blend = 0.25
	}
	if c.Appetite.Review <= 0 || c.Appetite.Review > 0.5 {
		c.Appetite.Review = 0.01
	}
	if c.Appetite.Sample < 0 || c.Appetite.Sample > 1 {
		c.Appetite.Sample = 0.001
	}
	// The reference must have been folded at least once and the distribution
	// must hold enough scores for its tail to mean anything, so the floor is
	// expressed in windows rather than as a bare count.
	if c.Appetite.Warm < 2*c.Window {
		c.Appetite.Warm = 8 * c.Window
	}
	if c.Weight <= 0 {
		c.Weight = 0.2
	}
	if c.Severity == "" {
		c.Severity = types.SeverityMedium
	}
	if c.Action == "" {
		c.Action = types.ActionReview
	}
	if c.MaxOrgs <= 0 {
		c.MaxOrgs = 256
	}
	return c
}

var (
	// ErrWindows is returned when the aggregate store does not keep the windows
	// the inventory reads. Failing at construction is the point: the alternative
	// is a model that runs, alerts, and has been reading zero for every feature.
	ErrWindows = errors.New("anomaly: aggregate store is missing a window the inventory reads")
	// ErrAction is returned for a configured action above the ceiling.
	ErrAction = errors.New("anomaly: action exceeds the ceiling for model evidence")
	// ErrSnapshot is returned for state that cannot be trusted.
	ErrSnapshot = errors.New("anomaly: snapshot rejected")
)

// Store holds one model per tenant and scores transactions against it.
//
// Models are per tenant and never shared. A model learned across tenants would
// carry one customer's behaviour into another's score, which is a disclosure of
// the first tenant's activity however small — and the tenant boundary here is
// physical, so the model must not be the one place it is not. The tree geometry
// is per tenant too, so probing one tenant reveals nothing about where another's
// regions lie.
//
// Safe for concurrent use.
type Store struct {
	cfg  Config
	vel  *velocity.Store
	mu   sync.RWMutex
	orgs map[string]*model
}

type model struct {
	mu      sync.Mutex
	trees   []*tree
	seed    uint64
	seen    int   // learned into the open window
	learned int64 // learned ever
	hist    [histBuckets]float64
	cut     float64
	scored  int64
	alerted int64
	refused map[string]int64
	blind   [Dims]int64
	sample  []Sampled
	at      int
	updated time.Time
}

// New builds a Store reading aggregates from vel.
//
// It fails rather than degrades when vel does not keep a window the inventory
// reads, and when the configured action exceeds the ceiling.
func New(cfg Config, vel *velocity.Store) (*Store, error) {
	if vel == nil {
		return nil, fmt.Errorf("%w: no aggregate store", ErrWindows)
	}
	cfg = cfg.withDefaults()
	if types.ActionRank(cfg.Action) > types.ActionRank(types.ActionCeiling) {
		return nil, fmt.Errorf("%w: %q above %q", ErrAction, cfg.Action, types.ActionCeiling)
	}
	kept := map[string]bool{}
	for _, w := range vel.Windows() {
		kept[w.Name] = true
	}
	for _, f := range inventory {
		if f.Window != "" && !kept[f.Window] {
			return nil, fmt.Errorf("%w: feature %q reads %q", ErrWindows, f.Name, f.Window)
		}
	}
	if cfg.Seed == 0 {
		var b [8]byte
		if _, err := crypto.Read(b[:]); err != nil {
			return nil, fmt.Errorf("anomaly: seed: %w", err)
		}
		cfg.Seed = binary.LittleEndian.Uint64(b[:])
	}
	return &Store{cfg: cfg, vel: vel, orgs: map[string]*model{}}, nil
}

// Config returns the configuration in force, with the seed withheld.
func (s *Store) Config() Config {
	c := s.cfg
	c.Seed = 0
	return c
}

// Assess scores one transaction, lets the model learn from it, and returns the
// evidence when it alerts. It satisfies the engine's Scorer.
//
// ok is false whenever the model has nothing to contribute — because it is still
// warming, because the features were unusable, because the score is inside the
// appetite, or because it is in shadow. The caller evaluates on rules alone in
// every one of those cases; none of them is a verdict of normal, and the counts
// behind each are in State so that the difference is legible.
//
// The transaction must already have been recorded to the aggregate store. Reading
// aggregates that include it is deliberate: the numbers quoted in the alert are
// then the same ones an investigator sees when they look at the account, and
// every baseline in the feature set has the transaction removed from it
// arithmetically so that nothing is measured against itself.
func (s *Store) Assess(tx types.Transaction, entity types.Entity) (types.RuleHit, bool) {
	a := s.Learn(tx, entity)
	if !a.Alert {
		return types.RuleHit{}, false
	}
	return types.RuleHit{
		Rule: types.Rule{
			ID:       RuleID,
			OrgID:    tx.OrgID,
			Name:     RuleName,
			Severity: s.cfg.Severity,
			// Evidence scales with how unusual the transaction is, so a
			// borderline score contributes less than a plain one through the
			// same weight-of-evidence sum the rules use. There is no second
			// scoring path.
			Weight:  s.cfg.Weight * a.Score,
			Action:  s.cfg.Action,
			Enabled: true,
		},
		Match:  true,
		Causes: a.Causes,
	}, true
}

// Learn scores one transaction, lets the model learn from it, and returns the
// whole assessment.
//
// It is what Assess does, with the answer not yet reduced to the engine's Scorer
// shape. The two cannot disagree about what the model did, because Assess is
// defined in terms of this and there is no second scoring path — which is the
// property that lets a study of the model (pkg/topology) replay a tenant's own
// history through the code that scores production, and read the score of every
// event as the model learned it, rather than through a copy of the algorithm
// that would be free to be wrong.
func (s *Store) Learn(tx types.Transaction, entity types.Entity) Assessment {
	return s.judge(tx, true)
}

// Inspect scores one transaction without learning from it, moving any counter, or
// touching the aggregate store. It is how a candidate is tried against a
// tenant's real behaviour before anything depends on the answer, and it is the
// model's analogue of testing a rule.
//
// Because it records nothing, the aggregates it reads do not include the
// candidate: its numbers are the tenant's history as it stands. That is the
// question worth asking of a hypothetical, and it is one transaction away from
// what the live path would compute for the same input.
func (s *Store) Inspect(tx types.Transaction, entity types.Entity) Assessment {
	return s.judge(tx, false)
}

// Assessment is the model's full verdict on one transaction.
type Assessment struct {
	// Scored is false when the model declined; Reason says which refusal.
	Scored bool   `json:"scored"`
	Reason string `json:"reason,omitempty"`
	// Score is the anomaly score in [0,1]: 0 where the tenant's recent
	// behaviour is densest, 1 where there is none of it.
	Score float64 `json:"score"`
	// Cut is the threshold in force, derived from the appetite.
	Cut float64 `json:"cut"`
	// Alert is whether this becomes evidence. False in shadow however high the
	// score, and Shadow says so.
	Alert  bool `json:"alert"`
	Shadow bool `json:"shadow"`
	// Causes is the per-feature attribution, ordered by contribution.
	Causes []types.Cause `json:"causes,omitempty"`
	// Values is every coordinate, including the ones that contributed nothing,
	// so a reviewer sees what the model read and not only what it concluded.
	Values []Value `json:"values,omitempty"`
}

// Value is one coordinate as the model read it.
type Value struct {
	Feature  string  `json:"feature"`
	X        float64 `json:"x"`
	Observed float64 `json:"observed"`
	Baseline float64 `json:"baseline"`
	Unit     string  `json:"unit"`
	Blind    bool    `json:"blind"`
}

// Sampled is one transaction retained below the line: scored, not alerted, and
// selected for review so that what the model misses can be measured instead of
// assumed.
type Sampled struct {
	TxID  string    `json:"tx_id"`
	At    time.Time `json:"at"`
	Score float64   `json:"score"`
	Cut   float64   `json:"cut"`
	Top   string    `json:"top"`
}

// judge reads the aggregates for one transaction, projects it, and weighs it.
func (s *Store) judge(tx types.Transaction, learn bool) Assessment {
	if account(tx) == "" {
		m := s.model(tx.OrgID)
		m.mu.Lock()
		m.refused[ReasonUnidentified]++
		m.mu.Unlock()
		return Assessment{Shadow: s.cfg.Shadow, Reason: ReasonUnidentified}
	}
	p := project(tx.USD,
		s.vel.Observe(velocity.Key{OrgID: tx.OrgID, Kind: AxisAccount, Value: account(tx)}),
		pairObs(s.vel, tx),
		deviceObs(s.vel, tx),
	)
	return s.weigh(tx.OrgID, p, tx.ID, tx.Timestamp, learn)
}

// weigh scores a point that has already been projected.
//
// Split from judge because projection reads the aggregate store and weighing does
// not. Two things follow from the split, and both matter more than the tidiness:
// what the trees will accept is decided in one place on a value that can be
// handed in, so the gate can be exercised rather than reasoned about; and the
// model's behaviour over a sequence of points is testable without a stream to
// produce them.
func (s *Store) weigh(orgID string, p Point, txID string, at time.Time, learn bool) Assessment {
	inv := inventory
	a := Assessment{Shadow: s.cfg.Shadow, Values: values(p, inv)}
	m := s.model(orgID)

	m.mu.Lock()
	defer m.mu.Unlock()

	if !p.usable() {
		m.refused[ReasonUnusable]++
		a.Reason = ReasonUnusable
		return a
	}
	for i := range p.Blind {
		if p.Blind[i] && learn {
			m.blind[i]++
		}
	}

	a.Score = m.score(p.X[:], s.cfg)
	a.Cut = m.cut
	warm := m.learned >= int64(s.cfg.Appetite.Warm)

	if learn {
		m.learn(p.X[:], s.cfg)
		m.updated = time.Now().UTC()
	}
	if !warm {
		if learn {
			m.refused[ReasonWarming]++
		}
		a.Reason = ReasonWarming
		return a
	}

	a.Scored = true
	if learn {
		m.scored++
	}

	// Strictly above: a score equal to the cut is inside the appetite. The cut
	// is the upper edge of the bucket that exhausts the review budget, so the
	// realised share stays at or below the stated one rather than above it.
	if a.Score > m.cut {
		if learn {
			m.alerted++
		}
		a.Causes = m.explain(p, a.Score, s.cfg, inv)
		a.Alert = !s.cfg.Shadow
		return a
	}
	if learn && belowLine(txID, s.cfg.Appetite.Sample) {
		m.keep(Sampled{TxID: txID, At: at, Score: a.Score, Cut: m.cut, Top: inv[p.far(inv)].Name})
	}
	return a
}

func pairObs(vel *velocity.Store, tx types.Transaction) []velocity.Observation {
	if tx.Counterparty == "" || account(tx) == "" {
		return nil
	}
	return vel.Observe(velocity.Key{OrgID: tx.OrgID, Kind: AxisPair, Value: account(tx) + "\x1f" + tx.Counterparty})
}

func deviceObs(vel *velocity.Store, tx types.Transaction) []velocity.Observation {
	if tx.DeviceFingerprint == "" {
		return nil
	}
	return vel.Observe(velocity.Key{OrgID: tx.OrgID, Kind: AxisDevice, Value: tx.DeviceFingerprint})
}

func values(p Point, inv [Dims]Feature) []Value {
	out := make([]Value, 0, Dims)
	for i := range inv {
		out = append(out, Value{
			Feature: inv[i].Name, X: p.X[i], Observed: p.Raw[i],
			Baseline: p.Base[i], Unit: inv[i].Unit, Blind: p.Blind[i],
		})
	}
	return out
}

// score is the anomaly score for x against the reference window: the share of
// the forest that found x in a region emptier than uniform.
//
// Each tree votes on its own, in [0,1], and the votes are averaged. A tree whose
// region holds at least the mass a uniform spread would put there votes zero; one
// whose region is empty votes one; in between the vote is how far short the
// region falls. The number that comes out says something a reviewer can hold on
// to — nineteen of twenty-five independently drawn partitions of this tenant's
// behaviour put this transaction where that behaviour does not go.
//
// The published algorithm sums the raw masses and ranks by the total. That is
// sound for ranking and wrong here, and the reason is worth recording because it
// is not obvious: mass*2^depth is heavy-tailed, so a single tree that happens to
// find the point in a dense region returns a value many times the uniform one and
// swamps every tree that found emptiness. Ranking survives it; a calibrated score
// does not, and a calibrated score is what an appetite expressed as a quantile
// needs. Clamping each tree's contribution before averaging costs nothing and
// bounds any one tree's influence to 1/trees.
//
// Each tree is measured against its OWN root mass rather than the nominal window
// size, which makes the score self-calibrating: a forest still filling its
// reference is compared to the reference it actually has, so the score does not
// carry a warm-up bias that decays over the first few thousand transactions. It
// also means the window size governs only how fast the model forgets, and nothing
// about what a score means.
func (m *model) score(x []float64, cfg Config) float64 {
	var vote float64
	var voters int
	for _, t := range m.trees {
		scale := t.ref[0]
		if scale <= 0 {
			continue // no reference yet: this tree has no opinion
		}
		voters++
		if mass := t.mass(x, cfg.Depth, 0.1*scale); mass < scale {
			vote += 1 - mass/scale
		}
	}
	if voters == 0 {
		// Nothing learned. Unknown is not normal, so this reads as maximally
		// unusual rather than as clean — and the threshold starts above every
		// possible score, so it still cannot alert.
		return 1
	}
	return vote / float64(voters)
}

func (m *model) learn(x []float64, cfg Config) {
	for _, t := range m.trees {
		t.learn(x, cfg.Depth)
	}
	m.learned++
	m.seen++
	// The distribution is only fed once a reference exists, or it would be a
	// record of the cold model scoring everything as maximally unusual and the
	// threshold derived from it would be meaningless.
	if m.learned > int64(cfg.Window) {
		m.hist[bucket(m.score(x, cfg))]++
	}
	if m.seen >= cfg.Window {
		for _, t := range m.trees {
			t.roll(cfg.Blend)
		}
		m.seen = 0
		// The distribution forgets at the same rate as the trees, so the
		// threshold describes the same stretch of the stream the model does.
		for i := range m.hist {
			m.hist[i] *= 1 - cfg.Blend
		}
		m.cut = cut(m.hist[:], cfg.Appetite.Review)
	}
}

func bucket(score float64) int {
	i := int(score * histBuckets)
	if i >= histBuckets {
		i = histBuckets - 1
	}
	if i < 0 {
		i = 0
	}
	return i
}

// cut is the score above which alerting stays inside the appetite: the upper edge
// of the bucket at which the review budget runs out.
//
// Taking the upper edge makes the realised share at most the stated one. That is
// the direction a governed alert level has to err, and it has a consequence worth
// stating plainly: if more of the stream than the appetite allows sits in the top
// bucket, no threshold can honour the appetite, and this returns 1 — above every
// possible score — so the model alerts on nothing rather than on everything. A
// saturated model is a control that has stopped contributing, which is bad; a
// model that floods the queue is a control that has stopped being read, which is
// worse and is the failure the appetite exists to prevent. State reports the
// saturation either way, because the one thing that must not happen is for it to
// be invisible.
func cut(hist []float64, review float64) float64 {
	var total float64
	for _, c := range hist {
		total += c
	}
	if total <= 0 {
		return 1
	}
	budget := review * total
	var acc float64
	for i := len(hist) - 1; i >= 0; i-- {
		acc += hist[i]
		if acc > budget {
			return float64(i+1) / float64(len(hist))
		}
	}
	return 0
}

// explain attributes an alert to the features that produced it.
//
// For each coordinate the score is recomputed with that coordinate moved to its
// feature's neutral value and every other coordinate held. The drop is what that
// feature contributed. This is a counterfactual on the model that raised the
// alert, not a surrogate fitted to imitate it, so the number is exact with
// respect to the thing being explained — which is the whole reason the detector
// is a tree and not a network, and it is why the answer survives being
// challenged. The sentence it produces is the one a supervisor asks for: had this
// customer's daily count been their own average rather than eleven times it, the
// score would have been 0.31 instead of 0.88.
//
// Cost falls on alerts only, not on the stream: Dims+1 traversals, at the
// appetite's share of transactions.
//
// A share of zero across every cause is a real answer and not a failure. It means
// no single feature accounts for the score and the conjunction does — repairing
// any one coordinate alone leaves the point just as isolated. The causes are then
// ordered by distance from neutral, which still names the features involved, and
// the counterfactual score printed against each shows why none of them is
// individually to blame.
func (m *model) explain(p Point, score float64, cfg Config, inv [Dims]Feature) []types.Cause {
	x := p.X // array: mutated by value, the caller's point is untouched
	var without, drop [Dims]float64
	var total float64
	for i := range inv {
		without[i] = score
		if x[i] == inv[i].Neutral {
			continue
		}
		keep := x[i]
		x[i] = inv[i].Neutral
		without[i] = m.score(x[:], cfg)
		x[i] = keep
		if d := score - without[i]; d > 0 {
			drop[i] = d
			total += d
		}
	}

	causes := make([]types.Cause, 0, Dims)
	for i := range inv {
		if x[i] == inv[i].Neutral {
			continue // contributed nothing: it is already at neutral
		}
		c := types.Cause{
			Feature: inv[i].Name, Typology: inv[i].Typology,
			Indicator: inv[i].Indicator, Citation: inv[i].Citation,
			Severity: inv[i].Severity, Unit: inv[i].Unit,
			Observed: p.Raw[i], Baseline: p.Base[i], Without: without[i],
		}
		if total > 0 {
			c.Share = drop[i] / total
		}
		causes = append(causes, c)
	}
	if total > 0 {
		sort.SliceStable(causes, func(a, b int) bool { return causes[a].Share > causes[b].Share })
		return causes
	}
	byDistance(causes, p, inv)
	return causes
}

func byDistance(causes []types.Cause, p Point, inv [Dims]Feature) {
	away := map[string]float64{}
	for i := range inv {
		d := p.X[i] - inv[i].Neutral
		if d < 0 {
			d = -d
		}
		away[inv[i].Name] = d
	}
	sort.SliceStable(causes, func(a, b int) bool { return away[causes[a].Feature] > away[causes[b].Feature] })
}

// belowLine selects a transaction for review below the line by hash of its
// identifier.
//
// Hashing rather than drawing at random costs nothing and buys two things: the
// same transaction is always either in the sample or out of it, so a reviewer
// holding the appetite can verify that the sample was not steered; and there is
// no shared random state on the scoring path.
func belowLine(txID string, rate float64) bool {
	if rate <= 0 || txID == "" {
		return false
	}
	var h uint32 = 2166136261
	for i := 0; i < len(txID); i++ {
		h ^= uint32(txID[i])
		h *= 16777619
	}
	const span = 1 << 20
	return float64(h%span) < rate*span
}

func (m *model) keep(s Sampled) {
	if len(m.sample) < cap(m.sample) {
		m.sample = append(m.sample, s)
		return
	}
	if cap(m.sample) == 0 {
		return
	}
	m.sample[m.at] = s
	m.at = (m.at + 1) % cap(m.sample)
}

// sampleDepth is how many below-the-line transactions are retained per tenant.
// It is a window onto the sample, not the sample itself: durable retention for a
// review cycle is the caller's, since only the caller knows the retention period
// it owes.
const sampleDepth = 256

func (s *Store) model(orgID string) *model {
	s.mu.RLock()
	m := s.orgs[orgID]
	s.mu.RUnlock()
	if m != nil {
		return m
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if m = s.orgs[orgID]; m != nil {
		return m
	}
	if len(s.orgs) >= s.cfg.MaxOrgs {
		s.evict()
	}
	m = s.plant(orgID, mix(s.cfg.Seed, orgID))
	s.orgs[orgID] = m
	return m
}

func (s *Store) plant(orgID string, seed uint64) *model {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	m := &model{
		trees:   make([]*tree, s.cfg.Trees),
		seed:    seed,
		refused: map[string]int64{ReasonWarming: 0, ReasonUnusable: 0, ReasonUnidentified: 0},
		sample:  make([]Sampled, 0, sampleDepth),
		cut:     1, // admit nothing until a distribution exists
		updated: time.Now().UTC(),
	}
	for i := range m.trees {
		m.trees[i] = plant(rng, Dims, s.cfg.Depth)
	}
	return m
}

// evict drops the least recently used tenant's model. Caller holds s.mu.
func (s *Store) evict() {
	var oldest string
	var at time.Time
	first := true
	for id, m := range s.orgs {
		m.mu.Lock()
		u := m.updated
		m.mu.Unlock()
		if first || u.Before(at) {
			oldest, at, first = id, u, false
		}
	}
	if oldest != "" {
		delete(s.orgs, oldest)
	}
}

// mix derives a tenant's seed from the store's seed and the tenant's identity, so
// that no two tenants share a geometry and neither can be derived from the other
// without the store's seed. SplitMix64 finalisation over an FNV-1a of the id.
func mix(seed uint64, orgID string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(orgID); i++ {
		h ^= uint64(orgID[i])
		h *= 1099511628211
	}
	z := seed ^ h
	z += 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// State is what a review of the model reads. It covers one tenant only: a caller
// scoped to a tenant cannot learn another's volumes, alert rate or behaviour from
// it.
type State struct {
	Config    Config        `json:"config"`
	Inventory [Dims]Feature `json:"inventory"`
	Digest    string        `json:"digest"`
	Org       string        `json:"org"`
	Learned   int64         `json:"learned"`
	Warm      bool          `json:"warm"`
	Cut       float64       `json:"cut"`
	Scored    int64         `json:"scored"`
	Alerted   int64         `json:"alerted"`
	// Realised is the share of scored transactions that alerted, against
	// Config.Appetite.Review which is the share intended. The two being
	// readable side by side is what makes the appetite a measured commitment
	// rather than a stated one.
	Realised float64 `json:"realised"`
	// Saturated means the appetite cannot be honoured by any threshold because
	// too much of the stream scores in the top bucket, so the model is alerting
	// on nothing. It is the one state that must never be mistaken for quiet.
	Saturated bool `json:"saturated"`
	// Refused counts transactions the model would not score, by reason. None of
	// these was examined by the model; all of them were examined by the rules.
	Refused map[string]int64 `json:"refused"`
	// Blind counts, per feature, how often it took its neutral value for want of
	// data. A feature blind on most traffic is not contributing whatever the
	// inventory claims for it.
	Blind map[string]int64 `json:"blind"`
	// Distribution is the score distribution the threshold is cut from, in 32
	// bands of 1/32.
	Distribution []float64 `json:"distribution"`
	// Sample is the below-the-line window: transactions scored, not alerted, and
	// retained for review so the miss rate can be measured.
	Sample []Sampled `json:"sample"`
}

// State reports the model for one tenant.
func (s *Store) State(orgID string) State {
	inv := inventory
	st := State{
		Config: s.Config(), Inventory: inv, Digest: s.Digest(), Org: orgID,
		Cut: 1, Refused: map[string]int64{}, Blind: map[string]int64{},
		Distribution: make([]float64, 32),
	}

	s.mu.RLock()
	m := s.orgs[orgID]
	s.mu.RUnlock()
	if m == nil {
		return st
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	st.Learned, st.Cut, st.Scored, st.Alerted = m.learned, m.cut, m.scored, m.alerted
	st.Warm = m.learned >= int64(s.cfg.Appetite.Warm)
	st.Saturated = st.Warm && m.cut >= 1
	if m.scored > 0 {
		st.Realised = float64(m.alerted) / float64(m.scored)
	}
	for k, v := range m.refused {
		st.Refused[k] = v
	}
	for i := range inv {
		st.Blind[inv[i].Name] = m.blind[i]
	}
	band := histBuckets / len(st.Distribution)
	for i, c := range m.hist {
		st.Distribution[i/band] += c
	}
	st.Sample = append([]Sampled(nil), m.sample...)
	return st
}

// Digest identifies the model's shape: the inventory in order and the detector's
// geometry parameters. Learned state is only meaningful against the shape that
// produced it, so the digest is what Restore checks.
func (s *Store) Digest() string {
	h := sha256.New()
	fmt.Fprintf(h, "v%d|%d|%d|%d|%g|", snapshotVersion, Dims, s.cfg.Trees, s.cfg.Depth, s.cfg.Blend)
	fmt.Fprintf(h, "%d|", s.cfg.Window)
	for _, f := range inventory {
		fmt.Fprintf(h, "%s:%s:%g|", f.Name, f.Window, f.Neutral)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Snapshot is one tenant's learned state, in a form that can be stored and
// restored.
//
// It carries the masses and the seed, never the geometry: geometry is a pure
// function of the seed, so it is regenerated on restore and cannot be supplied.
// That removes the sharpest edge a persisted model has — state that describes
// where the regions are, rather than only how full they are.
//
// Restoring is what keeps a restart from being a blind window. The model fails
// closed while warming, so a process that comes back with nothing learned
// declines to score for the whole warm period, and a control that is off for
// however long that takes is a control that was off.
type Snapshot struct {
	Version int         `json:"version"`
	Digest  string      `json:"digest"`
	OrgID   string      `json:"org_id"`
	Seed    uint64      `json:"seed"`
	Learned int64       `json:"learned"`
	Seen    int         `json:"seen"`
	Cut     float64     `json:"cut"`
	Ref     [][]float64 `json:"ref"`
	Cur     [][]float64 `json:"cur"`
	Hist    []float64   `json:"hist"`
}

// Snapshot returns one tenant's learned state, or false when that tenant has
// none.
func (s *Store) Snapshot(orgID string) (Snapshot, bool) {
	s.mu.RLock()
	m := s.orgs[orgID]
	s.mu.RUnlock()
	if m == nil {
		return Snapshot{}, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	snap := Snapshot{
		Version: snapshotVersion, Digest: s.Digest(), OrgID: orgID, Seed: m.seed,
		Learned: m.learned, Seen: m.seen, Cut: m.cut,
		Ref:  make([][]float64, len(m.trees)),
		Cur:  make([][]float64, len(m.trees)),
		Hist: append([]float64(nil), m.hist[:]...),
	}
	for i, t := range m.trees {
		snap.Ref[i] = append([]float64(nil), t.ref...)
		snap.Cur[i] = append([]float64(nil), t.cur...)
	}
	return snap, true
}

// Restore installs learned state for one tenant, replacing whatever that tenant
// had.
//
// Every check here exists because a snapshot is state the model will treat as its
// own memory of the tenant's behaviour. State that is merely stale makes the
// model wrong; state that has been chosen makes it wrong in a direction someone
// picked — a region can be filled until activity inside it reads as ordinary. So
// the shape must match, the tenant must match, and the masses must satisfy the
// invariant the algorithm cannot avoid producing: a region's mass is the sum of
// its two halves, because every point increments both a node and its parent, and
// folding a window is linear in the masses. An array that fails it was not
// produced by this algorithm.
//
// These are guards, not authentication. Anyone able to substitute a snapshot that
// satisfies the invariant can still shift the model, so the caller owes the
// snapshot integrity — it belongs where the tenant's own data belongs, and sealed
// if it travels.
func (s *Store) Restore(snap Snapshot) error {
	switch {
	case snap.Version != snapshotVersion:
		return fmt.Errorf("%w: version %d, want %d", ErrSnapshot, snap.Version, snapshotVersion)
	case snap.OrgID == "":
		return fmt.Errorf("%w: no tenant", ErrSnapshot)
	case snap.Digest != s.Digest():
		return fmt.Errorf("%w: shape does not match the running inventory", ErrSnapshot)
	case len(snap.Ref) != s.cfg.Trees || len(snap.Cur) != s.cfg.Trees:
		return fmt.Errorf("%w: %d trees, want %d", ErrSnapshot, len(snap.Ref), s.cfg.Trees)
	case len(snap.Hist) != histBuckets:
		return fmt.Errorf("%w: distribution has %d buckets, want %d", ErrSnapshot, len(snap.Hist), histBuckets)
	case snap.Learned < 0 || snap.Seen < 0 || snap.Seen > s.cfg.Window:
		return fmt.Errorf("%w: window position %d of %d after %d", ErrSnapshot, snap.Seen, s.cfg.Window, snap.Learned)
	case !finite(snap.Cut) || snap.Cut < 0 || snap.Cut > 1:
		return fmt.Errorf("%w: threshold %v outside [0,1]", ErrSnapshot, snap.Cut)
	}
	for _, c := range snap.Hist {
		if !finite(c) || c < 0 {
			return fmt.Errorf("%w: distribution holds %v", ErrSnapshot, c)
		}
	}

	m := s.plant(snap.OrgID, snap.Seed)
	nodes := 1<<(s.cfg.Depth+1) - 1
	for i, t := range m.trees {
		if len(snap.Ref[i]) != nodes || len(snap.Cur[i]) != nodes {
			return fmt.Errorf("%w: tree %d has %d nodes, want %d", ErrSnapshot, i, len(snap.Ref[i]), nodes)
		}
		copy(t.ref, snap.Ref[i])
		copy(t.cur, snap.Cur[i])
		if !t.sound(s.cfg.Depth) {
			return fmt.Errorf("%w: tree %d does not satisfy the mass invariant", ErrSnapshot, i)
		}
	}
	m.learned, m.seen, m.cut = snap.Learned, snap.Seen, snap.Cut
	copy(m.hist[:], snap.Hist)

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, held := s.orgs[snap.OrgID]; !held && len(s.orgs) >= s.cfg.MaxOrgs {
		s.evict()
	}
	s.orgs[snap.OrgID] = m
	return nil
}
