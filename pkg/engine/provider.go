package engine

import (
	"context"
	"errors"
	"time"

	"github.com/luxfi/aml/pkg/history"
)

// Screening classes. Sanctions and PEP screening share one mechanism — match a
// name against a curated list — but they answer different questions and carry
// different consequences, so a rule names which it is asking.
const (
	ClassSanctions = "sanctions"
	ClassPEP       = "pep"
)

// Jurisdiction risk tiers, mirroring the two lists a standard-setter publishes:
// jurisdictions calling for countermeasures, and jurisdictions under increased
// monitoring. A rule distinguishes them because the required response differs.
const (
	TierNone       = ""
	TierMonitoring = "monitoring"
	TierAction     = "action"
)

// Hit is the outcome of screening a name against a list.
type Hit struct {
	Matched bool    `json:"matched"`
	Score   float64 `json:"score"`
	List    string  `json:"list,omitempty"`
	EntryID string  `json:"entry_id,omitempty"`
	Name    string  `json:"name,omitempty"`
}

// Screen matches a name against a screening list.
type Screen interface {
	Hit(ctx context.Context, name, class string) (Hit, error)
}

// Reference answers questions about published reference data.
//
// Jurisdiction returns the risk tier for an ISO 3166-1 alpha-2 country code. It
// returns an error when no list has been loaded: an empty list would answer
// "not listed" for every country on earth, which is the same silent no-op as
// having no control at all.
type Reference interface {
	Jurisdiction(code string) (string, error)
}

// Lists answers whether a value is on one of the institution's own allow or deny
// lists.
//
// The org is the tenant KEY the transaction under evaluation carries, so a rule
// can only ever read its own institution's lists — the same value that indexes
// every other record plane, passed through rather than resolved again here.
//
// It returns an error for a list nobody declared, and for an empty value, rather
// than reporting "not listed". Both would be a rule that is present in the
// catalog, visible in the interface, counted as coverage, and incapable of
// firing — which is the failure ErrNoProvider exists to prevent, arrived at from
// the data side instead of the configuration side.
type Lists interface {
	Listed(ctx context.Context, org, name, value string) (bool, error)
}

// Rate converts an amount into USD.
//
// It returns an error for a currency it does not know rather than passing the
// amount through unchanged. Treating an unknown amount as already-USD moves
// every threshold by the exchange rate: 10,000 units of a currency worth three
// dollars each is a 30,000-dollar transaction, and passing it through as 10,000
// leaves it exactly on the wrong side of a 10,000-dollar threshold.
type Rate interface {
	USD(ctx context.Context, amount float64, currency string) (float64, error)
}

// Providers are the evidence sources the rule vocabulary is built from.
//
// A rule may only be admitted if every provider its expression needs is
// present. Nothing here has a default: a missing provider is a configuration
// error surfaced when rules are installed, not a value invented at evaluation
// time.
type Providers struct {
	History   history.Store
	Screen    Screen
	Reference Reference
	Rate      Rate
	Lists     Lists

	// Now supplies the evaluation instant. Tests set it; production leaves it nil
	// and gets the wall clock.
	Now func() time.Time

	// Zone is the institution's business day, used for calendar-day aggregation.
	// Nil means UTC.
	Zone *time.Location
}

// capability names the evidence source a vocabulary term needs.
type capability string

const (
	capHistory   capability = "history"
	capScreen    capability = "screen"
	capReference capability = "reference"
	capRate      capability = "rate"
	capLists     capability = "lists"
)

// vocabulary maps each term a rule expression may call to the provider that
// answers it. A term absent from this table is not part of the language, and a
// term present here cannot be used unless its provider is configured.
//
// This table is the single place the language is defined. Adding a scope method
// without adding it here makes the method unreachable from a rule, which the
// vocabulary test detects.
var vocabulary = map[string]capability{
	"Count":      capHistory,
	"Sum":        capHistory,
	"Max":        capHistory,
	"Day":        capHistory,
	"Distinct":   capHistory,
	"Seen":       capHistory,
	"Structured": capHistory,
	"InOut":      capHistory,
	"Dormant":    capHistory,
	"Round":      capHistory,
	"Deviation":  capHistory,
	"Screened":   capScreen,
	"Tier":       capReference,
	"USD":        capRate,
	"Near":       capRate,
	"Listed":     capLists,
}

func (p Providers) has(c capability) bool {
	switch c {
	case capHistory:
		return p.History != nil
	case capScreen:
		return p.Screen != nil
	case capReference:
		return p.Reference != nil
	case capRate:
		return p.Rate != nil
	case capLists:
		return p.Lists != nil
	}
	return false
}

// ErrNoProvider reports a rule that names evidence the deployment cannot supply.
var ErrNoProvider = errors.New("no provider")

func (p Providers) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now().UTC()
}

func (p Providers) zone() *time.Location {
	if p.Zone != nil {
		return p.Zone
	}
	return time.UTC
}
