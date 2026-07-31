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
- **Auth**: IAM access token verified in-process (`api.IAMIdentity`) — the brand of the request's own Host decides the issuer, `aud` must be this deployment's IAM clientId, `tokenType` must be `access-token`, and the `owner` claim names the tenant. `api.TrustedProxyHeader` remains for a deployment that can prove a gateway is its only route in
- **Tenant**: `<brand>/<org>` — the brand qualifies the org, in the store index and in the tokenisation vault's HKDF salt alike
- **White-label**: brand per request Host (`pkg/brand`, mirroring HIP-0111) — one `amld` serves lux.network, hanzo.ai, zoo.ngo, pars.network. A Host no brand claims is refused, never defaulted

## Build & Run

```bash
make build                  # Go build -> ./amld (the daemon)
make test                   # go test -race -count=1 ./...
make vet                    # go vet ./...
make ui                     # npm ci && npm run build -> ui/dist (the console)
make run                    # Build + run dev
```

Two artifacts, because they are two deploy units. `make build` produces the
daemon, which serves the API and nothing else; `make ui` produces the console
bundle, which is served by `ghcr.io/hanzoai/static` on its own host. No Node
toolchain is needed to build, vet, test or install the module.

## Module Layout

```
cmd/amld/main.go          -- Single binary: serve, version
migrations/0001_core.sql   -- 9 SQLite collections
ui/                        -- The console: its own bundle, its own image, its own host
  Dockerfile               -- node build -> ghcr.io/hanzoai/static -root /public -spa
  public/config.json       -- placeholder; the static server templates SPA_* over it
  src/
    main.tsx               -- React 19 entry
    app.tsx                -- Shell: brand from /v1/aml/config, PKCE gate, 6 routes
    app.css                -- The one stylesheet, on @hanzo/tokens. No inline styles.
    config.ts              -- /config.json: the API origin and this app's clientId
    auth.ts                -- Authorization code + PKCE against the brand's issuer
    api.ts                 -- One function per route the engine serves
    ui.tsx                 -- Card, Badge, Tile, Meter, Panel, useLoad, formatting
    pages/
      overview.tsx         -- Readiness, coverage, the published gaps, model state
      cases.tsx            -- Queue, detail panel, timeline, resolve with rationale
      rules.tsx            -- Visual builder over the engine vocabulary + replay
      flow.tsx             -- Evaluate or score a transaction; alerts by transaction
      sanctions.tsx        -- Screen a party (agree/conflict), list readiness
      relationships.tsx    -- Art. 78 lookback as a graph; open and close
  dist/                    -- Built output (gitignored)
pkg/
  types/types.go           -- Canonical domain types (Transaction, Entity, Rule, Alert, Case)
  engine/
    engine.go              -- Core engine: evaluate tx -> alerts + score + action
    evaluator.go           -- expr-lang compiler + evaluator with helper functions
    scoring.go             -- Weight-of-evidence scoring + the Scorer seam
  velocity/velocity.go     -- Constant-time sliding aggregates per (key, window):
                              bucketed ring, fixed memory per key, Deviation
  anomaly/
    forest.go              -- Half-space trees: data-independent geometry, mass
                              counters, exponential reference fold, mass invariant
    feature.go             -- The typology->feature inventory (9 dimensions, each
                              citing the instrument behind it) + projection
    anomaly.go             -- Per-tenant model, Appetite, attribution by
                              counterfactual, State, Snapshot/Restore
  rules/library.go         -- 20 starter AML rules (CTR, structuring, velocity, sanctions, etc.)
  sanctions/
    match.go               -- Jaro-Winkler + token-based fuzzy name matching
    lists.go               -- Default list configurations (OFAC, UN, EU, HMT)
    ingest.go              -- OFAC SDN XML parser + HTTP fetcher
  cases/
    case.go                -- Case store (create, update status, assign, resolve, events)
    errors.go              -- Sentinel errors
  retention/
    record.go              -- Record, the three Art. 77(3) clocks, expiry arithmetic
    ledger.go              -- Retain/Close/Extend, party index, purpose-gated reads,
                              five-year lookback, proven disposal
    cron.go                -- Daily disposal at 03:30 UTC
  token/token.go           -- Per-org HKDF key schedule; deterministic pseudonyms
                              for index keys, AES-256-GCM seal for record bodies
  replay/replay.go         -- Rule sandbox: replays a candidate over real history
                              through the engine's own evaluator, writes nothing
  webhook/webhook.go       -- Signed delivery (HMAC-SHA256) with retry + dead-letter
  api/routes.go            -- /v1/aml/* HTTP routes + SanctionsStore on Base
  api/records.go           -- The one place retention, token and replay are joined
  api/anomaly.go           -- Model state for governance + candidate scoring
  api/tenant.go            -- The Identity seam and the tenant key: <brand>/<org>,
                              minted in one place and checked at the boundary
  api/iam.go               -- Identity from an IAM token: Host pins the issuer, aud
                              pins the application, tokenType refuses an id_token,
                              `owner` must be an org the caller belongs to
  api/jwks.go              -- Published signing keys: RSA/EC/ML-DSA-65, per-issuer
                              single-flight cache, refresh + staleness bounds
  api/mldsa.go             -- ML-DSA-65 (FIPS 204) JWT verification
  api/brand.go             -- GET /v1/aml/config: the brand identity of this Host
  brand/brand.go           -- Brand id and request Host -> issuer + domains
                              (HIP-0111; the same registry as hanzoai/cloud brand)
```

## Behavioural detection

Rules catch typologies someone has already named. `anomaly` answers the other
question — is this where this customer's behaviour normally lives — which is the
statutory test, since consistency is assessed against knowledge of *this*
customer and no fixed threshold can express that.

**Detector: half-space trees** (Tan, Ting & Liu, IJCAI 2011). Trees are built
before any data arrives — a random dimension split at the midpoint of the node's
range — so there is no training pass, no sample retained, and no retraining job:
the model is a set of mass counters over a fixed geometry. Scoring walks a fixed
depth. Rejected: isolation forest (batch, so a retraining job per tenant, and
attribution needs a bolt-on), and robust random cut forest (stores the points, and
its attribution is to *other points* rather than to features — the wrong shape for
the question a supervisor asks).

Two deliberate departures from the paper, both recorded at the code:

1. **Votes are averaged, not summed.** `mass * 2^depth` is heavy-tailed, so one
   tree finding the point in a dense region returns a value many times the uniform
   one and swamps every tree that found emptiness. Ranking survives that; a
   calibrated score does not, and an appetite expressed as a quantile needs a
   calibrated score. Each tree is clamped to [0,1] against its own root mass
   before averaging, which also makes the score self-calibrating during warm-up.
2. **The reference folds exponentially** (`Blend`, default 0.25) rather than being
   replaced. `Blend: 1` is the published algorithm. Below 1, making the reference
   look like your own behaviour needs sustained volume against the whole tenant
   rather than one window's worth.

**Why attributability decided it.** HIP-518 §6 admits ML detection only behind a
typology→feature mapping, a stated miss-rate appetite, and per-alert feature
attribution. The third eliminates a neural scorer: its contribution to a score is
not attributable to an input, so it needs a second model fitted to guess at its
own reasons. A tree is interrogated directly — move one coordinate to its neutral
value, rescore, and the drop *is* that feature's contribution, exactly, on the
model that raised the alert. `Cause.Without` carries that counterfactual.

**The appetite is the governed knob.** `Appetite.Review` is the share of the
stream the model may send for examination; the threshold is the quantile of
observed scores that admits it, recomputed each window, taking the upper bucket
edge so the realised share stays at or below the stated one. The miss rate cannot
be computed without labels, so `Appetite.Sample` retains a share of *non*-alerted
transactions for review below the line — selected by hash of the transaction id,
so the sample is reproducible and cannot be steered. `GET /v1/aml/anomaly` reports
stated against realised.

**What it cannot do.** Evidence is capped at `types.ActionCeiling` (review): the
model summons a person, it does not act. Weight is non-negative and NaN/Inf are
rejected, so it can only add to a score the rules built, never subtract. A panic
in the Scorer is contained and counted — the rule plane's verdict stands. Where it
cannot score (warming, unusable coordinates, no subject) the transaction is
evaluated on rules alone and the refusal is counted per reason, because silence
must never read as a clean result.

Models are per tenant, including the tree geometry, seeded from
`mix(cfg.Seed, orgID)`. 336 KB per tenant measured, bounded by `MaxOrgs`
(default 256 → 84 MB). `AML_ANOMALY=live` leaves shadow; until then the model
scores, learns and reports what it *would* have alerted on, changing nothing.

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | /v1/aml/transactions | Ingest transaction, sync rule eval, returns action |
| GET | /v1/aml/transactions/{id}/alerts | Alerts for a transaction |
| GET | /v1/aml/cases | List cases (filter by status) |
| GET | /v1/aml/cases/{id}/events | The case timeline, scoped by the case's tenancy |
| POST | /v1/aml/cases/{id}/events | Add case event (note, status change) |
| GET | /v1/aml/rules | List all rules |
| POST | /v1/aml/rules/test | Replay a candidate rule over history (dry run) |
| GET | /v1/aml/anomaly | Model state: appetite, inventory, threshold, stated vs realised rate, refusals, blind features, below-the-line sample |
| POST | /v1/aml/anomaly/test | Score a candidate against the tenant's model, learning nothing |
| POST | /v1/aml/cases/{id}/resolve | Close a case against a retained assessment |
| POST | /v1/aml/relationships | Open a business relationship |
| POST | /v1/aml/relationships/{id}/close | End one, starting the retention clocks |
| POST | /v1/aml/relationships/search | Art. 78 five-year lookback by party |
| POST | /v1/aml/sanctions/search | Search sanctions lists by name |
| GET | /v1/aml/sanctions/sources | Per-list readiness: count, date, fitness |
| GET | /v1/aml/catalog | Coverage: installed rules, their citations, and the stated gaps |
| GET | /v1/aml/health | Health check, 503 when records cannot be kept |
| GET | /v1/aml/config | Brand identity of the request's Host: brand, display name, issuer, domain |

Every route resolves its tenant through `Handler.Identity`. Exactly two do not:
`/v1/aml/config`, which names the issuer a caller needs in order to obtain a token
— requiring one would be a lock whose key is behind it — and `/v1/aml/health`,
which a probe reads on a Host that names no brand. The rule list and sanctions
search require a tenant too: the first is the map of what this institution's
monitoring looks for, and the second runs a fuzzy match over every designation in
the set, which is not work an unauthenticated caller gets to spend.

Two identities, one seam, one tenant key.
`api.IAMIdentity(api.JWKS(ttl, stale), clientId)` is what `amld` runs: it verifies
the bearer itself, and four things must hold before a token names a tenant.

| Check | Why |
|---|---|
| signature under a key the brand of THIS Host publishes | a Lux token does not authenticate on a Zoo host, even where one in-cluster IAM publishes both brands' signing certificates. A Host no brand claims refuses |
| `aud` contains `AML_CLIENT_ID` | IAM stamps aud = the app's clientId, so without the pin a token minted for a marketing site — or any other tenant's app on the same issuer — is a credential here (RFC 9068 §4) |
| `tokenType == "access-token"` | an id_token is issued to a browser, carries the same `iss` and `aud`, and is not an API credential |
| `owner` ∈ `orgs`, when `orgs` is present | IAM stamps the APP's org as `owner`, so a shared or org-choice application would make all of its users one tenant. A machine token has no membership set: its org is its application's, and the application is pinned by `aud` |

`api.TrustedProxyHeader("X-Org-Id")` takes the ORG from the header a gateway writes
from that same claim — sound only where the gateway authenticates the caller, strips
any client-supplied copy, and is the only route to this service. It takes the BRAND
from the Host either way, so both identities produce the same key.

The AML application must be dedicated to one financial institution: not
`IsShared`, no `OrgChoiceMode`. IAM enforces same-org login for such an app
(`internal/oidc/login.go`), which is what makes `owner` the caller's own org.

## White-label

`pkg/brand` maps a brand id, and a request Host, to that brand's public identity:
its OIDC issuer and the domains it serves on. It is a copy of the fleet's canonical
registry (HIP-0111, `hanzoai/cloud` `brand/brand.go`) kept as a leaf, so one
`amld` answers as Lux on lux.network, as Zoo on zoo.ngo and zoo.cloud, as Hanzo on
hanzo.ai, and as Pars on pars.network — brand, console identity and trusted issuer
all from the Host. A Host no brand claims resolves to NO brand, and there is no
resolver that answers otherwise: the auth path is a caller that must not be handed
a default, or one brand's issuer authenticates every request arriving on a pod IP,
an in-cluster service name, localhost or a misrouted vhost.

Org names are unique within an issuer, not across issuers, so the tenant is the
KEY `<brand>/<org>` and never the bare org. It is minted in one place
(`pkg/api/tenant.go`, `qualify`) and is the same value that indexes alerts, cases
and retained records, scopes a history row, and salts the tokenisation vault. The
salt is the sharp end: keyed on the bare org, two brands' institutions of the same
name derive the SAME keys, so one brand's customer names tokenise to the other's
pseudonyms and its sealed records open under the other's tenant.

The isolation boundary is the deployment: each brand runs its own store, issuer and
ingress host. Brand-qualifying the key is what makes that boundary something other
than the only thing standing between two institutions.

## Records, tokenisation, and the sandbox

Three obligations, three packages, one join in `pkg/api/records.go`.

**Retention (`pkg/retention`)** — HIP-0518 §9. Five years from the end of the
relationship, from the occasional transaction, or **from the date of refusal**
(AMLR Art. 77(3)); a blocked transaction is therefore retained as a refusal with
the rules that refused it, running from its own clock. A record retained inside a
relationship has *no* expiry until `Close` cascades the clock to it. Extensions
are case-by-case, capped at five further years, refused without a reason and a
decider. Reads take a `Purpose` from a closed set, because retained personal data
may be processed only to prevent ML/TF (AMLD4 Art. 41(2)).

*Deletion on expiry against no redaction* — the unit of disposal is the whole
record, never a field. Nothing in the package removes, masks or rewrites part of a
retained record; reads hand out deep copies so a reader cannot either. At expiry
the record is destroyed in full with every index entry that referenced it, and
`Dispose` verifies its own post-conditions before reporting a count — a run that
cannot prove what it destroyed reports the error and nothing else. It also refuses
a date the caller's clock has not reached, so a clock lie cannot bring destruction
forward.

*The five-year lookback* (Art. 78) is answered from a per-party index, not a scan.
`Answer.Examined` is the evidence: it counts one party's records and does not grow
with the ledger.

**Tokenisation (`pkg/token`)** — one root from KMS, per-org keys by HKDF with the
org as salt, per-domain keys by HKDF info. Two operations: a deterministic
pseudonym, which is what the ledger indexes and correlates on, and an AES-256-GCM
seal bound to the org and the record slot, which is what holds the record body. It
is deliberately **not** one-way: a hash-only design can neither reconstruct a
transaction (MLR reg. 40(2)(b)) nor re-screen a customer base against a new
designation (EBA/GL/2024/15 §4.1.4), so it would fail the obligations it is
protecting. Correlation is per org and never across orgs — the same customer in
two orgs is two unrelated keys, so a cross-tenant join is not computable.

**The sandbox (`pkg/replay`)** — JMLSG 5.7.18 requires new typologies to be tested
before live activation, and FCG 3.2.5A requires a retirement to be justified
against the outgoing rule's performance. `/v1/aml/rules/test` replays a candidate
over the org's retained transactions through *the engine's own evaluator*, and
reports how many alerts it would raise, on what, and the added/dropped/kept
difference against the rule it would replace. An empty history is refused rather
than reported as zero alerts, and the false-positive proportion and
intelligence-value are absent rather than zero when nothing was judged. It reaches
the engine through a one-method interface and history through another, so it has
nothing it could write to.

`AML_TOKEN_KEY` carries the KMS-held root — 32 bytes or more, hex encoded. There
is no default. Without it an instance reports itself unfit on `/v1/aml/health` and
refuses to ingest, because a transaction that cannot be recorded must not be
processed.

## Environment

| Variable | Effect |
|---|---|
| `AML_CLIENT_ID` | This deployment's IAM application clientId, pinned as the token audience. **No default: `amld` refuses to start without it**, because an instance that cannot check which application a token was minted for cannot identify a caller |
| `AML_TOKEN_KEY` | KMS-held tokenisation root, hex, ≥32 bytes. No default |
| `AML_DEFAULT_ORG` | Label carried by the rule catalog. The catalog is the deployment's, not a tenant's |
| `AML_BUSINESS_ZONE` | Zone business-day and business-hour rules are answered in. Default UTC |
| `AML_ANOMALY` | `live` leaves shadow mode. Anything else scores without contributing |

## Console (`ui/`)

A compiled SPA, its own image, its own host. `ghcr.io/luxfi/aml-ui` is
`ghcr.io/hanzoai/static` with the bundle in `/public`, served with `-spa` so a
hard refresh on `/cases` resolves. It is NOT embedded in the daemon: the API is
one door (`api.<brand>/v1/aml`) and the console is another (`aml.<brand>`), and
putting them in one pod puts them in one blast radius.

```bash
make ui                                  # npm ci && npm run build -> ui/dist
cd ui && npm run dev                     # port 3000, proxies /v1/aml to aml.hanzo.ai
docker build -f ui/Dockerfile ui/        # what CI builds
```

Screens: overview (readiness, coverage and the published gaps), cases (queue +
timeline + resolve), rules (visual builder over the engine's own vocabulary,
with the replay report), transactions (evaluate or score, alerts by
transaction), sanctions (screen a party, list readiness), relationships (Art. 78
lookback drawn as a graph, open and close).

Deploy-time configuration is two values, templated by the static server into
`/config.json` from `SPA_API` and `SPA_CLIENT_ID` before the first request. The
brand's display name and its issuer are NOT configured — the console reads them
from `/v1/aml/config`, so it can only ever send a user to the issuer the engine
will accept a token from. The bundle holds no secret: sign-in is
authorization-code with PKCE against that issuer, as a public client.

Tech: React 19, wouter, Vite 6, `@hanzo/tokens` for the Hanzo design tokens —
the same values `@hanzo/ui`'s `theme.css` derives from. No runtime style
injection anywhere, so the served CSP needs no `'unsafe-inline'`.

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

- Hanzo Tasks durable workflows (sanctions-refresh, case-automation, backtest)
- Base realtime subscription for live transaction monitoring
- Full OFAC/UN/EU/HMT list refresh automation
- **Base collection persistence** (current: in-memory stores, the retention ledger
  included). A retention ledger that does not survive a restart is a
  record-keeping breach, so this is the first thing to close, and it is what makes
  the five-year lookback and the disposal cron mean anything in production.
- Per-org KMS key material. `token.Source` already takes the org, so this is a
  different Source and not a different design; today one root is derived per org
  by HKDF, which means no cross-org correlation but one blast radius.
- Exposing `Ledger.Extend` over HTTP. The cap and the refusals are implemented and
  tested; there is no route, because the decider's identity would be
  client-asserted (see below).
- Tokenising the operational transaction store. `pkg/engine/basestore.go` still
  writes `user_id`, `counterparty` and `ip_address` in the clear for its aggregate
  queries; the retention ledger holds pseudonyms and sealed bodies, so the two
  planes do not agree yet.
- Attribution. Every decider (`by` on a resolution) is client-asserted: the
  service has a tenant identity but no user identity, so "who dismissed this
  alert" is not authenticated.
- `cases.AutoClose` closes low-severity cases on inactivity without a retained
  assessment — the one path that closes a case without a recorded decision. Needs
  a compliance decision: escalate instead of closing, or record a system rationale.

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
- Docker: `ghcr.io/luxfi/aml:{tag}` (API), `ghcr.io/luxfi/aml-ui:{tag}` (console)
- Hanzo fleet: `charts/app/values/hanzo/aml.yaml` (no host of its own; reached at
  `api.hanzo.ai/v1/aml`, a path on the api Ingress in `hanzo-domains.yaml`) and
  `charts/app/values/hanzo/aml-ui.yaml` (the console at `aml.hanzo.ai`)
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
