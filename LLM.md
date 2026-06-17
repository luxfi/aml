# luxfi/aml

## Overview

Go module: `github.com/luxfi/aml`

Real-time AML/CFT transaction monitoring engine. Pure Go, single binary, embedded SQLite via Hanzo Base. Configurable rules DSL (expr-lang), sanctions screening (OFAC/UN/EU/HMT), case management, scored alerting.

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
cmd/amld/main.go          -- Single binary: serve, version, embedded admin UI
migrations/0001_core.sql   -- 9 SQLite collections
ui/                        -- Embedded admin dashboard (Vite + React + @hanzo/gui)
  embed.go                 -- go:embed all:dist -> DistDirFS()
  package.json             -- @luxfi/aml-ui (private, pnpm)
  vite.config.ts           -- base: /_/aml/, proxy /v1/aml to :8090
  src/
    main.tsx               -- React 19 entry
    App.tsx                -- Hash router (wouter), sidebar nav, 5 routes
    api.ts                 -- Typed fetch wrappers for /v1/aml/* endpoints
    pages/
      Dashboard.tsx        -- Stats cards, recent cases, health check
      Cases.tsx            -- DataTable with status filter tabs, expandable rows
      Rules.tsx            -- DataTable with DSL display, test rule modal
      Alerts.tsx           -- DataTable with severity filter chips, score breakdown
      Sanctions.tsx        -- Name search form, results table with scores
  dist/                    -- Built output (gitignored, ~220KB)
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
  api/routes.go            -- /v1/aml/* HTTP routes + SanctionsStore on Base
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
| POST | /v1/aml/sanctions/search | Search sanctions lists by name |
| GET | /v1/aml/health | Health check |

All endpoints require `X-Org-Id` header.

## Embedded Admin UI

Served at `/_/aml/` via `go:embed`. Hash router (`/#/cases`, `/#/rules`, etc.) for SPA routing without server rewrites.

```bash
cd ui && pnpm install && pnpm build   # produces dist/ (~220KB)
cd .. && go build ./cmd/amld/         # binary includes UI
./amld serve --dev                    # UI at http://localhost:8090/_/aml/
```

Dev mode: `cd ui && pnpm dev` (port 3000, proxies API to :8090).

Tech: React 19, wouter (hash router), @hanzo/gui, Vite 6. Dark theme.

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

## Compliance Client Compatibility

`luxfi/compliance` AML client connects via HTTP:

| Client Method | Endpoint |
|---|---|
| ScreenTransaction | POST /v1/aml/transactions |
| CheckSanctions | POST /v1/aml/sanctions/search |
| CreateCase | POST /v1/aml/cases/{id}/events |
| GetCases | GET /v1/aml/cases |

Webhook events: `aml.flagged`, `aml.cleared`, `kyc.approved`, `trade.executed`.

## Deferred

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
- K8s: Deployment, replicas=1 (SQLite single-writer), PVC for /data
- Replication: hanzoai/replicate sidecar for age-encrypted S3 WAL streaming

## Test Coverage

56 tests across 5 packages. All pass with `-race`.

- engine: 20 tests (evaluator, scoring, integration)
- rules: 20 tests (all 20 starter rules compile + individual eval)
- cases: 10 tests (CRUD, status, events, assignment, resolution)
- sanctions: 12 tests (Jaro-Winkler, normalize, token match)
- webhook: 4 tests (sign, verify, deliver, failure)
