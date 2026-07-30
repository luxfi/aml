// Package api registers AML HTTP routes on a Base app.
//
// Every route resolves its tenant through the Handler's Identity and refuses the
// request if it cannot. Nothing here reads a tenant from the request directly.
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/cases"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/history"
	"github.com/luxfi/aml/pkg/replay"
	"github.com/luxfi/aml/pkg/retention"
	"github.com/luxfi/aml/pkg/sanctions"
	"github.com/luxfi/aml/pkg/screen"
	"github.com/luxfi/aml/pkg/token"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
)

// Handler wires AML engine + case store + sanctions to HTTP routes.
type Handler struct {
	// Identity resolves which tenant a request is authenticated to act on.
	// Without it no route will serve — see tenant.
	Identity Identity

	Engine *engine.Engine
	Cases  *cases.Store
	Alerts *AlertStore
	// Screen is the screening store the refresh fills and every endpoint reads.
	// There is deliberately no second store here: two of them is how sanctions
	// search came to answer "no match" for every name while designations were
	// being loaded somewhere else.
	Screen *screen.Store
	// History is the retained transaction history the aggregate rules read.
	History *history.Base
	// Rate converts an amount to USD, refusing an unknown currency rather than
	// passing it through — a threshold in USD is wrong by the exchange rate if
	// the conversion silently does nothing.
	Rate engine.Rate

	// Records is the retained record plane. Ingest refuses without it: a
	// transaction that cannot be recorded must not be processed.
	Records *retention.Ledger
	// Keys tokenises direct identifiers before they are retained, and opens
	// sealed records for the reads that are entitled to them.
	Keys *token.Keyring
	// Readiness reports which sanctions list is fit to screen against, with a
	// count and a date. Without it an empty list cannot be told from a clean party.
	Readiness *screen.Readiness

	// Velocity holds the sliding aggregates every behavioural measure reads.
	// Ingest is the only writer, so what an alert quotes and what the model
	// scored are the same numbers.
	Velocity *velocity.Store
	// Anomaly scores whether a transaction is unusual for the entity, alongside
	// the rules rather than instead of them. Nil runs on rules alone.
	Anomaly *anomaly.Store
	// Limit is the reporting limit a transaction is judged against, which is
	// what makes a payment sitting just under it visible as structuring rather
	// than as an ordinary payment. Zero falls back to reportLimit.
	//
	// It is one number because the aggregates are kept in one unit. The limit
	// that actually applies depends on the jurisdiction and the kind of
	// transaction, and resolving it per transaction needs an entity whose
	// jurisdiction is known — see the entity resolver above, which does not
	// have one yet.
	Limit float64
}

// reportLimit is the fallback reporting limit, in the unit the aggregates are
// kept in. EUR 10 000 for an occasional transaction under Regulation (EU)
// 2024/1624 Art. 19(1)(b), GBP 12 000 under MLR 2017 reg. 27(2), USD 10 000 for a
// currency transaction report — the same order of magnitude in each, which is why
// one fallback is defensible and a per-jurisdiction table is still owed.
const reportLimit = 10_000.0

func (h *Handler) limit() float64 {
	if h.Limit > 0 {
		return h.Limit
	}
	return reportLimit
}

// DefaultMaxAlerts is the default maximum number of transaction IDs in the alert store.
const DefaultMaxAlerts = 100_000

// AlertStore is a thread-safe in-memory alert store with LRU eviction.
// RED-02: sync.RWMutex for data-race safety.
// RED-08: LRU eviction prevents unbounded memory growth.
type AlertStore struct {
	mu       sync.RWMutex
	alerts   map[string][]types.Alert // keyed by tx_id
	order    []string                 // insertion order for LRU eviction
	maxItems int
}

// NewAlertStore creates an alert store with the default max capacity.
func NewAlertStore() *AlertStore {
	return NewAlertStoreWithMax(DefaultMaxAlerts)
}

// NewAlertStoreWithMax creates an alert store with the specified max capacity.
func NewAlertStoreWithMax(maxItems int) *AlertStore {
	if maxItems <= 0 {
		maxItems = DefaultMaxAlerts
	}
	return &AlertStore{
		alerts:   make(map[string][]types.Alert),
		maxItems: maxItems,
	}
}

// Add stores alerts for a transaction. Evicts oldest 10% when capacity is exceeded.
func (s *AlertStore) Add(txID string, alerts []types.Alert) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.alerts[txID]; !exists {
		s.order = append(s.order, txID)
	}
	s.alerts[txID] = append(s.alerts[txID], alerts...)

	if len(s.order) > s.maxItems {
		evictCount := s.maxItems / 10
		if evictCount < 1 {
			evictCount = 1
		}
		for i := 0; i < evictCount && i < len(s.order); i++ {
			delete(s.alerts, s.order[i])
		}
		s.order = s.order[evictCount:]
	}
}

// ByTx returns an org's alerts for a transaction. A transaction id is not a
// secret and the store is shared, so the org is what scopes the read: without it
// knowing an id in another org would be enough to read its alerts.
func (s *AlertStore) ByTx(org, txID string) []types.Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []types.Alert
	for _, a := range s.alerts[txID] {
		if a.OrgID == org {
			out = append(out, a)
		}
	}
	return out
}

// Len returns the number of transaction IDs in the store.
func (s *AlertStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.alerts)
}

// Register adds AML routes to a ServeEvent router.
// Called from within an OnServe hook.
func (h *Handler) Register(se *core.ServeEvent) {
	se.Router.POST("/v1/aml/transactions", h.ingestTransaction())
	se.Router.GET("/v1/aml/transactions/{id}/alerts", h.getAlerts())
	se.Router.GET("/v1/aml/cases", h.listCases())
	se.Router.POST("/v1/aml/cases/{id}/events", h.addCaseEvent())
	se.Router.POST("/v1/aml/cases/{id}/resolve", h.resolveCase())
	se.Router.GET("/v1/aml/rules", h.listRules())
	se.Router.POST("/v1/aml/rules/test", h.testRule())
	se.Router.GET("/v1/aml/anomaly", h.anomalyState())
	se.Router.POST("/v1/aml/anomaly/test", h.anomalyTest())
	se.Router.POST("/v1/aml/relationships", h.openRelationship())
	se.Router.POST("/v1/aml/relationships/{id}/close", h.closeRelationship())
	se.Router.POST("/v1/aml/relationships/search", h.searchRelationships())
	se.Router.POST("/v1/aml/sanctions/search", h.searchSanctions())
	se.Router.GET("/v1/aml/sanctions/sources", h.screeningSources())
	se.Router.GET("/v1/aml/catalog", h.catalog())
	se.Router.GET("/v1/aml/config", h.brandConfig())
	se.Router.GET("/v1/aml/health", h.health())
}

// refuse answers an unauthenticated request. The reason is logged for the
// operator and not returned, so a caller cannot probe for which tenants exist.
func refuse(e *core.RequestEvent, err error) error {
	log.Printf("[aml] unauthenticated request to %s: %v", e.Request.URL.Path, err)
	return fail(e, http.StatusUnauthorized, "unauthenticated")
}

func fail(e *core.RequestEvent, code int, message string) error {
	return e.JSON(code, map[string]string{"error": message})
}

// unavailable is the answer when the record plane cannot take a record. The
// reason is logged for the operator and not returned to the caller.
func unavailable(e *core.RequestEvent, what string, err error) error {
	log.Printf("[aml] %s: %v", what, err)
	return fail(e, http.StatusServiceUnavailable, "record plane unavailable")
}

// health reports whether this instance can do its job, which is not the same as
// whether it is running: an instance that cannot record a transaction must not
// be sent one, so it reports itself unfit rather than accepting traffic it will
// have to refuse one request at a time.
//
// With no org header it can only check that the plane is wired; with one it
// checks that the org's key material is actually reachable.
func (h *Handler) health() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		body := map[string]string{
			"status":  "ok",
			"records": "ok",
			"time":    time.Now().UTC().Format(time.RFC3339),
		}
		code := http.StatusOK

		switch {
		case h.Records == nil || h.Keys == nil:
			body["status"], body["records"] = "degraded", "not wired"
			code = http.StatusServiceUnavailable
		default:
			if id, err := h.tenant(e); err == nil {
				if _, err := h.vault(id); err != nil {
					log.Printf("[aml] health: %v", err)
					body["status"], body["records"] = "degraded", "no key material"
					code = http.StatusServiceUnavailable
				}
			}
		}
		return e.JSON(code, body)
	}
}

func (h *Handler) ingestTransaction() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID, err := h.tenant(e)
		if err != nil {
			return refuse(e, err)
		}

		// The relationship a transaction sits inside decides where its retention
		// period starts, so it travels with the transaction rather than being
		// guessed afterwards.
		var in struct {
			types.Transaction
			Relationship string `json:"relationship"`
			// Entity is the customer this transaction belongs to. It is supplied by
			// the caller because only the caller knows it: a name cannot be derived
			// from an identifier, and customer screening has no input without one.
			Entity types.Entity `json:"entity"`
		}
		if err := json.NewDecoder(e.Request.Body).Decode(&in); err != nil {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}
		tx := in.Transaction
		if tx.ID == "" {
			tx.ID = uuid.NewString()
		}
		tx.OrgID = orgID
		now := time.Now().UTC()
		tx.CreatedAt = now
		tx.UpdatedAt = now
		if tx.Timestamp.IsZero() {
			tx.Timestamp = now
		}

		// Normalise the value once, here, and overwrite whatever the caller sent.
		// A client-supplied figure would let a payment declare itself small: the
		// aggregates, the near-threshold counters and every ratio the model reads
		// are all in this unit, so accepting one would hand the submitter control
		// of what counts as just under a reporting limit.
		if h.Rate == nil {
			return fail(e, http.StatusServiceUnavailable, "no conversion is configured, so no threshold can be applied")
		}
		usd, err := h.Rate.USD(e.Request.Context(), tx.Notional, tx.Currency)
		if err != nil {
			// Refusing is the only safe answer. Passing the amount through unchanged
			// understates every currency worth more than a dollar, so a payment three
			// times over a reporting limit tests as sitting under it.
			log.Printf("[aml] conversion refused for %s: %v", tx.ID, err)
			return fail(e, http.StatusBadRequest, "currency cannot be converted, so no threshold can be applied to it")
		}
		tx.USD = usd

		// Aggregate before evaluating. The rule plane and the model both read
		// these windows, so a transaction has to be in them before it is judged
		// against them — otherwise the ninth payment under a limit is scored
		// against a history of eight and the alert quotes a number that does not
		// match the account.
		if h.Velocity != nil {
			for _, k := range anomaly.Keys(tx) {
				h.Velocity.Record(k, tx.Timestamp, tx.USD, h.limit())
			}
		}

		// Record the transaction in history before it is judged against history.
		//
		// The order is the point. Every aggregate rule reads a window, and a
		// transaction absent from the window it is evaluated against is judged on a
		// history that stops one short of it — so the ninth deposit under a
		// reporting limit is scored against eight, and the alert quotes a total that
		// does not match the account. Structuring is exactly the typology that
		// breaks: the transaction completing the pattern is the one excluded from
		// seeing it.
		//
		// A write failure refuses the transaction rather than evaluating it against
		// an incomplete history, because the alternative is a rule silently
		// answering "no velocity" for a window it could not read.
		if h.History != nil {
			if err := h.History.Append(e.Request.Context(), orgID, history.Event{
				ID:           tx.ID,
				At:           tx.Timestamp,
				USD:          tx.USD,
				Currency:     tx.Currency,
				Direction:    tx.Side,
				User:         tx.UserID,
				Account:      tx.AccountID,
				Counterparty: tx.Counterparty,
				Device:       tx.DeviceFingerprint,
				Address:      tx.IPAddress,
				Jurisdiction: entityJurisdiction(in.Entity, tx),
				Symbol:       tx.Symbol,
			}); err != nil {
				return unavailable(e, "history append", err)
			}
		}

		// The customer this transaction belongs to.
		//
		// Name is deliberately NOT defaulted from the identifier. A customer id is an
		// opaque handle, and screening one as though it were a person's name cannot
		// produce a true positive while readily producing false ones: "cust-1"
		// scored 0.902 against "BRANCH 1 OF THE SHIRAZ REVOLUTIONARY COU" — over the
		// reporting threshold — and blocked an ordinary payment.
		//
		// With no name supplied the customer-screening rule's guard (Entity.Name !=
		// "") holds and the rule does not fire. That is the honest outcome: a
		// customer whose name this service has never been given has not been
		// screened, and the way to screen them is to send the name, not to invent one
		// from a database key. GET /v1/aml/catalog carries the gap.
		entity := in.Entity
		entity.OrgID = orgID
		if entity.ID == "" {
			entity.ID = tx.UserID
		}
		if entity.EntityType == "" {
			entity.EntityType = types.EntityUser
		}

		vault, err := h.vault(orgID)
		if err != nil {
			return unavailable(e, "ingest", err)
		}

		alerts, score, action := h.Engine.Evaluate(e.Request.Context(), tx, entity)

		// Record before answering. Nothing else is stored until the record is,
		// so a failure here leaves no half-processed transaction behind.
		record, err := h.retain(vault, tx, entity, in.Relationship, alerts, action)
		switch {
		case errors.Is(err, errNoParty):
			return fail(e, http.StatusBadRequest, "transaction names no party to retain it under")
		case errors.Is(err, retention.ErrRelationship):
			return fail(e, http.StatusBadRequest, "unknown relationship")
		case err != nil:
			return unavailable(e, "retain transaction", err)
		}

		alertIDs := make([]string, len(alerts))
		for i, a := range alerts {
			alertIDs[i] = a.ID
		}
		h.Alerts.Add(tx.ID, alerts)

		result := struct {
			types.EvalResult
			Record string `json:"record"`
		}{
			EvalResult: types.EvalResult{Action: action, Score: score, AlertIDs: alertIDs},
			Record:     record,
		}

		// On critical: auto-create case.
		if action == types.ActionBlock || action == types.ActionReport {
			c := h.Cases.Create(orgID, types.SeverityCritical, alertIDs, []string{tx.UserID})
			result.CaseID = c.ID
		}

		return e.JSON(http.StatusOK, result)
	}
}

func (h *Handler) getAlerts() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID, err := h.tenant(e)
		if err != nil {
			return refuse(e, err)
		}
		alerts := h.Alerts.ByTx(orgID, e.Request.PathValue("id"))
		if alerts == nil {
			alerts = []types.Alert{}
		}
		return e.JSON(http.StatusOK, alerts)
	}
}

func (h *Handler) listCases() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID, err := h.tenant(e)
		if err != nil {
			return refuse(e, err)
		}
		status := e.Request.URL.Query().Get("status")
		result := h.Cases.List(orgID, status)
		if result == nil {
			result = []*types.Case{}
		}
		return e.JSON(http.StatusOK, result)
	}
}

// errNoCase is a case the caller may not see, whether because it does not exist or
// because it belongs to another tenant. One error for both, because the answer to
// the caller is the same: a case id is not a secret, so distinguishing them would
// let a caller enumerate another tenant's cases by their ids.
var errNoCase = errors.New("no such case")

// caseOf resolves a case within the caller's tenant. It reports the reason it
// could not and writes nothing: an answer is a response, and returning one from
// here as if it were an error is how an unauthenticated POST to a case route came
// to dereference a nil case — refuse() answers the request and then returns nil,
// so the caller saw no error and carried on with nothing.
func (h *Handler) caseOf(e *core.RequestEvent) (*types.Case, string, error) {
	tenant, err := h.tenant(e)
	if err != nil {
		return nil, "", err
	}
	c := h.Cases.Get(e.Request.PathValue("id"))
	if c == nil || c.OrgID != tenant {
		return nil, "", errNoCase
	}
	return c, tenant, nil
}

func (h *Handler) addCaseEvent() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		c, _, err := h.caseOf(e)
		switch {
		case errors.Is(err, errNoCase):
			return fail(e, http.StatusNotFound, "no such case")
		case err != nil:
			return refuse(e, err)
		}

		var evt types.CaseEvent
		if err := json.NewDecoder(e.Request.Body).Decode(&evt); err != nil {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}
		if err := h.Cases.AddEvent(c.ID, evt); err != nil {
			return fail(e, http.StatusNotFound, err.Error())
		}
		return e.JSON(http.StatusCreated, map[string]string{"status": "ok"})
	}
}

// resolution is a decision to close a case, and it carries what Art. 69(2)
// requires of one.
type resolution struct {
	Resolution string   `json:"resolution"`
	Considered []string `json:"considered"`
	Rationale  string   `json:"rationale"`
	By         string   `json:"by"`
}

// resolveCase closes a case against a retained assessment.
//
// The assessment is written first and the case is closed against its id, so a
// case cannot be closed without a retained decision — which is the requirement
// most systems miss. A dismissed alert is a retained decision with its rationale
// (AMLR Art. 77(1)(b); JMLSG 6.32), not a deleted row.
func (h *Handler) resolveCase() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		c, orgID, err := h.caseOf(e)
		switch {
		case errors.Is(err, errNoCase):
			return fail(e, http.StatusNotFound, "no such case")
		case err != nil:
			return refuse(e, err)
		}

		var in resolution
		if err := json.NewDecoder(e.Request.Body).Decode(&in); err != nil {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}
		if in.Resolution == "" {
			return fail(e, http.StatusBadRequest, "resolution is required")
		}

		vault, err := h.vault(orgID)
		if err != nil {
			return unavailable(e, "resolve case", err)
		}

		assessment, err := h.assess(vault, orgID, c, in)
		switch {
		case errors.Is(err, retention.ErrAssessment):
			return fail(e, http.StatusBadRequest, err.Error())
		case errors.Is(err, errNoParty):
			return fail(e, http.StatusBadRequest, "case names no party to retain the decision under")
		case err != nil:
			return unavailable(e, "retain assessment", err)
		}

		if err := h.Cases.Resolve(c.ID, in.Resolution, in.By, assessment); err != nil {
			return fail(e, http.StatusBadRequest, err.Error())
		}
		return e.JSON(http.StatusOK, map[string]string{
			"case":       c.ID,
			"resolution": in.Resolution,
			"assessment": assessment,
		})
	}
}

func (h *Handler) listRules() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		return e.JSON(http.StatusOK, h.Engine.Rules())
	}
}

func (h *Handler) searchSanctions() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		var req struct {
			Name string `json:"name"`
			DOB  string `json:"dob"`
		}
		if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}
		if req.Name == "" {
			return fail(e, http.StatusBadRequest, "name is required")
		}

		if h.Screen == nil {
			return fail(e, http.StatusServiceUnavailable, "screening is not wired")
		}
		// An unfit list set cannot produce a clean answer, so it does not produce an
		// answer at all. Returning an empty result here is a false negative with a
		// 200 on it.
		if err := h.Screen.Ready(); err != nil {
			log.Printf("[aml] screening refused: %v", err)
			return fail(e, http.StatusServiceUnavailable, "screening is not ready")
		}

		results, err := h.Screen.Search(sanctions.Query{Name: req.Name}, sanctions.Threshold)
		if err != nil {
			return fail(e, http.StatusBadRequest, err.Error())
		}

		// The response carries why the hit matched and what corroborated or
		// contradicted it, not just a score. A score alone cannot be cleared: the
		// question an analyst has to answer is whether this is the same person, and
		// that is decided on the identifiers that agree and disagree.
		type hit struct {
			List     string   `json:"list"`
			RefID    string   `json:"ref_id"`
			Name     string   `json:"name"`
			Kind     string   `json:"kind"`
			Score    float64  `json:"score"`
			Reason   string   `json:"reason"`
			Agree    []string `json:"agree,omitempty"`
			Conflict []string `json:"conflict,omitempty"`
			Programs []string `json:"programs,omitempty"`
		}

		out := make([]hit, 0, len(results))
		for _, m := range results {
			out = append(out, hit{
				List:     m.Entry.List,
				RefID:    m.Entry.RefID,
				Name:     m.Name.Full,
				Kind:     m.Entry.Kind,
				Score:    m.Score,
				Reason:   m.Reason,
				Agree:    m.Agree,
				Conflict: m.Conflict,
				Programs: m.Entry.Programs,
			})
		}
		return e.JSON(http.StatusOK, out)
	}
}

// maxDSLLength is the maximum allowed DSL expression length (RED-15).
const maxDSLLength = 2048

// testRule replays a candidate rule over history before anyone activates it.
//
// JMLSG 5.7.18 requires the functionality; FCG 3.2.5A requires a retirement to be
// justified against the outgoing rule's performance, which is why an incumbent can
// be named and the report carries the difference. With no sample the replay runs
// over the org's retained transactions; a sample replaces the history rather than
// adding to it, so an author can try an expression without touching the record
// plane. Either way nothing is written.
func (h *Handler) testRule() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID, err := h.tenant(e)
		if err != nil {
			return refuse(e, err)
		}

		var req struct {
			DSL       string         `json:"dsl"`
			Incumbent string         `json:"incumbent"`
			Sample    []replay.Event `json:"sample"`
		}
		if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}

		// RED-15: Reject oversized DSL to prevent DoS via computation bombs.
		if len(req.DSL) > maxDSLLength {
			return fail(e, http.StatusBadRequest, "DSL expression exceeds maximum length of 2048 bytes")
		}
		if len(req.Sample) > maxSample {
			return fail(e, http.StatusBadRequest, "sample exceeds maximum of 1000 events")
		}

		var incumbent *types.Rule
		if req.Incumbent != "" {
			for _, r := range h.Engine.Rules() {
				if r.ID == req.Incumbent {
					found := r
					incumbent = &found
					break
				}
			}
			if incumbent == nil {
				return fail(e, http.StatusBadRequest, "no such rule to replace")
			}
		}

		var history replay.History = replay.Slice(req.Sample)
		if len(req.Sample) == 0 {
			vault, err := h.vault(orgID)
			if err != nil {
				return unavailable(e, "replay", err)
			}
			stored, err := h.history(orgID, vault)
			if err != nil {
				return unavailable(e, "read history", err)
			}
			history = stored
		}

		candidate := types.Rule{ID: "candidate", Name: "candidate", DSL: req.DSL}
		report, err := replay.Run(e.Request.Context(), h.Engine.Evaluator(), history, candidate, incumbent)
		switch {
		case errors.Is(err, replay.ErrEmpty):
			return fail(e, http.StatusConflict, "no history to replay against")
		case errors.Is(err, replay.ErrNoRule):
			return fail(e, http.StatusBadRequest, "dsl is required")
		case errors.Is(err, replay.ErrEval):
			return fail(e, http.StatusBadRequest, err.Error())
		case err != nil:
			return unavailable(e, "replay", err)
		}
		return e.JSON(http.StatusOK, report)
	}
}

// openRelationship records the start of a business relationship, whose retention
// period runs from the end of it (AMLR Art. 77(3)).
func (h *Handler) openRelationship() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID, err := h.tenant(e)
		if err != nil {
			return refuse(e, err)
		}

		var in struct {
			// Ref is the firm's own reference. It is retained in the clear, so it
			// must be a synthetic reference and not a direct identifier.
			Ref     string    `json:"ref"`
			Nature  string    `json:"nature"`
			Opened  time.Time `json:"opened"`
			UserID  string    `json:"user_id"`
			Name    string    `json:"name"`
			Account string    `json:"account_id"`
			Wallet  string    `json:"wallet"`
		}
		if err := json.NewDecoder(e.Request.Body).Decode(&in); err != nil {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}
		if in.Opened.IsZero() {
			in.Opened = time.Now().UTC()
		}

		vault, err := h.vault(orgID)
		if err != nil {
			return unavailable(e, "open relationship", err)
		}
		party, err := parties(vault, map[token.Domain]string{
			token.DomainSubject: in.UserID,
			token.DomainName:    in.Name,
			token.DomainAccount: in.Account,
			token.DomainWallet:  in.Wallet,
		})
		if err != nil {
			return fail(e, http.StatusBadRequest, "relationship names no party")
		}

		body, err := seal(vault, retention.ClassRelationship, in.Ref, in)
		if err != nil {
			return unavailable(e, "seal relationship", err)
		}

		id, err := h.Records.Retain(retention.Record{
			Org:      orgID,
			Class:    retention.ClassRelationship,
			Trigger:  retention.TriggerRelationshipEnd,
			Ref:      in.Ref,
			Nature:   in.Nature,
			Parties:  party,
			Occurred: in.Opened,
			Body:     body,
		})
		switch {
		case errors.Is(err, retention.ErrNature):
			return fail(e, http.StatusBadRequest, "nature is required")
		case err != nil:
			return unavailable(e, "retain relationship", err)
		}
		return e.JSON(http.StatusCreated, map[string]string{"relationship": id})
	}
}

// closeRelationship ends a relationship, which starts the retention clock on it
// and on everything retained inside it.
func (h *Handler) closeRelationship() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID, err := h.tenant(e)
		if err != nil {
			return refuse(e, err)
		}
		if h.Records == nil {
			return unavailable(e, "close relationship", errNoRecords)
		}

		var in struct {
			Ended time.Time `json:"ended"`
		}
		if err := json.NewDecoder(e.Request.Body).Decode(&in); err != nil {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}
		if in.Ended.IsZero() {
			in.Ended = time.Now().UTC()
		}

		started, err := h.Records.Close(orgID, e.Request.PathValue("id"), in.Ended)
		switch {
		case errors.Is(err, retention.ErrRelationship):
			return fail(e, http.StatusNotFound, "no such relationship")
		case errors.Is(err, retention.ErrClosed), errors.Is(err, retention.ErrOccurred):
			return fail(e, http.StatusBadRequest, err.Error())
		case err != nil:
			return unavailable(e, "close relationship", err)
		}
		return e.JSON(http.StatusOK, map[string]int{"clocks_started": started})
	}
}

// searchRelationships answers AMLR Art. 78: whether a business relationship with
// a named person is or was maintained in the prior five years, and its nature.
// The name is tokenised and the answer comes from the party index, so it is an
// index lookup rather than a scan — which is what "fully and speedily" needs.
func (h *Handler) searchRelationships() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID, err := h.tenant(e)
		if err != nil {
			return refuse(e, err)
		}

		var in struct {
			Party  string       `json:"party"`
			Domain token.Domain `json:"domain"`
		}
		if err := json.NewDecoder(e.Request.Body).Decode(&in); err != nil {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}
		if in.Party == "" {
			return fail(e, http.StatusBadRequest, "party is required")
		}
		if in.Domain == "" {
			in.Domain = token.DomainName
		}

		vault, err := h.vault(orgID)
		if err != nil {
			return unavailable(e, "relationship search", err)
		}
		pseudonym, err := vault.Pseudonym(in.Domain, in.Party)
		if err != nil {
			return fail(e, http.StatusBadRequest, err.Error())
		}

		answer, err := h.Records.Lookback(retention.PurposeDisclosure, orgID, pseudonym, time.Now().UTC())
		if err != nil {
			return unavailable(e, "relationship search", err)
		}
		return e.JSON(http.StatusOK, answer)
	}
}

// entityJurisdiction is the jurisdiction a transaction is attributed to: the
// customer's where it is known, the transaction's otherwise. It is stored on the
// event so a jurisdiction rule reads what was true at the time rather than what
// the customer record says today.
func entityJurisdiction(ent types.Entity, tx types.Transaction) string {
	if ent.Jurisdiction != "" {
		return ent.Jurisdiction
	}
	return tx.CustomerJurisdiction
}
