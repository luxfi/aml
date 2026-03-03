# LLM.md - luxfi/aml

## Overview

Go module: `github.com/luxfi/aml`

Pure Go AML engine replacing the C# Jube fork at `~/work/liquidity/aml/`.
Single binary `amld` on Hanzo Base (embedded SQLite) + expr-lang rules DSL.

## Tech Stack

- **Language**: Go 1.26.1
- **Database**: Hanzo Base (embedded SQLite per-org)
- **Rules DSL**: github.com/expr-lang/expr v1.17
- **Scoring**: Weight-of-evidence (pure Go math)
- **Sanctions**: Jaro-Winkler + token-based fuzzy name matching
- **HTTP**: Hanzo Base router (net/http)
- **Auth**: X-Org-Id header (set by gateway from JWT owner claim)

## Build & Run

```bash
go build ./cmd/amld/       # Build binary
go test -race ./...         # Run all tests
./amld serve --dev          # Dev mode (auto-creates data dir)
make build                  # Build with version
make test                   # Test with race detector
make run                    # Build + run dev
```

## Module Layout

```
cmd/amld/main.go          -- Single binary: serve, version
migrations/0001_core.sql   -- 9 SQLite collections
pkg/
  types/types.go           -- Canonical domain types (Transaction, Entity, Rule, Alert, Case)
  engine/
    engine.go              -- Core engine: evaluate tx -> alerts + score + action
    evaluator.go           -- expr-lang compiler + evaluator with helper functions
    scoring.go             -- Weight-of-evidence scoring + ScorerPlugin interface
  rules/library.go         -- 20 starter AML rules (CTR, structuring, velocity, sanctions, etc.)
  sanctions/
    match.go               -- Jaro-Winkler + token-based fuzzy name matching
    lists.go               -- Default list configurations (OFAC, UN, EU, HMT)
    ingest.go              -- OFAC SDN XML parser + HTTP fetcher
  cases/
    case.go                -- Case store (create, update status, assign, resolve, events)
    errors.go              -- Sentinel errors
  webhook/webhook.go       -- Signed delivery (HMAC-SHA256) with retry + dead-letter
  api/routes.go            -- /v1/aml/* HTTP routes on Base
```

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | /v1/aml/transactions | Ingest transaction, sync rule eval, returns action |
| GET | /v1/aml/transactions/{id}/alerts | Alerts for a transaction |
| GET | /v1/aml/cases | List cases (filter by status) |
| POST | /v1/aml/cases/{id}/events | Add case event (note, status change) |
| GET | /v1/aml/rules | List all rules |
| POST | /v1/aml/rules/test | Dry-run a DSL expression |
| GET | /v1/aml/health | Health check |

All endpoints require `X-Org-Id` header.

## Transaction Ingest Flow

```
POST /v1/aml/transactions
  -> Parse + validate
  -> Resolve entity from user_id
  -> EvalAll(rules, {tx, entity})
     -> Filter by jurisdiction, asset_class, enabled
     -> Compile + run each rule DSL via expr-lang
  -> Score(hits) -> weight-of-evidence [0,1]
  -> resolveAction(alerts) -> highest severity action wins
  -> If block/report: auto-create Case
  -> Return {action, score, alert_ids, case_id?}
```

## Jube C# to Go Mapping

| C# (Jube) | Go (luxfi/aml) | Notes |
|-----------|---------------|-------|
| Engine.cs | pkg/engine/engine.go | Core orchestration |
| EntityAnalysisModelInvoke | pkg/engine/evaluator.go | Rule evaluation (expr-lang replaces C# scripted rules) |
| Accord.NET neural network | pkg/engine/scoring.go ScorerPlugin | Deferred — WoE baseline in v1 |
| BackgroundTasks/AmqpTaskStarter | N/A | Removed — no AMQP/RabbitMQ |
| BackgroundTasks/SanctionsTaskStarter | pkg/sanctions/ingest.go | OFAC/UN/EU/HMT XML fetch |
| BackgroundTasks/CaseAutomationStarter | pkg/cases/case.go | In-process case lifecycle |
| BackgroundTasks/CaseCreationTaskStarter | pkg/engine/engine.go auto-create | Inline on block/report |
| BackgroundTasks/ManageCountersStarter | N/A | Aggregates via Base indexed queries |
| BackgroundTasks/NotificationsViaAmqpStarter | pkg/webhook/webhook.go | HTTP webhooks replace AMQP |
| BackgroundTasks/TaggingStarter | N/A | Removed — tags are fields on entities |
| BackgroundTasks/AsyncHttpContextCorrelation | N/A | Removed — Base handles concurrency |
| Exhaustive/Training.cs | Deferred | ML training out of scope for v1 |
| Sanctions/LevenshteinDistance.cs | pkg/sanctions/match.go | Jaro-Winkler (better for names) replaces Levenshtein |
| Cache/CacheService (Redis) | In-process cache (sync.Map) | No Redis — Base SQLite queries + short-TTL process cache |
| DynamicEnvironment | Environment variables | Standard Go env config |
| Poco.cs (154 classes) | pkg/types/types.go (~15 types) | Lean model — no cache/archive/session POCOs |
| PostgreSQL | SQLite (Hanzo Base) | Embedded, replicated via hanzoai/replicate |
| Redis | N/A | Removed |
| RabbitMQ | N/A | Removed — webhooks + Base realtime |
| Aml.Migrations | migrations/0001_core.sql | 9 tables vs 154 Jube tables |
| Entity Framework / LinqToDB | Hanzo Base collections | Auto CRUD + realtime |
| log4net | Hanzo Base built-in logging | Structured JSON |
| JWT auth (custom) | X-Org-Id from IAM gateway | Gateway validates JWT, sets identity headers |

## Starter Rules (20)

| # | Rule | DSL Pattern | Action |
|---|------|-------------|--------|
| 1 | CTR Threshold | notional > 10000 && USD | report |
| 2 | Structuring | notional in [9000, 10000) | flag |
| 3 | Velocity Daily | sum_last_24h > 50000 | review |
| 4 | Velocity Monthly | sum_last_30d > 500000 | review |
| 5 | First-Time Large | first_tx && notional > 25000 | review |
| 6 | Round-Trip | is_round_trip 24h | flag |
| 7 | Sanctioned Jurisdiction | jurisdiction in sanctioned list | block |
| 8 | PEP Large | PEP && notional > 10000 | review |
| 9 | Unusual Hour | hour in [2,3,4] | flag |
| 10 | New Counterparty Large | first_counterparty && notional > 5000 | review |
| 11 | Dormant Reactivation | last_tx > 180d && notional > 10000 | review |
| 12 | Crypto Mixer | is_mixer_address | block |
| 13 | Darknet Market | is_darknet_market | block |
| 14 | IP/Geo Mismatch | !geo_match | flag |
| 15 | Layering | layering_score > 0.8 | review |
| 16 | Smurfing | smurfing_detected 7d | review |
| 17 | Wash Trading | wash_trade_detected 1h | flag |
| 18 | Sanctions Direct | sanctions_hit(entity) | block |
| 19 | Sanctions Counterparty | sanctions_hit(counterparty) | block |
| 20 | Travel Rule | notional > travel_rule_threshold | report |

## Jube Client Compatibility

The existing `luxfi/compliance/pkg/jube/` client stays unchanged.
Its API surface (ScreenTransaction, CheckSanctions, CreateCase, GetCases) maps to:

| Jube Client Method | luxfi/aml Endpoint |
|---|---|
| ScreenTransaction | POST /v1/aml/transactions |
| CheckSanctions | POST /v1/aml/sanctions/search |
| CreateCase | POST /v1/aml/cases/{id}/events |
| GetCases | GET /v1/aml/cases |

Webhook event names are preserved: `aml.flagged`, `aml.cleared`, `kyc.approved`, `trade.executed`.

## Deferred Work

- ML scoring via Hanzo Zen gateway (ScorerPlugin interface ready)
- Hanzo Tasks durable workflows (sanctions-refresh, case-automation, backtest)
- Base realtime subscription for live transaction monitoring
- Backtest/batch evaluation against historical data
- SAR draft generation
- Full OFAC/UN/EU/HMT list refresh automation
- Base collection persistence (current: in-memory stores for v1 engine)

## Deployment

- Single binary: `amld serve --http=0.0.0.0:8090`
- Docker: `ghcr.io/luxfi/aml:{env}`
- K8s: StatefulSet, replicas=1 (SQLite single-writer), PVC for /data
- Replication: hanzoai/replicate sidecar for age-encrypted S3 WAL streaming

## Test Coverage

56 tests across 5 packages. All pass with `-race`.

- engine: 20 tests (evaluator, scoring, engine integration)
- rules: 20 tests (all 20 starter rules compile + individual eval)
- cases: 10 tests (CRUD, status, events, assignment, resolution)
- sanctions: 12 tests (Jaro-Winkler, normalize, token match)
- webhook: 4 tests (sign, verify, deliver, failure)
