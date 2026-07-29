// Package reference holds the published data a rule needs but cannot derive:
// currency conversion rates and jurisdiction risk listings.
//
// Both carry the date they were current at, and both refuse to answer rather than
// guess. That is the difference between a control and the appearance of one: a
// conversion table that passes an unknown currency through unchanged moves every
// threshold by the exchange rate, and a jurisdiction list that answers "not
// listed" because it was never loaded is indistinguishable from a clean world.
package reference

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Rates converts amounts into USD from a table current at a stated instant.
type Rates struct {
	// AsOf is when the table was current. It is required: a rate with no date
	// cannot be assessed for staleness, and a stale rate silently moves every
	// threshold expressed in USD.
	AsOf time.Time
	// USDPer maps an ISO 4217 code to the USD value of one unit.
	USDPer map[string]float64
	// MaxAge is how old the table may be before conversion fails. Zero means no
	// limit, which is appropriate only where every amount is already USD.
	MaxAge time.Duration
	// Now supplies the current instant; nil means the wall clock.
	Now func() time.Time
}

// USD converts an amount to USD.
//
// An unknown currency is an error. Returning the amount unchanged — the
// conservative-looking choice — is not conservative: a currency worth more than a
// dollar is understated, so 10,000 units of a currency worth three dollars is
// assessed as 10,000 and sits below a 10,000-dollar reporting threshold that it
// is in truth three times over.
func (r Rates) USD(_ context.Context, amount float64, currency string) (float64, error) {
	code := strings.ToUpper(strings.TrimSpace(currency))
	if code == "" || code == "USD" {
		return amount, nil
	}
	rate, ok := r.USDPer[code]
	if !ok {
		return 0, fmt.Errorf("reference: no rate for currency %q, so its USD value is unknown", code)
	}
	if rate <= 0 {
		return 0, fmt.Errorf("reference: rate for %q is %v, which is not a price", code, rate)
	}
	if r.MaxAge > 0 {
		if age := r.now().Sub(r.AsOf); age > r.MaxAge {
			return 0, fmt.Errorf("reference: rate table is %s old, older than the %s limit", age.Truncate(time.Hour), r.MaxAge)
		}
	}
	return amount * rate, nil
}

func (r Rates) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

// Jurisdictions is a published listing of higher-risk countries.
//
// The two tiers mirror what standard-setters publish: countries for which
// countermeasures are called for, and countries under increased monitoring. They
// are separate because the required response differs, and collapsing them into one
// "risky" flag loses the distinction a rule needs to choose between blocking and
// enhanced review.
//
// Membership is not compiled in. The lists change several times a year, a
// compiled-in copy is wrong within months of a release, and a wrong list in a
// compliance product is worse than an absent one because it is trusted. The
// operator loads the current lists and states the date they were current at.
type Jurisdictions struct {
	AsOf time.Time
	// Action holds the codes calling for countermeasures.
	Action []string
	// Monitoring holds the codes under increased monitoring.
	Monitoring []string
}

// Jurisdiction returns the tier for an ISO 3166-1 alpha-2 code: the empty string
// if the country is on neither list.
//
// It returns an error when no listing has been loaded. An empty listing would
// answer "not listed" for every country, which reads as a world with no high-risk
// jurisdictions in it, and no operator would notice.
func (j Jurisdictions) Jurisdiction(code string) (string, error) {
	if len(j.Action) == 0 && len(j.Monitoring) == 0 {
		return "", fmt.Errorf("reference: no jurisdiction listing loaded, so no country can be assessed")
	}
	if j.AsOf.IsZero() {
		return "", fmt.Errorf("reference: jurisdiction listing has no date, so its currency cannot be assessed")
	}
	want := strings.ToUpper(strings.TrimSpace(code))
	if want == "" {
		return "", nil
	}
	for _, c := range j.Action {
		if strings.EqualFold(strings.TrimSpace(c), want) {
			return "action", nil
		}
	}
	for _, c := range j.Monitoring {
		if strings.EqualFold(strings.TrimSpace(c), want) {
			return "monitoring", nil
		}
	}
	return "", nil
}

// Age reports how old the listing is, for a readiness check.
func (j Jurisdictions) Age(now time.Time) time.Duration {
	if j.AsOf.IsZero() {
		return 0
	}
	return now.Sub(j.AsOf)
}
