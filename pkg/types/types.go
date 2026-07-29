// Package types defines the canonical domain types for the AML engine.
// These types are the single source of truth for all AML operations.
package types

import (
	"encoding/json"
	"time"
)

// Severity levels for rules and alerts.
const (
	SeverityLow      = "low"
	SeverityMedium   = "medium"
	SeverityHigh     = "high"
	SeverityCritical = "critical"
)

// Action constants for rule outcomes and transaction responses.
const (
	ActionAllow  = "allow"
	ActionFlag   = "flag"
	ActionReview = "review"
	ActionBlock  = "block"
	ActionReport = "report"
)

// ActionCeiling is the strongest action that evidence from a statistical model
// may ask for: it escalates to a person, and does not block a payment or file a
// report on its own.
//
// Two reasons, both external to this codebase. A risk score must be overridable
// by a person with a documented rationale, which only means something if the
// score has not already acted. And the weighting behind a decision must not be
// driven by a single factor, which a model's output would be if it could act
// alone.
const ActionCeiling = ActionReview

// ActionRank orders actions by how much they demand, so that "the strongest of
// these" and "no stronger than this" are one comparison rather than two tables.
// An unknown action ranks lowest: an action nobody defined cannot be allowed to
// outrank one that was.
func ActionRank(action string) int {
	switch action {
	case ActionBlock:
		return 4
	case ActionReport:
		return 3
	case ActionReview:
		return 2
	case ActionFlag:
		return 1
	default:
		return 0
	}
}

// Case statuses.
const (
	CaseOpen      = "open"
	CaseInReview  = "in_review"
	CaseEscalated = "escalated"
	CaseClosed    = "closed"
)

// Case resolutions.
const (
	ResolutionCleared       = "cleared"
	ResolutionSARFiled      = "sar_filed"
	ResolutionAccountFrozen = "account_frozen"
	ResolutionFalsePositive = "false_positive"
)

// Alert decisions.
const (
	DecisionApproved  = "approved"
	DecisionRejected  = "rejected"
	DecisionEscalated = "escalated"
)

// Entity types.
const (
	EntityUser         = "user"
	EntityAccount      = "account"
	EntityCounterparty = "counterparty"
	EntityBankAccount  = "bank_account"
)

// Case event kinds.
const (
	EventNote         = "note"
	EventStatusChange = "status_change"
	EventFile         = "file"
	EventWebhook      = "webhook_event"
)

// Sanctions list sources.
const (
	ListOFACSDN  = "ofac_sdn"
	ListUN       = "un"
	ListEU       = "eu"
	ListHMT      = "hmt"
	ListInterpol = "interpol"
)

// Sanctions entry types.
const (
	SanctionIndividual = "individual"
	SanctionEntity     = "entity"
	SanctionVessel     = "vessel"
	SanctionAircraft   = "aircraft"
)

// Webhook event types.
const (
	WebhookAMLFlagged    = "aml.flagged"
	WebhookAMLCleared    = "aml.cleared"
	WebhookCaseOpened    = "case.opened"
	WebhookCaseClosed    = "case.closed"
	WebhookKYCApproved   = "kyc.approved"
	WebhookTradeExecuted = "trade.executed"
)

// Transaction is an incoming transaction event for AML evaluation.
type Transaction struct {
	ID         string  `json:"id"`
	OrgID      string  `json:"org_id"`
	TenantID   string  `json:"tenant_id,omitempty"`
	Source     string  `json:"source"`
	UserID     string  `json:"user_id"`
	AccountID  string  `json:"account_id,omitempty"`
	Symbol     string  `json:"symbol,omitempty"`
	AssetClass string  `json:"asset_class,omitempty"`
	Side       string  `json:"side,omitempty"`
	Qty        float64 `json:"qty"`
	Notional   float64 `json:"notional"`
	Currency   string  `json:"currency"`
	// USD is Notional converted at ingest, and it is set by the engine on the
	// way in — a value supplied by a client is overwritten.
	//
	// It is frozen at ingest rather than converted on read for two reasons. A
	// threshold applied today over a window of past transactions must not move
	// because a rate moved, or the same history answers a reporting question
	// differently on two days. And every aggregate and every ratio computed
	// downstream is then in one unit, so no comparison carries a silent
	// assumption about which currency it is in.
	USD               float64         `json:"usd"`
	Counterparty      string          `json:"counterparty,omitempty"`
	IPAddress         string          `json:"ip_address,omitempty"`
	DeviceFingerprint string          `json:"device_fingerprint,omitempty"`
	Timestamp         time.Time       `json:"timestamp"`
	Raw               json.RawMessage `json:"raw,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

// Entity is a normalized actor (user, account, counterparty).
type Entity struct {
	ID            string    `json:"id"`
	OrgID         string    `json:"org_id"`
	EntityType    string    `json:"entity_type"`
	ExternalID    string    `json:"external_id,omitempty"`
	Name          string    `json:"name"`
	Jurisdiction  string    `json:"jurisdiction,omitempty"`
	KYCLevel      int       `json:"kyc_level"`
	PEP           bool      `json:"pep"`
	SanctionsFlag bool      `json:"sanctions_flag"`
	RiskScore     float64   `json:"risk_score"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Rule is an AML evaluation rule using expr-lang DSL.
type Rule struct {
	ID                 string    `json:"id"`
	OrgID              string    `json:"org_id"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	DSL                string    `json:"dsl"`
	Severity           string    `json:"severity"`
	Weight             float64   `json:"weight"`
	Action             string    `json:"action"`
	Enabled            bool      `json:"enabled"`
	JurisdictionFilter []string  `json:"jurisdiction_filter,omitempty"`
	AssetClassFilter   []string  `json:"asset_class_filter,omitempty"`
	Priority           int       `json:"priority"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Alert is a rule hit — one per transaction per rule.
type Alert struct {
	ID             string             `json:"id"`
	OrgID          string             `json:"org_id"`
	TxID           string             `json:"tx_id"`
	RuleID         string             `json:"rule_id"`
	RuleName       string             `json:"rule_name"`
	Severity       string             `json:"severity"`
	Score          float64            `json:"score"`
	ScoreBreakdown map[string]float64 `json:"score_breakdown,omitempty"`
	// Causes carries the per-feature explanation when a model raised this
	// alert. Empty for a rule alert, whose expression is its explanation.
	Causes      []Cause    `json:"causes,omitempty"`
	ActionTaken string     `json:"action_taken"`
	ReviewedBy  string     `json:"reviewed_by,omitempty"`
	ReviewedAt  *time.Time `json:"reviewed_at,omitempty"`
	Decision    string     `json:"decision,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Case is a human review case.
type Case struct {
	ID         string     `json:"id"`
	OrgID      string     `json:"org_id"`
	Number     int64      `json:"number"`
	Status     string     `json:"status"`
	Severity   string     `json:"severity"`
	EntityIDs  []string   `json:"entity_ids,omitempty"`
	AlertIDs   []string   `json:"alert_ids,omitempty"`
	AssigneeID string     `json:"assignee_id,omitempty"`
	OpenedAt   time.Time  `json:"opened_at"`
	ClosedAt   *time.Time `json:"closed_at,omitempty"`
	Resolution string     `json:"resolution,omitempty"`
	// Assessment is the retained Art. 69(2) decision that closed the case — the
	// information considered, the result and the reasons. A closed case has one.
	Assessment string    `json:"assessment,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CaseEvent is a case timeline entry.
type CaseEvent struct {
	ID        string    `json:"id"`
	CaseID    string    `json:"case_id"`
	AuthorID  string    `json:"author_id"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body,omitempty"`
	FilePath  string    `json:"file_path,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// SanctionsList is list metadata.
type SanctionsList struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	URL       string    `json:"url"`
	Format    string    `json:"format"`
	FetchedAt time.Time `json:"fetched_at"`
	SHA256    string    `json:"sha256"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SanctionsEntry is a flattened sanctions list entry.
type SanctionsEntry struct {
	ID          string          `json:"id"`
	ListID      string          `json:"list_id"`
	RefID       string          `json:"ref_id"`
	Name        string          `json:"name"`
	Aliases     []string        `json:"aliases,omitempty"`
	DOB         string          `json:"dob,omitempty"`
	Nationality string          `json:"nationality,omitempty"`
	Address     string          `json:"address,omitempty"`
	Type        string          `json:"type"`
	Raw         json.RawMessage `json:"raw,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Webhook is a subscriber configuration.
type Webhook struct {
	ID             string     `json:"id"`
	OrgID          string     `json:"org_id"`
	URL            string     `json:"url"`
	Secret         string     `json:"secret"`
	Events         []string   `json:"events"`
	Enabled        bool       `json:"enabled"`
	LastDeliveryAt *time.Time `json:"last_delivery_at,omitempty"`
	FailureCount   int        `json:"failure_count"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Cause is one feature's contribution to an alert a model raised.
//
// Where a rule is its own explanation — the expression says what fired — a
// model's output is a number, and a number on its own cannot go to an
// investigator. What is owed instead is the features that influenced the output,
// the risk associated with each, and the risk indicators behind them, and a Cause
// is exactly that owed set for one feature.
//
// Observed and Baseline are the arithmetic the score came from, in the terms Unit
// names, so the explanation is the same calculation rather than a second account
// of it. Without is the score the model would have produced had this feature been
// unremarkable, holding everything else: it is a counterfactual on the model
// itself and it is what makes the contribution a measurement rather than an
// attribution scheme's opinion.
type Cause struct {
	Feature   string  `json:"feature"`
	Typology  string  `json:"typology"`
	Indicator string  `json:"indicator"`
	Citation  string  `json:"citation"`
	Severity  string  `json:"severity"`
	Unit      string  `json:"unit"`
	Observed  float64 `json:"observed"`
	Baseline  float64 `json:"baseline"`
	Without   float64 `json:"without"`
	// Share is this feature's part of the score, in [0,1]. Zero across every
	// cause means no single feature accounts for the alert and the combination
	// does; the causes are then ordered by how far each sits from unremarkable.
	Share float64 `json:"share"`
}

// RuleHit is the result of evaluating a single rule against a transaction.
type RuleHit struct {
	Rule    Rule
	Match   bool
	Score   float64
	EvalErr string // non-empty if the rule evaluation failed (fail-closed)
	// Causes explains a hit that came from a model rather than an expression.
	// Empty for rules, which explain themselves.
	Causes []Cause
}

// EvalContext is the evaluation context passed to expr-lang rules.
type EvalContext struct {
	Tx     Transaction `json:"tx"`
	Entity Entity      `json:"entity"`
}

// EvalResult is the synchronous response to a transaction ingestion.
type EvalResult struct {
	Action   string   `json:"action"`
	Score    float64  `json:"score"`
	AlertIDs []string `json:"alert_ids"`
	CaseID   string   `json:"case_id,omitempty"`
}
