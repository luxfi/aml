package reason

import (
	"strings"
	"testing"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/types"
)

// The vocabulary is derived from the model, so it must cover exactly the model
// and nothing else. A code for a feature that is not there is a reason nothing
// can cite; a feature with no code is an alert nobody can be told the reason for.
func TestVocabularyCoversExactlyTheModel(t *testing.T) {
	inv := anomaly.Inventory()
	seen := map[string]int{}
	for _, c := range Codes() {
		if c.Source != "model" {
			continue
		}
		feature, _, ok := strings.Cut(c.Code, ".")
		if !ok {
			t.Fatalf("model code %q is not <feature>.<direction>", c.Code)
		}
		seen[feature]++
	}
	if len(seen) != anomaly.Dims {
		t.Fatalf("vocabulary names %d features, the model reads %d", len(seen), anomaly.Dims)
	}
	for _, f := range inv {
		want := 1
		if f.Neutral > 0 {
			want = 2
		}
		if seen[f.Name] != want {
			t.Errorf("feature %q (neutral %g): %d codes, want %d", f.Name, f.Neutral, seen[f.Name], want)
		}
	}
}

// A one-sided feature cannot go below its neutral of zero, so publishing a
// "below" code for it would be a code nothing can ever reach.
func TestNoUnreachableCode(t *testing.T) {
	neutral := map[string]float64{}
	for _, f := range anomaly.Inventory() {
		neutral[f.Name] = f.Neutral
	}
	for _, c := range Codes() {
		if c.Source != "model" {
			continue
		}
		feature, direction, _ := strings.Cut(c.Code, ".")
		if direction == Below && neutral[feature] == 0 {
			t.Errorf("%q is unreachable: %q is neutral at zero", c.Code, feature)
		}
	}
}

// Every code carries the provenance a reviewer asks for. A reason with no
// citation is an assertion.
func TestEveryModelCodeCarriesProvenance(t *testing.T) {
	for _, c := range Codes() {
		if c.Source != "model" {
			continue
		}
		if c.Typology == "" || c.Indicator == "" || c.Citation == "" || c.Severity == "" {
			t.Errorf("%q is missing provenance: %+v", c.Code, c)
		}
		if c.Says == "" {
			t.Errorf("%q has no sentence to show anyone", c.Code)
		}
	}
}

// THE reason-code test. Rank must emit only codes the vocabulary publishes, must
// order them by contribution, and must read the direction off the arithmetic the
// model actually did.
func TestRankEmitsOnlyPublishedCodesStrongestFirst(t *testing.T) {
	causes := []types.Cause{
		{Feature: "novelty", Observed: 0, Baseline: 0, Share: 0.10},
		{Feature: "amount", Observed: 9000, Baseline: 300, Share: 0.55},
		{Feature: "count", Observed: 1, Baseline: 12, Share: 0.35},
		{Feature: "nonesuch", Observed: 1, Baseline: 0, Share: 0.99},
	}
	got := Rank(causes, 0)

	if len(got) != 3 {
		t.Fatalf("ranked %d reasons, want 3 (the unknown feature must be dropped, not invented)", len(got))
	}
	for _, r := range got {
		if !Known(r.Code) {
			t.Errorf("%q is not in the vocabulary", r.Code)
		}
		if r.Says == "" {
			t.Errorf("%q has no sentence", r.Code)
		}
	}
	if got[0].Code != "amount.above" {
		t.Errorf("strongest is %q, want amount.above", got[0].Code)
	}
	// count is a ratio feature (neutral 0.5) and the observation is BELOW the
	// baseline, so the finding is "lower than this subject's own pattern".
	if got[1].Code != "count.below" {
		t.Errorf("second is %q, want count.below — observed %g against baseline %g", got[1].Code, 1.0, 12.0)
	}
	if got[2].Code != "novelty.above" {
		t.Errorf("third is %q, want novelty.above", got[2].Code)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Weight < got[i].Weight {
			t.Errorf("reasons are not strongest first: %v", got)
		}
	}
	if !strings.Contains(got[0].Says, "9000") {
		t.Errorf("the sentence does not carry this decision's own numbers: %q", got[0].Says)
	}
}

// Reg B asks for the PRINCIPAL reasons and expressly does not ask for all of
// them. A cap that did not cap would put nine reasons in an adverse-action
// notice, which is not an explanation.
func TestRankCaps(t *testing.T) {
	var causes []types.Cause
	for i, f := range anomaly.Inventory() {
		causes = append(causes, types.Cause{Feature: f.Name, Observed: float64(i + 2), Baseline: 1, Share: float64(9-i) / 45})
	}
	if got := Rank(causes, 4); len(got) != 4 {
		t.Fatalf("cap of 4 returned %d", len(got))
	}
	if got := Rank(causes, 0); len(got) != anomaly.Dims {
		t.Fatalf("no cap returned %d, want %d", len(got), anomaly.Dims)
	}
}

// A reason minted for every rule a tenant writes would make the vocabulary
// uncountable, which is the property that makes it defensible. The rule goes in
// Source; the code stays one.
func TestOfRuleKeepsTheVocabularyClosed(t *testing.T) {
	a := OfRule("rule_7f3", "velocity over 24h on this card", types.SeverityHigh, 0.4)
	b := OfRule("rule_ab1", "a denied address", types.SeverityCritical, 0.9)
	if a.Code != Rule || b.Code != Rule {
		t.Errorf("two rules minted two codes: %q and %q", a.Code, b.Code)
	}
	if a.Source != "rule_7f3" || b.Source != "rule_ab1" {
		t.Errorf("the rule id did not reach Source: %q %q", a.Source, b.Source)
	}
	if !Known(a.Code) {
		t.Errorf("%q is not in the vocabulary", a.Code)
	}
	if a.Severity != types.SeverityHigh {
		t.Errorf("severity is %q, want the rule's own", a.Severity)
	}
	if got := OfRule("rule_x", "", "", 0.1); got.Says != "rule_x" || got.Severity == "" {
		t.Errorf("a nameless rule produced an unshowable reason: %+v", got)
	}
}

// The vocabulary must publish nothing this plane cannot emit. Two sources: the
// model's own features, and a rule firing.
func TestVocabularyPublishesOnlyEmittableSources(t *testing.T) {
	sources := map[string]int{}
	for _, c := range Codes() {
		sources[c.Source]++
	}
	if len(sources) != 2 || sources["model"] == 0 || sources[Rule] != 1 {
		t.Fatalf("sources are %v, want exactly {model: many, rule: 1}", sources)
	}
}

// Every code the vocabulary publishes must be recognised by Known — otherwise a
// stored decision could cite a code the reader rejects.
func TestKnownAgreesWithCodes(t *testing.T) {
	for _, c := range Codes() {
		if !Known(c.Code) {
			t.Errorf("Codes publishes %q and Known rejects it", c.Code)
		}
	}
	if Known("amount.sideways") {
		t.Error("Known accepted a code Codes does not publish")
	}
}
