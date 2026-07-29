package reference

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// RatesFromEnv reads the currency table from AML_RATES.
//
// The value is a JSON object of ISO 4217 code to USD value per unit, with the date
// it was current at:
//
//	{"as_of":"2026-07-29T00:00:00Z","max_age":"48h","rates":{"EUR":1.08,"GBP":1.27}}
//
// There is no built-in table. A compiled-in rate is wrong within days and silently
// moves every threshold expressed in another currency; an absent rate is refused at
// evaluation and routed to review. Only the second is safe, and only the second is
// visible.
func RatesFromEnv() Rates {
	raw := os.Getenv("AML_RATES")
	if strings.TrimSpace(raw) == "" {
		return Rates{}
	}
	var in struct {
		AsOf   time.Time          `json:"as_of"`
		MaxAge string             `json:"max_age"`
		Rates  map[string]float64 `json:"rates"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return Rates{}
	}
	r := Rates{AsOf: in.AsOf, USDPer: in.Rates}
	if d, err := time.ParseDuration(in.MaxAge); err == nil {
		r.MaxAge = d
	}
	return r
}

// JurisdictionsFromEnv reads the higher-risk country listing from
// AML_JURISDICTIONS:
//
//	{"as_of":"2026-06-27T00:00:00Z","action":["KP","IR"],"monitoring":["SY"]}
//
// Membership changes several times a year, so it is loaded rather than compiled in,
// and an absent listing makes the jurisdiction rules fail loudly rather than report
// every country on earth as unlisted.
func JurisdictionsFromEnv() Jurisdictions {
	raw := os.Getenv("AML_JURISDICTIONS")
	if strings.TrimSpace(raw) == "" {
		return Jurisdictions{}
	}
	var in struct {
		AsOf       time.Time `json:"as_of"`
		Action     []string  `json:"action"`
		Monitoring []string  `json:"monitoring"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return Jurisdictions{}
	}
	return Jurisdictions{AsOf: in.AsOf, Action: in.Action, Monitoring: in.Monitoring}
}
