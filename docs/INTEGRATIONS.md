# amld — Integration Patterns

This document describes how `amld` integrates with the rest of the
stack. It is deliberately generic: it specifies the *interfaces*,
*data shapes*, and *control flow*, not any particular commercial
data provider. Operators choose their own watchlist, PEP, and
adverse-media sources and adapt them to the schemas below.

`amld` is the AML / FinCrime evaluation service. It exposes a
versioned HTTP API on `/v1/aml/*`, persists rules / alerts / cases
through a Hanzo Base–backed store, evaluates per-transaction risk
synchronously, emits signed webhooks for downstream review, and
ships with a starter rule library covering the standard typologies.

The integration surface has six concerns:

1. Sanctions / watchlist screening
2. Politically Exposed Person (PEP) screening
3. Adverse-media screening
4. Velocity and amount-threshold limits
5. Customer risk tiers
6. Pre-transaction hooks for caller services (`bankd`, exchange,
   payments)
7. Webhook + case-management surface for human review

---

## 1. Sanctions / Watchlist Screening

`amld` treats sanctions data as a *normalised, append-only catalog*
that any number of upstream lists can feed. The canonical model is in
`pkg/types/types.go`:

```go
type SanctionsList struct {
    ID        string    // internal id
    Source    string    // free-form source identifier
    URL       string    // origin URL
    Format    string    // xml | csv | json | ...
    FetchedAt time.Time
    SHA256    string    // integrity hash of the raw payload
    Active    bool
}

type SanctionsEntry struct {
    ID          string
    ListID      string   // back-ref to SanctionsList.ID
    RefID       string   // upstream-stable identifier
    Name        string
    Aliases     []string
    DOB         string   // best-effort, often partial
    Nationality string
    Address     string
    Type        string   // individual | entity | vessel | aircraft
    Raw         json.RawMessage // original record preserved verbatim
}
```

The shipped list-source constants (`ofac_sdn`, `un`, `eu`, `hmt`,
`interpol`) are *category tags*, not bindings to any particular
publisher. Operators can ingest:

- Government-published consolidated lists (OFAC SDN / Consolidated,
  UN Security Council, EU Consolidated Financial Sanctions, UK HMT
  OFSI, regional regulators).
- Internal block lists (denylists from internal investigations,
  closed customer relationships, etc.).
- Third-party aggregated watchlist feeds — the same ingest path
  applies; only the parser differs.

### Ingest contract

A list ingester is a function that:

1. Fetches the upstream payload.
2. Computes its SHA-256 (recorded on `SanctionsList`).
3. Parses it into `[]SanctionsEntry` rows tagged with the parent
   `ListID`.
4. Persists rows transactionally — partial ingests must not leave
   half-loaded lists active.

The matcher (`pkg/sanctions/match.go`) is source-agnostic. It scores
incoming names against `Name` and every `Alias` using token-based
similarity and returns hits above a configurable threshold.

### Refresh cadence

`pkg/sanctions/refresh.go` defines the periodic refresh loop. Cadence
is policy: regulated counterparties typically require daily refresh
of consolidated government lists; internal lists update on commit.

---

## 2. PEP Screening

PEP status is a *property of the entity*, not of the transaction. The
canonical flag lives on `Entity`:

```go
type Entity struct {
    ID            string
    OrgID         string
    EntityType    string  // user | account | counterparty | bank_account
    Name          string
    Jurisdiction  string
    KYCLevel      int
    PEP           bool    // <-- politically exposed
    SanctionsFlag bool
    RiskScore     float64
}
```

`PEP` is a boolean on the resolved entity. The recommended pattern:

- Upstream KYC pipeline (in `bankd` or a dedicated onboarding
  service) determines PEP status at customer-onboarding and on
  periodic re-screen.
- The result is written to the AML `Entity` record. `amld` is the
  consumer, not the resolver — it does not embed a PEP database.
- Rules consume `entity.PEP` directly via the expr-lang DSL:
  `entity.PEP && tx.Notional > 1000`.

The same shape supports relatives-and-close-associates (RCA) and
PEP-by-association — encode them either as additional boolean
attributes on `Entity` (via a future-compatible extension column) or
as separate watchlist categories ingested through the sanctions path
above. The data model intentionally does not over-specify PEP
sub-categories so that operators can match the granularity their
regulator requires.

---

## 3. Adverse-Media Screening

Adverse-media findings are modelled as *evidence attached to a
case*, not as a primary signal. The integration shape:

- An external adverse-media screen (operator's choice) produces a
  finding: subject identifier, severity, source URL, headline,
  excerpt, date.
- The finding is posted as a `CaseEvent` of kind `note` (or `file`
  if a snapshot PDF is attached) on a case opened for the subject:

  ```
  POST /v1/aml/cases/{id}/events
  {
    "kind": "note",
    "body": "Adverse media match — <category> — <source> — <date>"
  }
  ```

- For *automated* adverse-media gating before a transaction settles,
  the upstream screen pushes an adverse-media risk score into the
  `Entity.RiskScore` field (or a derived attribute) so the rule
  engine can read it via expr-lang: `entity.RiskScore > 0.7`.

This keeps `amld` neutral about the adverse-media provider while
giving operators a clean place to materialise findings into the case
record for examiner review.

---

## 4. Velocity & Amount-Threshold Limits

Velocity and threshold rules are expressed in the rule DSL. The
starter library (`pkg/rules/library.go`) ships representative
examples:

| Rule              | DSL                                        |
|-------------------|---------------------------------------------|
| CTR threshold     | `notional_usd(tx) > 10000`                 |
| Structuring       | `tx.Notional >= 9000 && tx.Notional < 10000` |
| Velocity (24h)    | `sum_last_24h(tx.UserID) > 50000`          |
| Velocity (30d)    | `sum_last_30d(tx.UserID) > 500000`         |
| First-time large  | `first_tx(tx.UserID) && tx.Notional > 25000` |
| Round-trip        | `is_round_trip(tx.UserID, "24h")`          |

Helper functions (`pkg/engine/helpers.go`) provide the temporal
aggregates: `sum_last_24h`, `sum_last_30d`, `count_last_24h`,
`first_tx`, `is_round_trip`, `notional_usd`. Operators add their
own helpers by registering them on the evaluator.

Each rule carries:

- `Severity`: low | medium | high | critical
- `Weight`: contribution to the aggregate risk score (0..1)
- `Action`: allow | flag | review | block | report
- `JurisdictionFilter` / `AssetClassFilter`: scope guards
- `Priority`: deterministic evaluation order

Negative weights are rejected at write time (RED-19 hardening) — a
rule cannot subtract from the score.

---

## 5. Customer Risk Tiers

There is no separate "risk-tier" table. Tiering is modelled by the
combination of two `Entity` fields:

- `KYCLevel` (int) — the verification depth completed.
- `RiskScore` (float, 0..1) — the rolling risk score from accepted
  alerts.

A rule expresses a tier in DSL:

```
entity.KYCLevel < 2 && tx.Notional > 1000
entity.RiskScore > 0.6 && tx.AssetClass == "crypto"
```

The aggregate score returned by `engine.Evaluate` is the
weight-sum of matching rules, clipped to `[0, 1]`. The action taken
is the *most severe* of the matching rules' actions (`report` >
`block` > `review` > `flag` > `allow` for ordering purposes), with
ties broken by `Priority`.

---

## 6. Pre-Transaction Hooks (`bankd` and other callers)

Caller services call `amld` synchronously on the hot path. The
contract is one POST per transaction:

```
POST /v1/aml/transactions
Headers:
  X-Org-Id: <tenant>
  Authorization: Bearer <iam-jwt>
Body:
  {
    "id":          "<caller-tx-id>",   // optional; generated if absent
    "user_id":     "<entity-id>",
    "account_id":  "<account-id>",
    "symbol":      "<asset>",
    "asset_class": "fiat|crypto|equity|...",
    "side":        "buy|sell|in|out",
    "qty":         123.0,
    "notional":    12300.00,
    "currency":    "USD",
    "counterparty":"<counterparty-id>",
    "ip_address":  "...",
    "device_fingerprint":"...",
    "timestamp":   "2026-05-23T12:34:56Z"
  }
```

The response is the synchronous decision:

```
{
  "action":    "allow|flag|review|block|report",
  "score":     0.42,
  "alert_ids": ["..."],
  "case_id":   "..."        // present when action ∈ {review,block,report}
}
```

### Caller contract (e.g., `bankd`)

Pseudocode at the caller:

```
res := amld.Evaluate(tx)
switch res.Action {
case "allow":
    proceed()
case "flag":
    proceed()                // async review; do not block settlement
case "review":
    park(tx, res.CaseID)     // hold until case resolved
case "block":
    reject(tx, res.AlertIDs) // hard stop
case "report":
    proceed()                // settle, but a regulatory report
                             // (e.g., CTR) is now owed
}
```

The caller MUST treat any non-200 response as **fail-closed** for
regulated transactions (i.e., reject), and as **fail-open** only
for explicitly designated low-risk lanes. The default posture is
fail-closed.

The hook is a single round-trip; `amld` does not call back into
the caller during evaluation. Asynchronous review happens via the
case + webhook surface described next.

---

## 7. Webhooks & Case Management

When a transaction transitions out of `allow`, two things happen:

1. A `Case` is opened (or extended) with the firing `Alert` rows
   attached.
2. A signed webhook is dispatched to every subscriber registered
   for the relevant event.

### Webhook contract

Subscriber config:

```go
type Webhook struct {
    ID      string
    OrgID   string
    URL     string
    Secret  string
    Events  []string  // aml.flagged | aml.cleared | case.opened | ...
    Enabled bool
}
```

Delivery is HTTP POST with:

- `Content-Type: application/json`
- `X-Webhook-Event: <event-name>`
- `X-Webhook-Signature: <hex(hmac-sha256(body, secret))>`

Body:

```
{
  "event":     "case.opened",
  "timestamp": "2026-05-23T12:34:56Z",
  "data":      { ... }
}
```

Delivery is at-least-once with exponential backoff up to
`webhook.MaxRetries` attempts; persistent failures dead-letter.
Outbound URLs are validated against SSRF (RED-18 hardening) — no
loopback, no link-local, no metadata endpoints.

### Case API

The case-management surface is HTTP-only — there is no embedded
reviewer UI in `amld` proper (the dashboard is mounted by Base at
`/_/aml/`).

| Method | Path                                   | Purpose                           |
|--------|----------------------------------------|-----------------------------------|
| POST   | `/v1/aml/transactions`                 | Submit tx, get decision           |
| GET    | `/v1/aml/transactions/{id}/alerts`     | Alerts for a tx                   |
| GET    | `/v1/aml/cases`                        | List cases (filterable)           |
| POST   | `/v1/aml/cases/{id}/events`            | Append note / file / status change |
| GET    | `/v1/aml/rules`                        | List active rules                 |
| POST   | `/v1/aml/rules/test`                   | Dry-run a rule against a sample tx |
| POST   | `/v1/aml/sanctions/search`             | Name search against loaded lists  |
| GET    | `/v1/aml/health`                       | Liveness                          |

All routes require `X-Org-Id` and an authenticated IAM session.
Data is scoped per-org; cross-org reads are not possible.

A typical reviewer flow:

1. Case opens → `case.opened` webhook fires → reviewer's
   case-management UI ingests it.
2. Reviewer investigates; appends notes / files / a status change
   via `POST /v1/aml/cases/{id}/events`.
3. Reviewer resolves the case (`cleared` | `sar_filed` |
   `account_frozen` | `false_positive`); this drives the
   `case.closed` webhook and updates the rolling `Entity.RiskScore`
   for future evaluations.

---

## Operational invariants

- **Synchronous, single-round-trip evaluation.** Callers do not wait
  on review; they wait only on rule evaluation.
- **Per-org isolation.** All persisted rows carry `OrgID`; queries
  scope by `X-Org-Id` enforced server-side.
- **Fail-closed on rule error.** A rule that errors counts as
  *unevaluated* with `EvalErr` populated; the operator-configured
  default action applies (default: `review`).
- **Append-only sanctions catalog.** Lists are content-addressed by
  SHA-256; refreshes never mutate prior entries in place.
- **Signed, retried webhooks.** HMAC-SHA256 over the body with the
  subscriber's secret; outbound URLs SSRF-validated.
- **No vendor lock-in.** Every external data category — sanctions,
  PEP, adverse-media — is consumed via a generic schema. Swapping
  providers means swapping ingesters, not rewriting the engine.
