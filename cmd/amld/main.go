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
	"fmt"
	"log"
	"os"
	"time"

	"github.com/hanzoai/base"
	"github.com/hanzoai/base/apis"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tools/hook"
	"github.com/spf13/cobra"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/api"
	"github.com/luxfi/aml/pkg/cases"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/history"
	"github.com/luxfi/aml/pkg/reference"
	"github.com/luxfi/aml/pkg/retention"
	"github.com/luxfi/aml/pkg/rules"
	"github.com/luxfi/aml/pkg/screen"
	"github.com/luxfi/aml/pkg/token"
	"github.com/luxfi/aml/pkg/velocity"
	uiaml "github.com/luxfi/aml/ui"
)

var version = "(dev)"

// listStale is how old the newest sanctions load may be before screening reports
// itself unready. The lists refresh daily, so two days means two consecutive
// failures have gone unnoticed.
const listStale = 48 * time.Hour

func main() {
	app := base.New()

	org := os.Getenv("AML_DEFAULT_ORG")
	if org == "" {
		org = "default"
	}

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
	lists := screen.New(listStale, nil)

	// Per-source readiness. The store answers whether screening can be relied on at
	// all; this answers which publisher is behind it, with a count and a date,
	// because "not ready" without a source is not actionable.
	readiness := screen.NewReadiness(screen.SourceNames())

	// The record plane. AML_TOKEN_KEY carries the KMS-held root that per-org
	// tokenisation keys derive from: 32 bytes or more, hex. There is no default, so
	// an instance without it reports itself unfit and refuses to process a
	// transaction it could not record — the right failure for a control whose whole
	// job is to have kept the record.
	records := retention.New()
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
			// The transaction collection has to exist before any rule reads a
			// window. Without it every aggregate rule reaches no verdict, which is
			// how twelve of twenty rules came to fault on every transaction.
			if err := history.Ensure(app); err != nil {
				return fmt.Errorf("refusing to start, the transaction record cannot be created: %w", err)
			}
			events := history.NewBase(app)
			rates := reference.RatesFromEnv()

			eng := engine.New(engine.Providers{
				History:   events,
				Screen:    lists,
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
				// The gateway authenticates the caller and writes the verified owner
				// claim to this header, and this service is reachable only through it.
				// That is an assumption about the deployment, so it is named here
				// rather than left implicit in a handler.
				Identity:  api.TrustedProxyHeader("X-Org-Id"),
				Engine:    eng,
				Cases:     cases.NewStore(),
				Alerts:    api.NewAlertStore(),
				Screen:    lists,
				Readiness: readiness,
				History:   events,
				Rate:      rates,
				Records:   records,
				Keys:      keys,
				Velocity:  windows,
				Anomaly:   model,
			}
			handler.Register(se)

			refresh(app, lists, readiness)

			// Destroy records whose retention period has run out, daily.
			retention.Cron(app, records)

			se.Router.GET("/_/aml/{path...}", apis.Static(uiaml.DistDirFS(), true))
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
func refresh(app core.App, lists *screen.Store, readiness *screen.Readiness) {
	load := func() {
		results := screen.Fetch(context.Background())
		for i := range results {
			r := &results[i]
			if r.Err != nil {
				app.Logger().Error("sanctions list load failed", "source", r.Source, "error", r.Err)
				continue
			}
			if err := lists.Load(r.Source, r.Entries); err != nil {
				// Recorded on the result too, so readiness reports a rejected list as
				// unfit rather than as a successful load of nothing.
				r.Err = err
				app.Logger().Error("sanctions list rejected", "source", r.Source, "error", err)
				continue
			}
			app.Logger().Info("sanctions list loaded", "source", r.Source, "designations", len(r.Entries))
		}
		readiness.Record(results, time.Now().UTC())
		if err := lists.Ready(); err != nil {
			app.Logger().Error("screening is not ready after a refresh", "error", err)
		}
	}
	app.Cron().Add("sanctions-refresh", "0 6 * * *", load)
	go load()
}
