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
  src/
    main.tsx               -- React 19 entry
    gui.ts                 -- The kit, and the one way its stylesheet reaches the DOM
    app.tsx                -- Shell: brand from /v1/aml/config, PKCE gate, 6 routes
    app.css                -- The one stylesheet: the kit's theme under this
                              file's names, plus the arrangements the kit has no
                              equivalent for. No inline styles.
    config.ts              -- API origin derived from the host; clientId from the API
    api.ts                 -- One function per route the engine serves
    ui.tsx                 -- The screens' whole vocabulary, on @hanzo/gui
    pages/
      overview.tsx         -- Readiness, coverage, the published gaps, model state
      cases.tsx            -- Queue, detail panel, timeline, resolve with rationale
      rules.tsx            -- Visual builder over the engine vocabulary + replay
      flow.tsx             -- Evaluate or score a transaction; alerts by transaction
      sanctions.tsx        -- Screen a party (agree/conflict), list readiness
      relationships.tsx    -- Art. 78 lookback as a graph; open and close
  e2e/
    session.ts             -- A signed-in console, seeded the way the SDK stores one
    serve.mjs              -- Serves dist under csp.txt, with a mock API on-origin
    console.spec.ts        -- The served CSP, signed in, on every screen the rail offers
    kit.spec.ts            -- Whether the kit actually reaches the screens
    origin.spec.ts         -- apiOrigin over 22 hosts
  csp.txt                  -- The policy under test, byte-identical to the chart
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
  lists/                   -- The institution's own allow and deny lists, and the
                              one rule term over them: Listed(name, value)
  suppress/                -- The decisions to keep a detection out of a queue:
                              reason, decider, window, and the narrowest-wins cover
  watch/                   -- Every activation as it fires, the rungs that raise a
                              response, the fold that marks a repeat, the live feed
  dictionary/              -- What a tenant's payloads carry: declared fields by
                              reflection, the tenant's own Raw keys, fill, blindness
                              and a distinct count kept as a bitmap and never values
  topology/                -- The space of model shapes, searched over a tenant's
                              own history. Pure: no store, nothing it could write to
    budget.go              -- The machine's share for study work, so ingest keeps
                              the rest. Tokens ARE trial workers, taken before the
                              history is read; the wait is the queue and it cancels
  models/                  -- The record of every search and every fitted state, and
                              the one path that adopts one into the live model
  receipt/                 -- What an institution was told about a transaction,
                              kept so that offering it again returns the same
                              answer instead of computing a second one
  roster/                  -- The set of tenants a process holds live state for,
                              and the ceiling on how many. Admits, never removes
  api/wire.go              -- THE assembly: every collection, every shelf, every
                              join, and what runs on a cadence. One function,
                              called by cmd/amld and by the tests alike
  api/load.go              -- One door for "is anything of mine quietly degraded"
  api/typed.go             -- One operation shape, two adapters: the tenant is an
                              argument, the input is one struct, the output another
  api/gate.go              -- Admission for the expensive work: one per tenant, and
                              a deadline, as two combinators over the same shape
  api/planes.go            -- The five planes on the router, and the one place
                              ingest meets them
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
  brand/tenant.go          -- The tenant key <brand>/<org>: Qualify, Qualified and
                              the boundary check every record plane calls
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
| GET | /v1/aml/load | What this engine holds for the caller's tenant, and what it has had to let go |
| GET | /v1/aml/health | Health check, 503 when records cannot be kept |
| GET | /v1/aml/config | Brand identity of the request's Host: brand, display name, issuer, domain |
| GET/POST | /v1/aml/lists | The tenant's declared lists / declare one |
| GET/POST | /v1/aml/lists/{name}/entries | Read a list / put values on it |
| POST | /v1/aml/lists/{name}/entries/remove | Take a value off; the row stays |
| GET | /v1/aml/lists/{name}/lookup | Is this value listed, and on what entry |
| GET/POST | /v1/aml/suppressions | The suppression ledger / declare one |
| POST | /v1/aml/suppressions/{id}/lift | End one, with its own reason and decider |
| GET/POST | /v1/aml/activations | The activation feed / record one |
| GET | /v1/aml/activations/rates | Per rule: fired, live, silenced, folded, elevated |
| GET/POST | /v1/aml/rungs | The declared ladder / declare a rung |
| POST | /v1/aml/rungs/{id}/retire | Retire one; the row stays |
| GET | /v1/aml/dictionary | What this tenant's payloads carry, and what is blind |
| GET/POST | /v1/aml/models/runs | Past searches / run one over this tenant's history |
| GET | /v1/aml/models/runs/{id} | One whole search: every trial, every curve |
| GET/POST | /v1/aml/models/fits | Fitted states / fit one under a shape |
| POST | /v1/aml/models/fits/{id}/adopt | Install a fit into the live model |

Every route above is `get(h, <plane>.<Op>)` or `post(h, <plane>.<Op>)` and nothing
else — see "The five planes" below.

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

## The five planes

Five capabilities an institution operates its monitoring programme through, each
its own package, each a durable record plane, and none of them able to destroy
anything: disposal is `pkg/retention`'s decision alone (AMLR Art. 77), per tenant,
and every one of these packages has a test that reads its own source for a call to
`Delete`.

| Plane | Package | What it is | What it refuses |
|---|---|---|---|
| Lists | `pkg/lists` | The institution's own allow and deny lists, by class, reachable from a rule as `Listed(name, value)` | A list nobody declared, and an empty value — both would be a rule that is catalogued, reported as coverage, and incapable of firing |
| Suppression | `pkg/suppress` | The decisions to keep a detection out of a queue: reason, decider, window | A suppression naming neither a rule nor a subject: that is a kill switch, not a suppression |
| Watcher | `pkg/watch` | Every activation as it fires, what became of it, and a live feed over the durable rows | An activation with no subject — it would pool every anonymous detection under one imaginary customer |
| Dictionary | `pkg/dictionary` | What a tenant's payloads carry: fill, blindness, distinct-count, and the model's own inventory | Nothing. It is a diagnostic and must never be able to refuse a payment |
| Models | `pkg/topology` + `pkg/models` | The space of model shapes searched over a tenant's own history, and the record of every search and fit | A winner nobody can justify, and a fit installed into a model of another shape |

### The decider is the credential, never the body

Every governed act on these planes — declaring a list, putting a value on one,
suppressing a detection, lifting a suppression, declaring a rung, retiring one,
running a search, fitting a shape, adopting a fit, closing a case — is asked for a
reason and a decider so that a supervisor asking "who turned this off" has an
answer. That answer is worth nothing if the caller writes it.

So the decider is a DISTINCT TYPE, `types.Decider`, and it is the one input a
caller does not supply. Fields of that type carry `json:"-"`: the body decoder, the
query binder and the path overlay all skip it, and `pkg/api/typed.go` (`decide`)
writes the AUTHENTICATED SUBJECT onto it after the body and after the path. A
caller that sends `"by":"the head of compliance"` has written a field that does not
exist. `pkg/api/tenant.go`'s `Caller` is the two projections of one verified
credential — the tenant that indexes every store, and the subject that names a
decision — and both come from the same signature (`iam.go`, the token's `sub`).

The value is the SUBJECT rather than a display name, because attribution must
survive a rename. Where the deployment's identity cannot name one, authentication
still succeeds — reads and ingest are the tenant's work, not a person's — and every
operation that would record a decision refuses with the plane's own `ErrDecider`:
a decision naming nobody is refused rather than recorded.

### Suppression is a marking, never a drop

A detection a suppression covers, or a repeat inside a fold window, is WRITTEN and
marked with the reason it did not reach a queue. `Action` is what the rule asked
for; `Response` is what the plane concluded. A monitoring system that discards what
it decided not to show cannot answer how much it is not showing, and "no alerts"
then means either a quiet institution or a silent control with no way to tell
which. Because the suppression's id is on every activation it covered, "how much
has this silenced" is a query — which is why nothing keeps a counter of it.

### Elevation and folding are one mechanism

A RUNG is a tenant's declared policy: when this rule has fired this many times on
one subject within this window, do this. `To: watch.Fold` marks the repeat as a
duplicate; `To: <action>` raises the response. A rung can only RAISE — lowering is
a suppression by another name, and a suppression is asked for a reason and a
decider that a rung is not asked for on every activation it touches. Elevation
beats folding, because an activation that reached an escalation rung IS the
escalation and not a repeat of one. With no rung declared nothing folds: silence is
a decision, never a default.

A rung's `Count` becomes the LIMIT of a read on the INGEST path — the streak query
reads exactly as many prior activations as the deepest declared rung needs — so it
is bounded (`MaxCount`, `MaxWithin`) at declaration, where a refusal costs one
operator request, and clamped again where it is USED, because a bound introduced
after a row was written does not reach that row. Undeclared, a count of ten million
turns every activation of that rule into a ten-million-row scan of the tenant's own
store, on the path that has to answer before a payment can be taken.

### One assembly, and both the deployment and the tests use it

`pkg/api/wire.go` is the only place this engine is built: every collection
(`Ensure`), every shelf (`NewBase`), every join between them, and everything that
has to run on a cadence for the durable state to stay honest. `cmd/amld` calls
`api.Wire(app, api.Deployment{…})` and supplies only what an INSTALLATION answers
differently — who a caller is, which IAM application this is, what it detects,
its key material, its designations, its business day, its rates. The tests build
the same function.

That is a correctness property and not tidiness. The previous review's defining
failure was two wirings: `cmd/amld` assembled a Handler by hand and the tests
assembled a different one, so the record fingerprint could be a struct field the
tests exercised and no column stored — every retry a permanent 409 in production
while every test passed. The bug was not in the ledger; it was that there were
two arrangements and only one of them was under test.

Three tests keep it that way, each doing one thing:

| Test | What it refuses |
|---|---|
| `TestTheDeploymentDoesNotAssembleItsOwnHandler` | an `api.Handler` composite literal anywhere in `cmd/amld`, read from the AST (`internal/source.NoLiteral`) |
| `TestWireLeavesNothingUnwired` | any exported `Handler` field left nil, empty or zero after `Wire`, found by reflection rather than by a list that would be a third copy |
| `TestWireCreatesEveryCollectionItWritesTo` | a missing collection, by ingesting one transaction through every plane |

### A retry is not a second transaction

`pkg/receipt` is the identity of an OFFER, resolved once, at the top of ingest,
before anything is read or written — and the answer that was given for it.

Every number this engine exists to compute is a count of transactions over a
window, so a retry that is counted is an aggregate wrong by the number of times
the network failed. Worse: the second offer of an UNCHANGED transaction saw a
window it had already put itself in, the verdict flipped from allow to block, and
`retain` filed a REFUSAL under AMLR Art. 77(3) — a regulatory record of a refusal
the institution never made, and a customer's payment declined, because a client
retried.

The ledger was idempotent. Nothing else was, and an engine whose ledger holds one
record while its aggregates hold three is worse than one that double counts
everywhere, because the numbers disagree and nothing says which is right.

- The offer is `(Ref, Mark)`. `Mark` digests the request body decoded and
  re-encoded, so a client that re-serialises is recognised; it covers nothing this
  engine derived, because a digest over a value this process invented differs on
  every offer — which is precisely the defect that made every retry a permanent
  conflict once before.
- A prior receipt is returned **byte for byte**. A client cannot tell a retry from
  a first offer, which is what idempotent means.
- Two different transactions under one reference is not a retry. It is refused
  (`409`), never answered from the wrong one, and never on a 5xx: `503` reads as
  "retry", and a client retrying a permanent refusal retries forever.
- The shelf is durable because the deployment is Recreate at one replica: the
  process that answered the first offer is gone by the time a client retries
  across a rollout.
- Receipts are kept for `Window(widest aggregate) = widest + 24h`, DERIVED rather
  than chosen, so tuning a window cannot leave the receipt outlived by the
  aggregate it protects. Disposal is a daily cron and destroys nothing an
  obligation covers.

**And every plane past it recognises the transaction for itself.** The receipt is
written after the work and before the response, so a process that dies in between
leaves the work done, no receipt, and a caller who — never answered — offers
again. The receipt cannot catch that one, so nothing relies on being sequenced
behind it: the ledger has its fingerprint, an activation's id is
`hash(tenant, tx, rule, subject)`, `history.Base.Append` does not append a second
row for a transaction it already holds, `AlertStore.add` keeps the first
judgement, and a transaction that has already been judged does not open a second
case. Velocity is not in that list because it is in memory — the crash that loses
the receipt loses the aggregate too. `TestAnInterruptedOfferIsStillCountedOnce`
produces exactly that state and checks all of them.

### A bound over a count of caller-sized things is not a bound

Three bounds in this engine are stated in BYTES, and each derives its count from
its cost in one place so the two cannot disagree:

| Bound | Where | Worst case weighed by |
|---|---|---|
| `types.MaxIdent` (256) | every identifier a transaction carries, refused at the door | `TestAnOversizedIdentifierIsRefusedAtTheDoor` |
| `velocity.Store.Ceiling()` | tenants × keys × `keyCost(windows)` | `TestTheCeilingIsInBytesAndItIsHonest` |
| `dictionary.Ceiling()` | `(MaxCustom + declared) × nameCost()`, where `nameCost` needs `MaxName` (128) | `TestOneNamesCeilingIsInBytes` |

`MaxIdent` is what turns every count downstream into a figure in bytes: a
sliding-aggregate key is a fixed ring plus that string, a catalog name is that
string, a retained record's slot is that string. Refusing is right where
truncating is not — an identifier this engine shortened is an aggregate kept
under a key naming a different account.

### Per-tenant state, per-tenant bounds, and no eviction anywhere

`pkg/roster` is the set of tenants a process holds live state for. It **admits and
never removes**: there is no `Drop`, no evict, no `delete` in the package, and
`internal/source.NoRemoval` reads the file to keep it that way. A tenant that is
held keeps what it holds; a tenant arriving when the roster is full is REFUSED,
and the refusal is counted and published.

The banned shape is one map of every tenant's state under one cap with LRU
eviction. It is a defect every time: the cap is spent from a pool every tenant
draws on, so a busy institution's traffic takes a quiet one's CONTROL — its
aggregates return to zero and its model returns to warming, both of which read
exactly like an institution with nothing to report. No error, no log. It is also
cheap to cause on purpose, from any tenant, with ordinary traffic.

`velocity.Store`, `anomaly.Store` and `dictionary.Shelf` all hold their
per-tenant state in a roster, and each has a `source.NoTable` test refusing a
string-keyed map on the type. `roster.Default` is ONE answer to how many
institutions this process holds state for, so every per-tenant bound multiplies
by the same figure and the products add up against a pod's memory limit.

The same rule applies to a table keyed by anything else a caller writes.
`engine.Evaluator` holds NO compiled rules for that reason: a candidate rule's
text arrives on the wire at `/v1/aml/rules/test`, so a compiled-rule cache would
be keyed by a string a caller wrote, sized by how many distinct ones a caller
sends, with no cap and no removal, in the memory every institution's ingest runs
in. A rule's compiled form is a VALUE — `engine.Ruleset` — belonging to whoever
asked for it: the engine's owner is the installed library, a replay's owner is the
one request, and both are dropped with their owner.

### Reaching a bound is allowed; reaching it quietly is not

Every live bound here can bind, and when one does an aggregate stops being kept,
a model is not planted, or an observation is turned away. None of those raises an
error, because refusing a payment because a cache is full would be the worse
failure. What is NOT allowed is for it to happen silently: an aggregate that reads
zero is what a clean account also reads, and a model that is warming reports
nothing, which is what a clean institution also reports. A control that switches
itself off without saying so is worse than no control, because it is a control
somebody is relying on.

So there is ONE door — `GET /v1/aml/load` — per tenant, answering in words rather
than in ratios a reader has to interpret: the tenant's aggregates against its own
ceiling in bytes with a grade (`clear`/`crowded`/`full`), whether it has a
behavioural model at all, the field catalog's own pressure, and the process's
counts of institutions held, refused and turned away. Nothing there names another
institution.

### The machine is one budget, and the expensive read demands its receipt

`topology.Budget` is half the cores for every study, replay and fold together;
the other half is ingest's. Tokens ARE trial workers and the hold is taken by the
CALLER THAT READS, not the callee that computes — that distinction is the whole
of the memory property, because the read is a tenant's whole retained history
opened record by record, and a token taken after it bounded nothing. Eight tenants
once held eight full histories at once against a budget of one worker.

`Handler.history` is the one function that materialises a whole retained history,
so it REQUIRES a `*topology.Grant`, and a `Grant` comes from `Budget.Admit` and
from nowhere else. An ungated whole-history read is therefore something a caller
has to go out of their way to write rather than something they can forget.
`api.costly` puts the same budget in front of a fold that is not a page —
`GET /v1/aml/activations/rates` examines up to `MaxExamined` rows however few the
caller asked for, and row-bounded is not rate-bounded. A paged read is
deliberately NOT wrapped: a page is bounded work with a cursor, and putting the
machine's budget in front of every list would make an ordinary console screen wait
behind a study.

### Ingest degrades; it does not refuse for volume

Two things run on the ingest path once per activation, and neither may be a way for
a tenant to stop its own payments.

A cover check reads this tenant's suppressions bearing on the rule. Past
`MaxCandidates` it answers over what it read and MARKS the answer
(`Cover.Partial`, `Activation.Unchecked`) instead of failing. The crowding is
refused at DECLARATION instead (`MaxInForce`, `ErrCrowded`), where a refusal costs
one operator request rather than every payment. Degrading in that direction
produces an alert a suppression would have silenced — noise, which a reviewer
dismisses — where refusing produces silence, which nobody sees.

An activation naming a transaction IS one rule firing on one subject in one
transaction, so its id is derived from `(tenant, transaction, rule, subject)` and
the row is looked up before anything is computed. With a fresh id each time, a
retry writes a second row, the second row is counted in the streak, and a declared
repetition policy fires on a repeat that never happened — a payment blocked
because the client retried.

The retained record is idempotent by the same means: `token.Vault.Fingerprint`
names the plaintext deterministically (a seal draws a fresh nonce, so sealed bytes
never match), it is a COLUMN and not only a struct field, and the transaction
carries ONE clock — its own. A reception clock stamped at ingest made every retry
a different fact under one id, a conflict no retry could ever clear. A fingerprint
that was a struct field no column stored had the same effect on the durable shelf
and none on the memory one, which is why every identity test now runs on the shelf
`cmd/amld` wires. See "A retry is not a second transaction" for the identity that
gates all of it.

### The dictionary stores no value, ever

A distinct-value count is a bitmap sketch (linear counting, 4096 bits per field per
tenant), hashed with the tenant in the key so two tenants' bitmaps cannot be
compared to learn whether they share a customer. Identifiers belong in the retained
record plane, sealed and purpose-gated; a statistics table does not get a second
copy of them under a weaker regime. The estimate SATURATES — "at least this many,
and no longer a count" — rather than drifting down, because a cardinality that
silently stops rising reads as a field that stopped varying.

That holds for numbers too, and numbers are where it is easiest to lose. A minimum
IS a payload value, exactly, at any volume; at a count of one so are the sum and
the mean derived from it. A DECLARED number is a measurement of the transaction
model this engine defines — a notional, a converted amount, a score — and its range
is the statistic a reviewer asks for, so it keeps its moments. A CUSTOM number is
whatever the institution put under its own key, and nothing here knows whether that
is an amount or a national identifier, so it gets the sketch and nothing else: min,
max, mean and deviation are ABSENT rather than zero.

The catalog is write-behind: observations accumulate and are written on a cadence
and at shutdown. That is deliberate — a field statistic is not a compliance record,
and paying an indexed write per field per transaction would spend the ingest path's
latency on a diagnostic. What is not acceptable is silence about it, so `Pending`
is published: a restart loses exactly that many observations and the answer says
so.

The vocabulary is bounded PER TENANT (`MaxCustom`), in the accumulator and in the
stored rows alike. `MaxKeys` bounds one payload used as a bag; it does not bound a
tenant that sends a different key on every transaction, and that tenant's growth is
in the memory every other institution's ingest runs in. Reaching the bound degrades
only the tenant that reached it — no error, no refused payload, the names it
already has keep measuring — and `Crowded` publishes the readings turned away,
because a catalog that quietly stopped taking names would report a payload surface
the institution does not have.

### A search names no winner it cannot justify

Ranking model shapes needs an outcome, and the only honest one is whether a shape
separates the events a human judged productive from the events a human dismissed
(the area under the ROC curve of its scores against those dispositions). Where
nothing has been judged, `topology.Search` reports every trial and NO winner, with
the reason. A ranking invented from unlabelled data — most alerts, fewest alerts,
best-behaved threshold — would look like a recommendation and would be a
preference. Ties go to the smaller model: a shape that ties on evidence and costs
less is the better answer, and preferring the bigger one drifts the fleet upward
every time a search runs.

`pkg/topology` has no store and imports nothing that has one, exactly as
`pkg/replay` does not — and a test reads its imports to keep it that way. A trial
builds its own detector and its own aggregate store, uses them, and drops them.

`models.Fit` produces learned state, and `anomaly.Restore` will only install it
into a model of the SAME shape (the digest). That is a governance property, not a
limitation: a tenant's model shape cannot be swapped underneath it by restoring a
file. The learned state itself never leaves the store — mass counters describe
where a tenant's activity is dense, and handing them out over an API would publish
the shape of an institution's customer behaviour to whoever holds a token.

### A study is admitted, bounded, cancellable — and ingest keeps the machine

A search replays a tenant's history through up to `MaxTrials` candidate detectors
and a fit replays it through one. Either is minutes of arithmetic asked for by one
request, on a single-replica process that also has to answer ingest, and a
transaction that cannot be recorded cannot be processed. Four bounds, each doing
one thing:

| Bound | Where | What it stops |
|---|---|---|
| One study per tenant | `api.gate`, `one(...)` | a tenant queueing studies against itself. Per tenant and never global: one institution's study is never refused because another is studying, and nothing is ever evicted from the map |
| A deadline (`maxStudy`, 2 min) | `api.within(...)` | a legitimate space whose total is hours. Derived from the REQUEST's context, so a client that goes away cancels the work |
| `topology.Budget` | wired in `api.Wire` | every study, replay and fold together taking the machine. Tokens ARE trial workers, taken by the CALLER THAT READS before the history is read, so CPU, the events held in memory, and the queue are one mechanism. `Handler.history` refuses without a `*topology.Grant`. Half the cores, so the other half is ingest's. The wait is the queue and it is cancellable |
| `MaxWorkers`, `MaxTrials`, `MaxEvents` | `pkg/topology` | one request naming the width of its own study |

The budget is a bound on the PROCESS and is the one thing here that is not per
tenant — deliberately, because the CPU is shared however it is modelled, and
waiting for the machine is not the same as being refused because of another
tenant. A rule replay is admitted the same way: one per tenant, and the same
deadline.

Every fit records what it cost (`Fit.Elapsed`), as every run already did
(`Report.Elapsed`), so a tenant's spend on the expensive plane is answerable from
what was kept rather than from a counter a restart resets.

### An adopted control cannot go quiet on its own

Adoption is the one governed act whose EFFECT was not durable. The fit is a row and
the adoption is a row, but what adoption DOES is install learned state into a model
that lives in memory — and this deployment is one replica with a Recreate strategy,
so a rollout empties it, and the live store holds a bounded number of tenants'
models at once, so an idle institution's can be dropped for a busy one's. Neither
is a decision anybody took, and both return the tenant to warming, which reports no
alerts, which is what a quiet institution also reports.

So `anomaly.Store` asks `models.Shelf.Adopted` when it plants a tenant's model:
per tenant, one indexed read, at most once per planting, and the installing is the
model's own `Restore` with its own digest and mass checks. `State.Planted` and
`State.Restored` publish the rest — when the model a tenant is being scored by
started, and whether it started from an adoption — so a reset is visible from
outside instead of being inferred from a silence.

### One operation shape, two faces

Every operation on every plane is

```go
func(ctx context.Context, org string, in *In) (*Out, error)
```

— the tenant is an argument and never something the operation resolves for itself,
the input is one typed struct, the output is another. `pkg/api/typed.go` is the
whole of what turns one into an HTTP route: `get` binds the input from the query
string, `post` from the body, both driven by the same json tags, and the route's
own path parameters are bound last and win. `pkg/api/planes.go` is then a routing
table with no handler bodies in it, and a test reads it as source to keep it that
way.

The point is that the CONTRACT is the pair of Go types and nothing else. The cloud
mount wraps the same functions as zip typed operations — where the same In and Out
become the OpenAPI schema, the CLI, the SDKs and the MCP tools — with no line of
the contract restated. See "A native zip conversion" below for what that costs.

### The tenant key, and where it is checked

`brand.Qualify(brandID, org)` mints `<brand>/<org>` and `brand.Qualified` is
derived from it, so there is one definition of the shape. `pkg/api/tenant.go`'s
`qualify`/`qualified` are aliases of those, not a second implementation.

Every one of the five planes calls `brand.Tenant(org)` at its own boundary before
it writes. The check is in the planes and not only where the key is minted because
the identity comes from a seam the DEPLOYMENT supplies: this is where the value
crosses into a store index, and an unqualified org reaching one puts two brands'
institutions of the same name into one tenant. Each plane has a
`TestBareOrgIsRefused` over every operation and a `TestTenantIsolation` that uses
the same org name under a second brand — which is the case a bare-org regression
collides.

## A native zip conversion

The typed operations above were written so that the cloud mount is a wrap. What a
full native-zip conversion of this engine would take, measured against the code as
it stands:

**Already done, and it is most of the work.** The 19 plane routes are typed
operations over 24 In/Out struct pairs with json tags on every field. A zip face
over them is one line each:

```go
zip.Post(g, "/lists", ops.declare, zip.WithOperationID("amlDeclareList"), zip.WithTags("aml"))
```

where `ops.declare` reads the tenant from the validated principal and calls
`lists.Shelf.Declare` — the same function this repo's Base router calls. Nothing in
the contract is restated.

**What is left, and it is the older half.** The 19 pre-existing routes in
`api/routes.go` are `func(*core.RequestEvent) error` closures that decode an
anonymous struct inline, so each one's request shape exists only inside its
handler. Converting them is: lift each anonymous struct to a named In type, lift
each `e.JSON(...)` literal to a named Out type, and move the body to
`func(ctx, org, *In) (*Out, error)`. `POST /v1/aml/transactions` is the largest —
its In is the embedded `types.Transaction` plus `relationship` and `entity`, and
its Out is the anonymous `EvalResult`+`record` struct at routes.go. Roughly 19
types to name and 19 handler bodies to move; no logic changes and no behaviour
changes.

**Four things a conversion must decide rather than discover.**

1. **Schema names are flat across the fleet.** Every type this repo would publish
   needs a prefix — `amlDeclareListIn`, `amlActivation`, `mlSearchRun` — because a
   weave refuses one name with two shapes and binds an SDK to whichever it read
   last. The plane types here are currently named for their package
   (`lists.DeclareIn`, `watch.DeclareIn`) and are distinct only by import path,
   which a flat namespace does not have.
2. **`GET /v1/aml/health` cannot be a typed operation.** A real probe answers 503
   carrying the degraded report AS ITS BODY (`readiness.go`), and a typed operation
   reaches a non-2xx only by returning an error, which renders as the framework's
   envelope and drops the report. It stays untyped, deliberately, and belongs on
   whatever closed list the mount keeps of routes that are untyped by design.
3. **The tenant must come from the principal, not from an In field.** Every
   operation here already takes `org` as its first argument for exactly this
   reason. A conversion that put the org on the In struct would make it
   caller-supplied, which is a cross-tenant read the caller asserted for itself.
4. **So must the decider, and it already is.** `types.Decider` fields carry
   `json:"-"`, so they are not part of the published schema and no generated CLI,
   SDK or MCP tool offers a `by` argument — which is correct, because there is
   nothing for a caller to pass. The mount does what `pkg/api/typed.go` does:
   decode, overlay the path, then write the validated principal's subject onto
   every decider field. A conversion that published `by` would put the field back
   on the wire in the generated surface even though this engine ignores it.

**What does not convert.** `watch.Shelf.Subscribe` is a channel, not a request. A
live feed is a transport concern (server-sent events, or a ZAP stream) over the
same durable rows that `GET /v1/aml/activations` pages; it is not a typed operation
and should not be made to look like one.

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
| `AML_ANOMALY` | `live` leaves shadow mode. Anything else scores without contributing. The assembly states it from the LIVE side (`api.Deployment.Live`) so the zero value is shadow — a default that armed the statistical plane for anyone who did not think about it is the one direction a default must not fail in |

The five planes take no configuration. A list, a suppression and a rung are all
tenant DECLARATIONS carrying a reason and a decider, so none of them is a switch an
operator flips in a manifest — which is the point: a control that can be turned off
by an environment variable has no record of who turned it off.

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

There is no deploy-time configuration at all. The console derives its API from
the host it is served on (`aml.<domain>` -> `api.<domain>`; a bare domain or an
address literal means same origin, which is what the dev proxy serves) and reads
its brand, display name, issuer AND IAM `client_id` from that API's
`/v1/aml/config`. No config.json, no SPA_* env, no baked clientId — so there is
no setting that points authenticated requests anywhere, and one image is every
brand's. Nothing writes the served root, so the container runs
`readOnlyRootFilesystem` and `runAsNonRoot`.

Auth is **`@hanzo/iam/browser`** — the SDK every Hanzo app uses, not a local
copy of it. `configureIam({issuer, clientId, redirect})` with both values from
`/v1/aml/config`, then `startLogin` / `handleCallback` / `getSession` /
`getUser` / `logout`. There is no auth code in this repo to review, drift or get
wrong: PKCE, token storage, refresh and RP-initiated logout are the SDK's, so
when IAM changes every app gets it.

Verified against production IAM: `hanzo.id` reflects `https://aml.hanzo.ai` on
discovery, token and userinfo, so the flow works cross-origin from the console
host.

Tech: React 19, wouter, Vite 8, `@hanzo/iam` for auth, **`@hanzo/gui`** for the
kit — the Hanzo design system, so this console wears the fleet's identity rather
than a lookalike assembled here. The kit is imported at the part
(`@hanzogui/stacks`, `@hanzogui/button`, …) rather than at the barrel, because
the barrel re-exports components built on react-native primitives this bundle
never renders. One kit, one version pin.

### The kit reaches the screens

`ui/src/ui.tsx` is the screens' whole vocabulary, and it is the ONLY place that
writes an HTML control or names a stylesheet class. A page composes `Row`,
`Col`, `Stack`, `Cols`, `Tiles`, `Split`, `Scroll`, `Body`, `Frame`, `Card`,
`Field`, `Input`, `TextArea`, `Select`, `Button`, `Tabs`/`Tab`, `Badge`, `Tile`,
`Meter`, `Panel`, `Hint`, `Mono`, `Sub`, `Code`, `Kv`, `Go` — and writes no
`<input>`, `<button>`, `<select>` or `<textarea>` of its own.

That boundary is checked rather than intended. `ui/e2e/kit.spec.ts` reads these
sources and fails on a raw control or a stylesheet-class arrangement in a page,
so "the console is on the kit" cannot decay into "the shell is on the kit".

Four things are deliberately not the kit's, each because the kit's answer would
be wrong here (`ui/src/ui.tsx` states each at the code):

| Not the kit's | Why |
|---|---|
| The four state colours | A state must read the same in every theme and in a screenshot of one. It never carries meaning alone: every badge, accent and dot ships with its label |
| The meter | Geometry is an SVG attribute. A width that varies with data is the one thing that would otherwise reach the DOM as an inline style |
| `Cols`, `Tiles`, `Split` | `repeat(auto-fit, minmax(…))`, which reflows on its own width with no breakpoint. The kit's stacks are flexbox; `flex-wrap` strands the last item rather than dividing the row |
| `Select` | The kit's is built on Sheet and Adapt, whose pan responder needs a react-native runtime this bundle does not carry. One native `<select>`, in one place |

Three properties of the kit are worth knowing before changing `ui.tsx`, because
each produced a defect that a build, a typecheck, a CSP run and a render test
all passed, and only looking at the screen found:

1. **`size` is a size token.** The kit derives the box AND the type from it, so a
   button asked for by height comes out with type to match. Box and type are
   given separately, from the console's scale (13px, or 11px when quiet), once.
2. **Button does not forward `className`.** A tone written as a class is dropped
   in silence: the one action a screen exists for looks like every other action,
   and a segment strip shows no selection. Tone is style props.
3. **A custom property is substituted where it is DECLARED.** The kit publishes
   themes as `:root .t_dark` — a descendant selector — so a theme is always a
   subtree and never reaches `:root`. `app.css` therefore bridges the theme on
   `.shell, .gate`, which are inside it. Bridged at `:root` it reads the kit's
   OS-preference default instead, and a control whose ground and ink both come
   through it becomes white on white.
   The kit's default web config is also worth knowing about here: its Button
   sub-theme carries placeholders (`--t0: dark`, `--t1: button`), which are
   names and not colours, so `$color` inside a Button is not a colour. Tone
   reads the bridge, which is where this console reads the theme, once.

`kit.spec.ts` asserts all three as invariants rather than as three regression
cases — a label is measured, a primary button is compared against a plain one, a
pressed segment against an unpressed one, and no control may paint its text in
its own background colour. Each was mutation-proven: reintroduce the defect and
the matching assertion fails.

### The console and the CSP

The served policy carries no `'unsafe-inline'`, and the kit styles at runtime.
That is not a contradiction; it is an arrangement, and it is in one file
(`ui/src/gui.ts`).

`GuiProvider` would render the config's stylesheet as `<style>{getCSS()}</style>`
— an inline style element, whose text `style-src` governs. `disableInjectCSS`
turns that off and the same CSS goes on the document through a **constructed
stylesheet**. CSP does not govern the CSSOM: the script that called it had
already passed `script-src`. The kit's per-component rules take the same route,
into **empty** `<style>` holders, and `'sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU='`
— the SHA-256 of the empty string — is what allows those and nothing else.

One style *attribute* survives, on the kit's theme root. It is written through
`element.style`, which is CSSOM again, so it applies and raises nothing. A style
attribute **parsed from markup** — the shape an injection takes — is refused,
and `ui/e2e/console.spec.ts` asserts that too, as a positive control: "no
violations" proves nothing without evidence that a violation would have been
noticed.

`ui/csp.txt` is the policy under test, byte-identical to the `HANZO_STATIC_CSP`
in `charts/app/values/hanzo/aml-ui.yaml`. The two are the one place each side
states it; changing one means changing the other.

```bash
npm --prefix ui run e2e     # builds, serves under csp.txt, drives Chromium
```

The harness signs in before the first script runs, so what it exercises is the
SIGNED-IN tree — six screens, every control — not the sign-in gate. It walks
whatever the rail offers rather than a hard-coded list, so a screen added
without a test is still a screen it covers. It serves the mock API on its own
origin, which is not a relaxation: a host with no parent domain means same
origin (`ui/src/config.ts`), so `connect-src 'self'` covers it and the policy
stays byte-identical to the deployed one.

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
- Per-org KMS key material. `token.Source` already takes the org, so this is a
  different Source and not a different design; today one root is derived per org
  by HKDF, which means no cross-org correlation but one blast radius.
- Exposing `Ledger.Extend` over HTTP. The cap and the refusals are implemented and
  tested; there is no route yet. The reason there was none is closed —
  `types.Decider` makes the decider the credential's — so this is now a route to
  write rather than a design to decide.
- Tokenising the operational transaction store. `pkg/engine/basestore.go` still
  writes `user_id`, `counterparty` and `ip_address` in the clear for its aggregate
  queries; the retention ledger holds pseudonyms and sealed bodies, so the two
  planes do not agree yet.
- The field catalog's flush is one `SELECT` and up to one `SAVE` per field, per
  tenant, inside one transaction, on the database ingest also writes to. Per-tenant
  bounds make the worst case statable (`MaxCustom` names × `roster.Default`
  tenants per five-minute window) and that is what closed the unbounded version,
  but the aggregate on a one-replica pod is still the largest write this process
  makes. A single upsert per field, or a flush that yields between tenants, would
  remove it.
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

## Durability

Three planes, all Base-backed, all proven across an instance boundary. Nothing
an obligation covers is held in memory: a rollout is not an event the
record-keeping rules make an exception for.

| Plane | Collections | Wired by | Restart test |
|-------|-------------|----------|--------------|
| Retained records | `aml_retention`, `aml_retention_parties` | `retention.Ensure` + `retention.NewBase` | `TestRetainedRecordsSurviveARestart` |
| Receipts | `aml_receipts` | `receipt.Ensure` + `receipt.NewBase` | `receipt.TestRestart` |
| Cases + timelines | `aml_cases`, `aml_case_events` | `cases.Ensure` + `cases.NewBase` | `TestCasesSurviveARestart` |
| Alerts | `aml_alerts` | `api.EnsureAlerts` + `api.NewAlertStoreBase` | `TestAlertsSurviveARestart` |
| Lists | `aml_lists`, `aml_list_entries` | `lists.Ensure` + `lists.NewBase` | `lists.TestRestart` |
| Suppressions | `aml_suppressions` | `suppress.Ensure` + `suppress.NewBase` | `suppress.TestRestart` |
| Activations + rungs | `aml_activations`, `aml_rungs` | `watch.Ensure` + `watch.NewBase` | `watch.TestRestart` |
| Field catalog | `aml_fields`, `aml_payloads` | `dictionary.Ensure` + `dictionary.NewBase` | `dictionary.TestRestart` |
| Model runs + fits | `aml_model_runs`, `aml_model_fits` | `models.Ensure` + `models.NewBase` | `models.TestRestart` |

Each test writes, copies the data directory as it stands, drops the instance,
opens a second one over those bytes, and asks again — including that the tenant
boundary still holds and that case numbering continues rather than restarting.

The memory implementations remain, and are for tests. `cases.NewStore()` and
`api.NewAlertStore()` are not what an instance serves from.

**What is deliberately in memory, and what brings it back.** The behavioural
model's learned state is a cache: its durable home is the adopted fit
(`aml_model_fits`), and `anomaly.Store` reloads it when it plants a tenant's model
(`models.Shelf.Adopted`), so a rollout of a Recreate-at-1-replica deployment or an
eviction to make room for another tenant costs a read and not a control.
`State.Planted` and `State.Restored` say which happened. The field catalog's
accumulator is in memory by design and `Catalog.Pending` publishes what a restart
would lose. Nothing else an obligation covers is held in memory at all.

## Deployment

- Single binary: `amld serve --http=0.0.0.0:8090`
- Docker: `ghcr.io/luxfi/aml:{tag}` (API), `ghcr.io/luxfi/aml-ui:{tag}` (console)
- Hanzo fleet: `charts/app/values/hanzo/aml.yaml` (no host of its own; reached at
  `api.hanzo.ai/v1/aml`, a path on the api Ingress in `hanzo-domains.yaml`) and
  `charts/app/values/hanzo/aml-ui.yaml` (the console at `aml.hanzo.ai`)
- K8s: Deployment, replicas=1 (SQLite single-writer), PVC for /data
- Replication: hanzoai/replicate sidecar for age-encrypted S3 WAL streaming

### What the release smoke has to know about the runner

Three facts, each learned from a release that produced no image at all. Every
v0.3.x tag before v0.3.6 is a tag with nothing behind it for these reasons.

- `amld` lives at `/usr/local/bin/amld`, on PATH. It was `/app/amld`, which
  alpine's PATH does not contain, so the smoke's `command -v amld` exited 127 and
  the release died one line into the check.
- A smoke container shares the job container's network namespace
  (`--network "container:$(hostname)"`) and is probed on its own port. It must
  not publish one: the step runs inside a job container while the docker daemon
  is the runner's, so `-p 127.0.0.1:…` binds in the runner's namespace and the
  loopback in the step reaches nothing.
- Hex comes from `od -An -tx1 | tr -d ' \n'`. `xxd` is not in the runner image.
  It failed quietly to an empty `AML_TOKEN_KEY` — an env prefix on a backgrounded
  command reports the async list's status, not the substitution's — so the CI
  smoke ran green for weeks against a daemon holding no key. The length is now
  asserted before use.

## Test Coverage

Go tests across 26 packages, all green under `-race`, plus 12 browser tests.
`pkg/receipt` and `pkg/roster` are the two new ones; both carry the full plane
suite (tenant isolation, bare org refused, restart over the first instance's
bytes).

- api: 76 · retention: 43 · cases: 40 · sanctions: 39 · engine: 38 · anomaly: 27
- measure: 26 · watch: 22 · dictionary: 17 · topology: 16 · screen: 15 · token: 14
- replay: 14 · models: 14 · suppress: 13 · rules: 13 · lists: 13 · velocity: 11
- brand: 11 · chain: 7 · workflows: 7 · webhook: 5 · store: 4 · history: 3
- lists · suppress · watch · dictionary · topology · models — each carries a
  `TestTenantIsolation` (the same org name under a second brand), a
  `TestBareOrgIsRefused` over every operation, a `TestNothingDeletes` that reads
  the package's own source, and a `TestRestart` that opens a second instance over
  the first one's bytes (`internal/instance`)

**Mutation harness.** `python3 tools/mutate.py` breaks one guard at a time and
reports whether a test noticed: ANCHOR MISS, COMPILE FAIL, SURVIVED, or KILLED. A
property with no mutation is a property nobody has seen fail, and a SURVIVED is a
test that cannot fail. Every bound and every gate above carries one — the study
gate, the worker clamp, the decider binding, the crowding refusal, the partial
cover marking, the firing identity, the record fingerprint, the rung bounds, the
custom-number moments, the vocabulary bound at both ends, and the adoption reload.

The browser suite is `ui/e2e`, run by `npm --prefix ui run e2e` and by CI. It
builds the bundle, serves it under `ui/csp.txt`, signs in before the first
script runs, and drives Chromium. See "The console and the CSP" and "The kit
reaches the screens" below for what each half of it establishes.
