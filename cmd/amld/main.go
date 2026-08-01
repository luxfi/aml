// amld is the AML monitoring daemon: real-time transaction monitoring, sanctions
// screening, behavioural scoring, case management and record retention in one
// process over one embedded store.
//
// The wiring order below is the design. Evidence providers are constructed first,
// the engine is built over them, and the rule library is installed last — because
// installing a rule is what checks that its evidence exists, and that check is
// worth nothing if it runs before the providers are attached.
//
//	amld serve      start the server
//	amld version    print the version
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/base"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tools/hook"
	"github.com/spf13/cobra"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/api"
	"github.com/luxfi/aml/pkg/cases"
	"github.com/luxfi/aml/pkg/dictionary"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/history"
	"github.com/luxfi/aml/pkg/lists"
	"github.com/luxfi/aml/pkg/models"
	"github.com/luxfi/aml/pkg/reference"
	"github.com/luxfi/aml/pkg/retention"
	"github.com/luxfi/aml/pkg/rules"
	"github.com/luxfi/aml/pkg/screen"
	"github.com/luxfi/aml/pkg/suppress"
	"github.com/luxfi/aml/pkg/token"
	"github.com/luxfi/aml/pkg/velocity"
	"github.com/luxfi/aml/pkg/watch"
)

var version = "(dev)"

// listStale is how old the newest sanctions load may be before screening reports
// itself unready. The lists refresh daily, so two days means two consecutive
// failures have gone unnoticed.
const listStale = 48 * time.Hour

// How long an issuer's signing keys are held.
//
// keysTTL is the refresh, and it is what bounds how long a rotation takes to be
// honoured: a token signed by a key published after the last fetch verifies against
// nothing until the next one. IAM serves its JWKS with max-age=60, so minutes is
// the right order. keysStale is the outage grace — keys already confirmed are
// served past the refresh while the issuer is unreachable, and past this bound the
// engine refuses rather than authenticating against a set it can no longer see.
const (
	keysTTL   = 5 * time.Minute
	keysStale = time.Hour
)

func main() {
	app := base.New()

	org := os.Getenv("AML_DEFAULT_ORG")
	if org == "" {
		org = "default"
	}

	// The IAM application this deployment IS. AML_CLIENT_ID is its clientId, which
	// is the value IAM stamps as `aud` on every token it mints for it, and pinning
	// it is what makes a token FOR this engine different from any other first-party
	// token on the same issuer.
	//
	// There is no default and no unpinned mode: an instance without it authenticates
	// nobody, and it refuses to start rather than serving while it accepts a token
	// minted for a marketing site. The application must be dedicated to one
	// financial institution — not shared, no org choice — because IAM stamps the
	// APP's org as the tenant claim, and a shared application would make all of its
	// users one tenant. IAMIdentity checks that constraint from the token side too,
	// against the caller's own membership set.
	client := strings.TrimSpace(os.Getenv("AML_CLIENT_ID"))

	// Business-day and business-hour rules are answered in a stated zone. UTC is the
	// default rather than the host's zone, so the same transaction does not evaluate
	// differently on two machines.
	zone := time.UTC
	if name := os.Getenv("AML_BUSINESS_ZONE"); name != "" {
		loc, err := time.LoadLocation(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "AML_BUSINESS_ZONE %q is not a known zone: %v\n", name, err)
			os.Exit(1)
		}
		zone = loc
	}

	// The screening store is built once and shared. The refresh fills it and the
	// endpoints read it; there is no second store for either to talk to instead —
	// two stores is how sanctions search came to answer "no match" for every name
	// while the refresh was loading designations somewhere else.
	screening := screen.New(listStale, nil)

	// Per-source readiness. The store answers whether screening can be relied on at
	// all; this answers which publisher is behind it, with a count and a date,
	// because "not ready" without a source is not actionable.
	readiness := screen.NewReadiness(screen.SourceNames())

	// The record plane. AML_TOKEN_KEY carries the KMS-held root that per-org
	// tokenisation keys derive from: 32 bytes or more, hex. There is no default, so
	// an instance without it reports itself unfit and refuses to process a
	// transaction it could not record — the right failure for a control whose whole
	// job is to have kept the record.
	keys := token.NewKeyring(token.Env("AML_TOKEN_KEY"))

	// The behavioural plane. Sliding aggregates are the substrate every behavioural
	// measure reads; the model reads them to score whether a transaction is unusual
	// for the entity that made it, as a complement to the rules.
	//
	// It starts in shadow. Detection has to be testable before it is activated, so a
	// new deployment scores, learns, and records what it would have alerted on at
	// GET /v1/aml/anomaly, contributing nothing to any transaction's outcome until
	// someone has read that and set AML_ANOMALY=live. The rules are unaffected.
	windows := velocity.New(velocity.Config{})
	model, err := anomaly.New(anomaly.Config{Shadow: os.Getenv("AML_ANOMALY") != "live"}, windows)
	if err != nil {
		log.Fatalf("[aml] behavioural plane: %v", err)
	}

	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(se *core.ServeEvent) error {
			// Refuse to serve unauthenticated. This is checked here, at the start,
			// rather than one request at a time: an instance that cannot identify a
			// caller has nothing it can safely answer, and an operator finds a process
			// that will not start far sooner than one that 401s everything.
			if client == "" {
				return errors.New("refusing to start, AML_CLIENT_ID is not set: it is this deployment's IAM application clientId, and without it no token's audience can be checked")
			}

			// The transaction collection has to exist before any rule reads a
			// window. Without it every aggregate rule reaches no verdict, which is
			// how twelve of twenty rules came to fault on every transaction.
			if err := history.Ensure(app); err != nil {
				return fmt.Errorf("refusing to start, the transaction record cannot be created: %w", err)
			}
			events := history.NewBase(app)

			// The retained record plane, on the same terms as the transaction
			// record above: collections first, then a ledger over them.
			//
			// It is Base-backed and not the memory shelf, which is the whole
			// obligation. Records have to be kept for five years after the
			// relationship ends (AMLR Art. 77(3)); a shelf that empties on restart
			// keeps them until the next rollout, and a `kubectl rollout restart`
			// is not an event the law makes an exception for. The memory shelf is
			// for tests and says so.
			if err := retention.Ensure(app); err != nil {
				return fmt.Errorf("refusing to start, the retained record cannot be created: %w", err)
			}
			records := retention.NewBase(app)

			// The case plane, on the same terms. A case is the record that an
			// alert was considered and what was decided (AMLR Art. 77(1)(b)),
			// and the timeline is the evidence of the work — both have to be
			// there after a restart, so both are Base-backed.
			if err := cases.Ensure(app); err != nil {
				return fmt.Errorf("refusing to start, the case plane cannot be created: %w", err)
			}
			if err := api.EnsureAlerts(app); err != nil {
				return fmt.Errorf("refusing to start, alerts cannot be recorded: %w", err)
			}

			// The five planes the monitoring programme is operated through:
			// the institution's own lists, the suppressions it has decided,
			// every activation as it fires, the catalog of what its payloads
			// carry, and the studies of its model. Every one is Base-backed
			// for the same reason the three above are — a control that empties
			// on a rollout is a control that was off, and from the outside a
			// silent one is indistinguishable from a quiet institution.
			for _, ensure := range []func(core.App) error{
				lists.Ensure, suppress.Ensure, watch.Ensure, dictionary.Ensure, models.Ensure,
			} {
				if err := ensure(app); err != nil {
					return fmt.Errorf("refusing to start, a record plane cannot be created: %w", err)
				}
			}
			deny := lists.NewBase(app)
			silence := suppress.NewBase(app)
			monitor := watch.NewBase(app)
			monitor.Cover = silence
			catalog := dictionary.NewBase(app)
			study := models.NewBase(app)
			study.Model = model

			rates := reference.RatesFromEnv()

			eng := engine.New(engine.Providers{
				History:   events,
				Lists:     deny,
				Screen:    screening,
				Reference: reference.JurisdictionsFromEnv(),
				Rate:      rates,
				Zone:      zone,
			})
			eng.SetScorer(model)

			// Installing the library is what checks that every rule's evidence
			// exists. A failure is a configuration error and the process refuses to
			// start: a monitoring system that comes up with part of its catalog
			// silently missing is worse than one that does not come up, because only
			// the second is noticed.
			if err := eng.SetRules(rules.Library(org)); err != nil {
				return fmt.Errorf("refusing to start, the detection library cannot be installed: %w", err)
			}

			handler := &api.Handler{
				// The token is verified here, in this process, against the JWKS of the
				// brand whose Host the request arrived on.
				//
				// Not the gateway's header. The header is sound only where an
				// authenticating proxy is the sole route in, and in a cluster this
				// service is also reachable at its Service name and its pod IP by
				// anything that can open a socket — at which point the header is an
				// unauthenticated assertion and any caller names any tenant. The record
				// plane of a compliance control is the wrong place to hold that
				// assumption: it is one hop away from another institution's records.
				// TrustedProxyHeader remains for a deployment that can prove the
				// assumption, and it qualifies its tenant the same way.
				Identity:  api.IAMIdentity(api.JWKS(keysTTL, keysStale), client),
				Engine:    eng,
				ClientID:  client,
				Cases:     cases.NewBase(app),
				Alerts:    api.NewAlertStoreBase(app),
				Screen:    screening,
				Readiness: readiness,
				History:   events,
				Rate:      rates,
				Records:   records,
				Keys:      keys,
				Velocity:  windows,
				Anomaly:   model,
				Planes: api.Planes{
					Lists: deny, Suppress: silence, Watch: monitor,
					Dictionary: catalog, Models: study,
				},
			}
			// The model plane reads a tenant's history through the same join a
			// rule replay uses, so a study of the model and a study of a rule
			// see the same events.
			study.History = api.Replayed{H: handler}
			handler.Register(se)

			// The field catalog accumulates in memory and is written on a
			// cadence and at shutdown. Both are wired here rather than left to
			// a caller: an accumulator nobody flushes is a statistic that only
			// ever reads as pending.
			app.Cron().Add("dictionary-flush", "*/5 * * * *", func() {
				if err := catalog.Flush(context.Background()); err != nil {
					app.Logger().Error("field catalog flush failed", "error", err)
				}
			})
			app.OnTerminate().BindFunc(func(te *core.TerminateEvent) error {
				if err := catalog.Flush(context.Background()); err != nil {
					app.Logger().Error("field catalog flush failed at shutdown", "error", err)
				}
				return te.Next()
			})

			refresh(app, screening, readiness)

			// Destroy records whose retention period has run out, daily.
			retention.Cron(app, records)

			return se.Next()
		},
	})

	app.RootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run:   func(*cobra.Command, []string) { fmt.Println("amld", version) },
	})

	if err := app.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// refresh registers the daily sanctions load and runs one immediately, so the
// process does not serve a whole day with nothing loaded.
func refresh(app core.App, screening *screen.Store, readiness *screen.Readiness) {
	load := func() {
		results := screen.Fetch(context.Background())
		for i := range results {
			r := &results[i]
			if r.Err != nil {
				app.Logger().Error("sanctions list load failed", "source", r.Source, "error", r.Err)
				continue
			}
			if err := screening.Load(r.Source, r.Entries); err != nil {
				// Recorded on the result too, so readiness reports a rejected list as
				// unfit rather than as a successful load of nothing.
				r.Err = err
				app.Logger().Error("sanctions list rejected", "source", r.Source, "error", err)
				continue
			}
			app.Logger().Info("sanctions list loaded", "source", r.Source, "designations", len(r.Entries))
		}
		readiness.Record(results, time.Now().UTC())
		if err := screening.Ready(); err != nil {
			app.Logger().Error("screening is not ready after a refresh", "error", err)
		}
	}
	app.Cron().Add("sanctions-refresh", "0 6 * * *", load)
	go load()
}
