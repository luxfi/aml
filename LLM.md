# luxfi/aml

## Overview

Go module: `github.com/luxfi/aml`

Real-time AML/CFT transaction monitoring engine. Pure Go, single binary, embedded SQLite via Hanzo Base. Configurable rules DSL (expr-lang), sanctions screening (OFAC/UN/EU/HMT), case management, scored alerting.

## Tech Stack

- **Language**: Go 1.26.4
- **Database**: Hanzo Base (embedded SQLite per-org)
- **Rules DSL**: github.com/expr-lang/expr v1.17
- **Scoring**: Weight-of-evidence (pure Go math)
- **Sanctions**: Jaro-Winkler + token-based fuzzy name matching
- **HTTP**: Hanzo Base router (net/http)
- **Auth**: X-Org-Id header (set by gateway from JWT owner claim)

## Build & Run

```bash
make build                  # UI build + Go build -tags embedui -> ./amld (the product)
make test                   # go test -race -count=1 ./...
make vet                    # go vet ./...
make ui                     # pnpm install --frozen-lockfile && pnpm build -> ui/dist
make run                    # Build + run dev
```

`make build` is the only command that produces the shippable binary: the admin
dashboard is embedded, so it always builds `ui/dist` first and links with
`-tags embedui`.

Bare `go build ./...`, `go vet ./...`, `go test ./...` and `go install` work on a
fresh clone with no Node toolchain — without the `embedui` tag, `ui/embed.go`
supplies a placeholder tree instead of embedding `ui/dist`. That is what keeps
the module installable, since `ui/dist` is a build artifact and is not committed.

## Module Layout

```
cmd/amld/main.go          -- Single binary: serve, version, embedded admin UI
migrations/0001_core.sql   -- 9 SQLite collections
ui/                        -- Embedded admin dashboard (Vite + React + @hanzo/gui)
  embed.go                 -- !embedui: placeholder DistDirFS(), no dist/ needed
  embed_prod.go            -- embedui: go:embed all:dist -> DistDirFS()
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
make build             # builds ui/dist, then links it with -tags embedui
./amld serve --dev     # UI at http://localhost:8090/_/aml/
```

Without `-tags embedui` the binary serves a placeholder page at `/_/aml/`; that
is the expected result of a bare `go build`/`go install`.

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

## Dependency pinning

Every dependency resolves through `proxy.golang.org` and is verified against
`sum.golang.org`. Both are append-only: the proxy serves the zip it first
recorded for a version, and the checksum database is a transparency log of that
zip's hash. A tag that moves afterwards therefore cannot change what this repo
compiles — which is the whole point, because tags in this fleet do move.

The builds pin that policy themselves rather than inheriting it. A machine may
carry a `go env` file whose `GOPRIVATE`/`GONOPROXY`/`GONOSUMDB` route
`luxfi/*` and `hanzoai/*` around both, so `ci.yml` and `release.yml` set
`GOENV: off`, the one documented switch that ignores the file. `GOPRIVATE=` in
the environment does **not** override it: Go reads an empty environment variable
as unset and falls back to the file.

Rules:

1. **Never write a hash `sum.golang.org` does not corroborate.** The log is the
   authority; if `go.sum` disagrees with it, `go.sum` is wrong. Two commits in
   this history (`9821aff`, `77173ad`) wrote hashes taken from whatever the git
   host was serving, which is the failure this rule exists to prevent.

   ```bash
   curl -sS https://sum.golang.org/lookup/github.com/luxfi/pq@v1.0.3
   ```

2. **`go mod verify` does not check `go.sum`.** It compares the extracted module
   against the hash recorded in the local module cache, so it reports
   "all modules verified" while a `go.sum` entry that matches nothing sits right
   next to it. Only a download can catch that — on a *clean* `GOMODCACHE`, since
   a cache entry fetched from the git host keeps serving those bits.
3. **Prefer the lowest unmoved patch successor** (`x.y.z` -> `x.y.z+1`). Never
   jump a major; never go above `v1.x.x`.
4. If a module has no version whose host content, proxy content, and log record
   all agree, that repo's owner must cut one. Do not work around it here.

Tags known to have been force-moved, and how each was resolved:

| Module | Moved tag | Resolution |
|---|---|---|
| `github.com/luxfi/age` | v1.5.0 | pinned v1.5.1 — same commit `b13d4cb0`, unmoved |
| `github.com/luxfi/threshold` | v1.9.4 | pinned v1.9.5 — same commit `0080201`, unmoved |
| `github.com/luxfi/pq` | v1.0.3 | resolved in place: move was content-neutral, `go.sum` carries the log hash |
| `github.com/hanzoai/base` | v1.1.0 | `go.sum` carries the log hash; **owner action** for a version host and log agree on |
| `github.com/hanzoai/kms/sdk/go` | v1.0.0 | graph-only, not in `go.sum` |
| `github.com/luxfi/mpc` | v1.14.13 | graph-only, not in `go.sum`; host and log disagree |
| `github.com/luxfi/precompile` | v0.5.37 | graph-only, not in `go.sum`; host and log disagree |

`luxfi/age` and `luxfi/threshold` are the pattern to copy: a successor tag cut at
the *identical commit*, so pinning it changes no code and restores corroboration.

Two entries needed more than a successor tag:

- **`luxfi/pq` v1.0.3** moved from `e16b004d` to `90d2223e`, but the two commits
  have byte-identical trees, so the zip hash never changed. The proxy, the log,
  and the git host all report `h1:pFlQm1+5...`; `go.sum` had recorded
  `h1:ksw1dmfT...`, which corresponds to nothing obtainable and made a
  cold-cache build fail outright. `go.sum` now carries the log hash. No
  successor tag is needed.
- **`hanzoai/base` v1.1.0** moved from `fe516fdc` to `a0b4a9f3`, and the content
  did change — in exactly three files, `README.md`, `docs/NETWORK.md`, and
  `docs/NETWORK_RED_REVIEW.md`. **No Go source and no `go.mod` differs**, so the
  two trees compile identically. `go.sum` carries the log hash
  (`h1:Mi4yRgR1...`, the `fe516fdc` tree). A machine whose module cache already
  holds the `a0b4a9f3` bits will report a mismatch until that entry is evicted;
  a clean cache resolves it from the proxy. The durable fix is for the owner to
  cut `v1.1.1` at `a0b4a9f3` so host, proxy, and log agree on one version.

## Third-party notices

The distributed artifacts carry `THIRD-PARTY-NOTICES`: the license, notice, and
patent-grant texts of every module the linker puts in `amld`, reproduced
verbatim. Apache-2.0 §4(d) requires the attribution notices of Apache-licensed
dependencies to travel with each distributed copy, and 10 of the linked modules
ship a `NOTICE` file; MIT/BSD/ISC require their copyright and permission notices
to travel the same way.

`go run ./internal/notice` generates it from the build's own module graph, so it
cannot describe a dependency set the artifact does not have and cannot go stale
when a dependency changes. Output is deterministic — modules sorted by path, no
timestamps — so it does not perturb a reproducible build.

It is generated, not committed (`.gitignore`), and produced on all three
distribution paths: `make build` (via the `notice` target), the goreleaser
archive (a `before` hook plus an `archives.files` entry), and the container image
(build stage, copied to `/app/THIRD-PARTY-NOTICES` beside `/app/LICENSE`).
Regenerate with `make notice`; verify by inspecting a built archive or the image
filesystem, never by reading the config.

## Deployment

- Single binary: `amld serve --http=0.0.0.0:8090`
- Docker: `ghcr.io/luxfi/aml:{env}`
- K8s: Deployment, replicas=1 (SQLite single-writer), PVC for /data
- Replication: hanzoai/replicate sidecar for age-encrypted S3 WAL streaming

## Test Coverage

133 tests across 8 packages. All pass with `-race`.

- engine: 38 tests (evaluator, scoring, integration)
- cases: 28 tests (CRUD, status, events, assignment, resolution)
- sanctions: 24 tests (Jaro-Winkler, normalize, token match)
- rules: 21 tests (all 20 starter rules compile + individual eval)
- chain: 7 tests (Chainalysis client: entities, transfers, webhooks)
- workflows: 7 tests
- webhook: 5 tests (sign, verify, deliver, failure)
- api: 3 tests
