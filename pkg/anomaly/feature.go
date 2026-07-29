package anomaly

import (
	"math"

	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
)

// Dims is the number of dimensions in the model space, one per Feature. It is a
// constant so a point is an array rather than a slice and scoring allocates
// nothing.
const Dims = 9

// Feature names one dimension of the model space and records the obligation it
// serves.
//
// Where a rule maps a risk indicator to a typology, a model maps a typology to
// the data features that express it, so the evidence of risk coverage is a
// maintained mapping from typology to feature. Inventory is that mapping, and it
// is code rather than a document because a mapping kept beside the model cannot
// drift away from what the model actually reads.
//
// Every field is here because a reviewer asks for it. Typology is the pattern.
// Indicator is the supervisor's own words for the thing being looked for.
// Citation is where those words come from, so the claim is checkable rather than
// asserted. Unit is how to read the raw number, which is what turns a coordinate
// into a sentence an investigator can put in a case file.
type Feature struct {
	Name      string  `json:"name"`
	Window    string  `json:"window,omitempty"`
	Typology  string  `json:"typology"`
	Indicator string  `json:"indicator"`
	Citation  string  `json:"citation"`
	Severity  string  `json:"severity"`
	Unit      string  `json:"unit"`
	Neutral   float64 `json:"neutral"`
}

// Inventory is the typology-to-feature mapping the model is built on. The order
// is the coordinate order of the model space and is therefore part of the
// model's identity: adding, removing or reordering an entry invalidates learned
// state, which Digest enforces.
//
// Two properties are deliberate throughout. Every feature is dimensionless, so
// no coordinate carries a currency or a count that would have to be rescaled per
// tenant. And every ratio is measured against the entity's own history rather
// than a fixed limit, because the statutory test is consistency with what is
// known about THIS customer, not comparison to a global number. The density
// model over those ratios is what supplies the second axis a supervisor asks
// about — how unusual a given deviation is among the institution's customers —
// so intra-customer deviation and peer-group comparison come out of one
// mechanism rather than two.
func Inventory() [Dims]Feature {
	return [Dims]Feature{{
		Name:      "amount",
		Window:    "30d",
		Typology:  "unusually large transaction",
		Indicator: "a transaction unusually large relative to the customer's own pattern",
		Citation:  "Directive (EU) 2018/843, Art. 1(10)(b) replacing Art. 18(2) AMLD4; EBA/GL/2021/02, Guideline 4.60(a)",
		Severity:  types.SeverityHigh,
		Unit:      "multiples of the customer's own mean transaction value",
		Neutral:   0.5,
	}, {
		Name:      "count",
		Window:    "24h",
		Typology:  "unusual pattern of activity",
		Indicator: "deviation in the frequency of transactions",
		Citation:  "Directive (EU) 2018/843, Art. 1(10)(b) replacing Art. 18(2) AMLD4; EBA/GL/2021/02, Guideline 4.60(a)",
		Severity:  types.SeverityMedium,
		Unit:      "multiples of the customer's own transactions per active day",
		Neutral:   0.5,
	}, {
		Name:      "volume",
		Window:    "24h",
		Typology:  "unusually large aggregate",
		Indicator: "deviation in amount taken over a window rather than per transaction",
		Citation:  "Directive (EU) 2018/843, Art. 1(10)(b) replacing Art. 18(2) AMLD4; Regulation (EU) 2024/1624, Art. 69(2)",
		Severity:  types.SeverityHigh,
		Unit:      "multiples of the customer's own value moved per active day",
		Neutral:   0.5,
	}, {
		Name:      "burst",
		Window:    "1h",
		Typology:  "unusual pattern of activity",
		Indicator: "successive transactions with no obvious economic rationale",
		Citation:  "EBA/GL/2021/02, Guideline 4.60(a)",
		Severity:  types.SeverityMedium,
		Unit:      "share of the day's transactions falling in one hour",
		Neutral:   0,
	}, {
		Name:      "subthreshold",
		Window:    "7d",
		Typology:  "structuring",
		Indicator: "transactions split to circumvent reporting limits",
		Citation:  "EBA/GL/2021/02, Guideline 4.60(a); Regulation (EU) 2024/1624, Art. 19(9)",
		Severity:  types.SeverityHigh,
		Unit:      "share of transactions falling just below the reporting threshold",
		Neutral:   0,
	}, {
		Name:      "subvalue",
		Window:    "7d",
		Typology:  "structuring",
		Indicator: "the link between several transactions kept below a reporting limit",
		Citation:  "Regulation (EU) 2024/1624, Art. 69(2); EBA/GL/2021/02, Guideline 4.60(a)",
		Severity:  types.SeverityHigh,
		Unit:      "share of value moved just below the reporting threshold",
		Neutral:   0,
	}, {
		Name:      "counterparty",
		Window:    "30d",
		Typology:  "unfamiliar counterparty",
		Indicator: "the origin and destination of the funds",
		Citation:  "Regulation (EU) 2024/1624, Art. 34(2); Regulation (EU) 2024/1624, Art. 69(2)",
		Severity:  types.SeverityMedium,
		Unit:      "prior transactions between this customer and this counterparty",
		Neutral:   0,
	}, {
		Name:      "device",
		Window:    "24h",
		Typology:  "network of connected persons",
		Indicator: "activity across a network of connected persons rather than one customer",
		Citation:  "JMLSG Guidance Part I, para 5.7.5",
		Severity:  types.SeverityMedium,
		Unit:      "ratio of the device's transactions to this account's",
		Neutral:   0.5,
	}, {
		Name:      "novelty",
		Window:    "30d",
		Typology:  "no established pattern",
		Indicator: "activity with no prior pattern for it to be consistent with",
		Citation:  "MLR 2017 (SI 2017/692), reg. 28(11)(a)-(b); Regulation (EU) 2024/1624, Art. 26(1)",
		Severity:  types.SeverityLow,
		Unit:      "prior transactions by this account in the window",
		Neutral:   0,
	}}
}

// Aggregation axes. A deviation is meaningless without saying whose: the axis is
// what the sliding aggregates are grouped by. The pair axis is the customer and
// one counterparty together, because "unfamiliar" is a fact about the
// relationship and not about either party alone. The device axis is what
// surfaces several nominally unrelated customers acting as one.
const (
	AxisAccount = "account"
	AxisPair    = "pair"
	AxisDevice  = "device"
)

// Keys returns the aggregate keys a transaction contributes to, and is the one
// definition of those keys: the ingest path records to exactly these and the
// model reads exactly these, so a key can never be written on one axis and read
// on another. Axes whose identifier the transaction does not carry are omitted
// rather than keyed on the empty string, which would pool every anonymous
// transaction in the tenant into one aggregate.
func Keys(tx types.Transaction) []velocity.Key {
	keys := make([]velocity.Key, 0, 3)
	if v := account(tx); v != "" {
		keys = append(keys, velocity.Key{OrgID: tx.OrgID, Kind: AxisAccount, Value: v})
	}
	if tx.Counterparty != "" && account(tx) != "" {
		keys = append(keys, velocity.Key{OrgID: tx.OrgID, Kind: AxisPair, Value: account(tx) + "\x1f" + tx.Counterparty})
	}
	if tx.DeviceFingerprint != "" {
		keys = append(keys, velocity.Key{OrgID: tx.OrgID, Kind: AxisDevice, Value: tx.DeviceFingerprint})
	}
	return keys
}

// account is the identifier the customer's own aggregates are kept under,
// falling back to the user when no account is carried. Monitoring is required at
// several levels of aggregation and this is the lowest one the transaction
// always identifies.
func account(tx types.Transaction) string {
	if tx.AccountID != "" {
		return tx.AccountID
	}
	return tx.UserID
}

// Point is one transaction expressed in the model's space, carrying alongside
// each coordinate the number it was computed from and the baseline it was
// measured against. Those two are not diagnostics — they are what the alert
// quotes, so the explanation is the same arithmetic the score came from rather
// than a second story told about it.
type Point struct {
	X    [Dims]float64
	Raw  [Dims]float64
	Base [Dims]float64
	// Blind marks a coordinate that could not be computed from the data
	// available and took its neutral value instead. A model silently reading
	// neutral for a dimension it never has data for is indistinguishable from a
	// model reading a genuine absence of risk, so the difference is counted and
	// reported.
	Blind [Dims]bool
}

// project maps one transaction's aggregates into the model space.
//
// It is a pure function of the aggregates: the caller owns the store and the
// ordering, so every feature here is an ordinary testable expression over
// numbers. usd is the transaction's value normalised at ingest, which is also
// what the aggregates hold, so a ratio between them carries no exchange-rate
// assumption.
func project(usd float64, acct, pair, device []velocity.Observation) Point {
	var p Point
	inv := Inventory()

	day := pick(acct, "24h")
	hour := pick(acct, "1h")
	week := pick(acct, "7d")
	month := pick(acct, "30d")

	// amount: this transaction against what this customer moves in an average
	// transaction, on the days it is active. Dividing instead by thirty calendar
	// days would manufacture a deviation every time an occasional customer
	// transacts at all.
	perTx := over(month.Sum-usd, month.Count-1)
	p.set(0, ratio(usd, perTx), usd, perTx, perTx <= 0, inv)

	// count: today's transactions against this customer's own rate per active
	// day. The transaction being scored is removed from the baseline so it
	// cannot raise the bar it is measured against.
	rate := over(float64(month.Count-1), month.Days)
	p.set(1, ratio(float64(day.Count), rate), float64(day.Count), rate, rate <= 0, inv)

	// volume: today's value against this customer's own value per active day.
	// Separate from count because the same aggregate reached by more payments
	// and by larger ones are different typologies.
	flow := over(month.Sum-usd, month.Days)
	p.set(2, ratio(day.Sum, flow), day.Sum, flow, flow <= 0, inv)

	// burst: concentration of the day's activity into this hour. Undefined below
	// two transactions in the day — one transaction is not a burst, and reading
	// it as complete concentration would flag every quiet customer.
	burst := 0.0
	blind := day.Count < 2
	if !blind {
		burst = share(float64(hour.Count), float64(day.Count))
	}
	p.set(3, burst, float64(hour.Count), float64(day.Count), blind, inv)

	// subthreshold and subvalue: the structuring pair. The first is how much of
	// the activity sits just under the reporting limit, the second how much of
	// the money does. Both are needed because one payment under the limit among
	// fifty is not the pattern, and fifty of them is nothing else.
	p.set(4, share(float64(week.Near), float64(week.Count)), float64(week.Near), float64(week.Count), week.Count == 0, inv)
	p.set(5, share(week.NearSum, week.Sum), week.NearSum, week.Sum, week.Sum <= 0, inv)

	// counterparty: how unfamiliar this counterparty is to this customer. Prior
	// dealings decay the signal hyperbolically — the first repeat matters far
	// more than the fortieth.
	prior := float64(pick(pair, "30d").Count - 1)
	if prior < 0 {
		prior = 0
	}
	p.set(6, 1/(1+prior), prior, 0, len(pair) == 0, inv)

	// device: whether the device is transacting for more accounts than this one,
	// or this account is spread across more devices than usual. Both directions
	// matter, so the ratio is two-sided around its neutral value.
	dev := pick(device, "24h")
	p.set(7, ratio(float64(dev.Count), float64(day.Count)), float64(dev.Count), float64(day.Count), len(device) == 0 || day.Count == 0, inv)

	// novelty: how little history this account has. It is what lets the model
	// join newness to size, which is a different finding from either alone.
	seen := float64(month.Count - 1)
	if seen < 0 {
		seen = 0
	}
	p.set(8, 1/(1+seen), seen, 0, false, inv)

	return p
}

// set writes one coordinate, substituting the feature's neutral value where the
// data could not support it.
//
// It substitutes for a KNOWN absence — no baseline yet, too few transactions to
// speak of a share — and marks the coordinate blind. It does not repair a
// coordinate that arrived as something other than a number in [0,1]: that would
// be an arithmetic path nobody anticipated, and quietly rewriting it to neutral
// would score a broken point as ordinary. Point.usable is the single place that
// decides what the trees will accept, and an unusable point is refused there.
func (p *Point) set(i int, x, raw, base float64, blind bool, inv [Dims]Feature) {
	if blind {
		x = inv[i].Neutral
	}
	p.X[i], p.Raw[i], p.Base[i], p.Blind[i] = x, raw, base, blind
}

// pick returns the observation for a named window, or the zero observation when
// the store does not keep it.
func pick(obs []velocity.Observation, window string) velocity.Observation {
	for _, o := range obs {
		if o.Window == window {
			return o
		}
	}
	return velocity.Observation{}
}

// over is a mean that reports no baseline rather than an infinite one when there
// is nothing to average over.
func over(total float64, n int) float64 {
	if n <= 0 || total <= 0 {
		return 0
	}
	return total / float64(n)
}

// ratio maps a value measured against a baseline onto [0,1], putting the
// baseline itself exactly at the centre.
//
// It is r/(1+r) applied to the value's multiple of its baseline, so the model
// space reads symmetrically in both directions: the customer's own average sits
// at 0.5, nine times it at 0.9, a ninth of it at 0.1. Normality at the centre is
// what makes a coordinate legible on its own and gives the counterfactual in
// Store.explain a value to substitute that means something.
//
// The multiple comes from velocity.Deviation, which answers zero when there is
// no baseline to compare against. A first transaction therefore reads as exactly
// average rather than as infinitely unusual: a new customer is not suspicious
// for being new.
func ratio(value, baseline float64) float64 {
	r := 1 + velocity.Deviation(value, baseline)
	switch {
	case math.IsNaN(r), r <= 0:
		return 0
	case math.IsInf(r, 1):
		return 1
	}
	return r / (1 + r)
}

// share is a fraction of a whole, clamped and safe on an empty whole.
func share(part, whole float64) float64 {
	if !(whole > 0) || !(part > 0) {
		return 0
	}
	if part >= whole {
		return 1
	}
	return part / whole
}

// usable reports whether a point can be fed to the trees.
//
// A coordinate outside the unit cube or not a number would corrupt every
// subsequent score, because the masses it lands on are shared with every other
// point: one poisoned update is not a bad answer for one transaction, it is a
// quietly wrong model from then on. So an unusable point is neither scored nor
// learned, and the refusal is counted.
func (p Point) usable() bool {
	for _, v := range p.X {
		if !finite(v) || v < 0 || v > 1 {
			return false
		}
	}
	return true
}

// far is the coordinate furthest from its neutral value, which is the feature to
// name when a point has to be described in one word.
func (p Point) far(inv [Dims]Feature) int {
	best, at := -1.0, 0
	for i := range p.X {
		if d := math.Abs(p.X[i] - inv[i].Neutral); d > best {
			best, at = d, i
		}
	}
	return at
}
