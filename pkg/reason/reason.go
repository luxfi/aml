// Package reason turns a model's attribution into the artefact an adverse
// decision is defended with: a small closed set of codes, ranked by how much
// each one moved the decision, each carrying a sentence a person can be shown.
//
// WHY A CLOSED VOCABULARY. A free-text reason cannot be counted, cannot be
// tested, and cannot be compared between two decisions or two tenants. Every
// regime that lets a person question a decision — Reg B §1002.9(b)(2) for
// credit, GDPR Art. 22(3) and Art. 13(2)(f) for automated processing, and the
// supervisory expectation behind every alert an investigator writes up — asks
// for the PRINCIPAL reasons, specifically and accurately. That is a ranked list
// over a fixed vocabulary, not a paragraph.
//
// WHY THE VOCABULARY IS DERIVED AND NOT DECLARED. The codes come from
// anomaly.Inventory() — the same nine features the model actually reads, in the
// same order, with the same neutral values. A second hand-written table of codes
// would be a second description of the model, and the two would drift the first
// time a feature was added. Codes() is a pure function of the inventory, so the
// vocabulary cannot describe a model that is not there and cannot omit one that
// is.
//
// WHY THE WEIGHTS ARE THE MODEL'S OWN. A Reason's weight is types.Cause.Share,
// which anomaly computes by moving one coordinate to its neutral value and
// rescoring on the same trees — an exact counterfactual on the model that
// decided, not a surrogate fitted afterwards. That is the whole reason the
// detector is a half-space tree and not a net: attributability was the
// selection criterion.
package reason

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/types"
)

// Direction says which way a feature moved away from unremarkable. It is part of
// the code because "much larger than this customer's norm" and "much smaller
// than this customer's norm" are different findings that would otherwise share
// one code and be uncountable apart.
const (
	// Above is a coordinate higher than the feature's neutral value.
	Above = "above"
	// Below is a coordinate lower than it. Only reachable for the features whose
	// neutral sits inside the unit interval; a feature neutral at zero has no
	// below.
	Below = "below"
)

// Sources that are not the model. Each is a decision a tenant or the platform
// made explicitly, so each is one code and the specific instrument is named in
// the reason's Source rather than minted as a new code — otherwise the
// vocabulary grows by one entry per rule a tenant writes and stops being
// countable.
const (
	// List is a value on one of the tenant's own allow or deny lists.
	List = "list"
	// Rule is one of the tenant's own written rules.
	Rule = "rule"
	// Control is a platform control standing on the subject: a reserve, a payout
	// hold, a block.
	Control = "control"
	// Agency is what kind of actor this was — undeclared automation reaching a
	// surface that expects a person, or the reverse.
	Agency = "agency"
)

// Code is one entry of the vocabulary.
type Code struct {
	// Code is the identifier a decision cites and a report counts. Stable: it is
	// part of the published contract, so renaming one invalidates every stored
	// decision that cites it.
	Code string `json:"code"`
	// Source is where a reason with this code comes from: a model feature, or
	// one of the four explicit instruments above.
	Source string `json:"source"`
	// Says is the sentence template a person is shown, with no numbers in it. A
	// Reason fills the numbers in.
	Says string `json:"says"`
	// Typology, Indicator and Citation are the model features' own provenance,
	// carried through unchanged from anomaly.Inventory so the published reason
	// and the model's own inventory cannot tell two different stories. Empty for
	// the four explicit sources, which cite the tenant's instrument instead.
	Typology  string `json:"typology,omitempty"`
	Indicator string `json:"indicator,omitempty"`
	Citation  string `json:"citation,omitempty"`
	// Severity is the grading a reason with this code carries.
	Severity string `json:"severity"`
}

// Reason is one code as it applies to one decision.
type Reason struct {
	// Code is from the vocabulary Codes() publishes. A code not in that set is a
	// bug, and Rank cannot produce one: every code it emits is built by the same
	// two functions that build the vocabulary.
	Code string `json:"code"`
	// Weight is this reason's part of the decision, in [0,1]. For a model reason
	// it is the exact counterfactual share; for an explicit instrument it is the
	// weight that instrument carried.
	Weight float64 `json:"weight"`
	// Says is the sentence shown to an investigator or to a declined customer,
	// with this decision's own numbers in it.
	Says string `json:"says"`
	// Source names the specific instrument, when the code does not: the rule id,
	// the list name, the control id. Empty for a model feature, whose code
	// already names it exactly.
	Source string `json:"source,omitempty"`
	// Severity is the grading.
	Severity string `json:"severity,omitempty"`
}

// codeOf renders the identifier for one feature and direction. It is the ONE
// place the code's spelling is decided, so Codes and Rank cannot disagree about
// what a code looks like.
func codeOf(feature, direction string) string { return feature + "." + direction }

// Codes is the whole vocabulary, in a stable order: every reachable model code
// first, in the model's own coordinate order, then the four explicit sources.
//
// Reachability is read off the feature's neutral value rather than listed:
// a feature neutral at zero is a share or a count that cannot go below it, so
// there is no "below" finding to report and publishing one would be a code
// nothing can ever cite. Deriving it means adding a tenth feature adds its codes
// automatically and correctly.
func Codes() []Code {
	inv := anomaly.Inventory()
	out := make([]Code, 0, 2*anomaly.Dims+4)
	for _, f := range inv {
		out = append(out, feature(f, Above))
		if f.Neutral > 0 {
			out = append(out, feature(f, Below))
		}
	}
	return append(out,
		Code{Code: List, Source: List, Severity: types.SeverityHigh,
			Says: "a value on one of this organisation's own lists"},
		Code{Code: Rule, Source: Rule, Severity: types.SeverityMedium,
			Says: "one of this organisation's own rules"},
		Code{Code: Control, Source: Control, Severity: types.SeverityHigh,
			Says: "a control standing on this subject"},
		Code{Code: Agency, Source: Agency, Severity: types.SeverityMedium,
			Says: "the kind of actor this request came from"},
	)
}

// feature renders one model code from one inventory entry.
func feature(f anomaly.Feature, direction string) Code {
	return Code{
		Code:      codeOf(f.Name, direction),
		Source:    "model",
		Says:      says(f, direction),
		Typology:  f.Typology,
		Indicator: f.Indicator,
		Citation:  f.Citation,
		Severity:  f.Severity,
	}
}

// says renders the sentence for one feature and direction, with no numbers.
func says(f anomaly.Feature, direction string) string {
	word := "higher than"
	if direction == Below {
		word = "lower than"
	}
	return f.Indicator + " — " + word + " this subject's own pattern, measured in " + f.Unit
}

// Known reports whether a code is in the vocabulary. It is what a caller that
// stored a code and read it back later checks before showing it, and what a test
// asserts over every reason a decision produced.
func Known(code string) bool {
	for _, c := range Codes() {
		if c.Code == code {
			return true
		}
	}
	return false
}

// Rank turns a model's attribution into the reasons a decision cites, strongest
// first.
//
// The direction is read from the coordinate the model actually used, not from
// the raw observation: a coordinate is where the model was, and the raw number
// is what it was computed from. Cause carries Observed and Baseline, and the
// direction is decided by comparing them the way the feature's own projection
// does — a ratio feature is above when the observation exceeds the baseline, and
// a share feature (neutral zero) can only ever be above.
//
// top bounds how many reasons come back. Reg B asks for the principal reasons
// and expressly does not ask for all of them; a list of nine is not an
// explanation. Zero means the caller wants them all.
func Rank(causes []types.Cause, top int) []Reason {
	inv := anomaly.Inventory()
	byName := map[string]anomaly.Feature{}
	for _, f := range inv {
		byName[f.Name] = f
	}

	out := make([]Reason, 0, len(causes))
	for _, c := range causes {
		f, ok := byName[c.Feature]
		if !ok {
			// A cause naming a feature the model does not have cannot be turned
			// into a code in the vocabulary, and inventing one would publish a
			// reason nothing can count. Dropped, and the drop is visible as a
			// reason list shorter than the cause list.
			continue
		}
		dir := Above
		if f.Neutral > 0 && c.Observed < c.Baseline {
			dir = Below
		}
		out = append(out, Reason{
			Code:     codeOf(f.Name, dir),
			Weight:   c.Share,
			Says:     sentence(f, dir, c),
			Severity: f.Severity,
		})
	}

	// Stable by weight so that two reasons contributing equally keep the order
	// the model gave them — which is its own ordering by distance from neutral
	// when no single feature accounts for the score.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Weight > out[j].Weight })
	if top > 0 && len(out) > top {
		out = out[:top]
	}
	return out
}

// sentence renders one reason with this decision's own numbers.
func sentence(f anomaly.Feature, direction string, c types.Cause) string {
	var b strings.Builder
	b.WriteString(f.Indicator)
	b.WriteString(" — ")
	if direction == Below {
		b.WriteString("lower")
	} else {
		b.WriteString("higher")
	}
	b.WriteString(" than this subject's own pattern (observed ")
	b.WriteString(number(c.Observed))
	if c.Baseline != 0 {
		b.WriteString(" against ")
		b.WriteString(number(c.Baseline))
	}
	b.WriteString(" ")
	b.WriteString(f.Unit)
	b.WriteString(")")
	return b.String()
}

// number renders a quantity the way a person reads one: no exponent, no
// seventeen digits of float noise, and no trailing zeros.
func number(v float64) string {
	s := strconv.FormatFloat(v, 'f', 4, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// Explicit builds a reason for one of the four instruments that are not the
// model. The instrument is named in Source rather than in the code, so the
// vocabulary stays closed however many rules a tenant writes.
//
// It refuses an unknown source rather than minting a code: a reason cited under
// a code nothing publishes is exactly as undefendable as free text.
func Explicit(source, instrument, says string, weight float64) (Reason, error) {
	switch source {
	case List, Rule, Control, Agency:
	default:
		return Reason{}, fmt.Errorf("reason: %q is not a source in the vocabulary", source)
	}
	sev := types.SeverityMedium
	if source == List || source == Control {
		sev = types.SeverityHigh
	}
	if says == "" {
		says = instrument
	}
	return Reason{Code: source, Weight: weight, Says: says, Source: instrument, Severity: sev}, nil
}
