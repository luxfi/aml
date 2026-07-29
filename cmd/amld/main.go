// amld is the AML engine daemon. It provides real-time transaction monitoring,
// sanctions screening, case management, and webhook delivery.
//
// Usage:
//
//	amld serve        Start the HTTP server
//	amld version      Print version
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/hanzoai/base"
	"github.com/hanzoai/base/apis"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tools/hook"
	"github.com/spf13/cobra"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/api"
	"github.com/luxfi/aml/pkg/cases"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/retention"
	"github.com/luxfi/aml/pkg/rules"
	"github.com/luxfi/aml/pkg/sanctions"
	"github.com/luxfi/aml/pkg/token"
	"github.com/luxfi/aml/pkg/velocity"
	uiaml "github.com/luxfi/aml/ui"
)

var version = "(dev)"

func main() {
	app := base.New()

	var serveCmd *cobra.Command
	for _, c := range app.RootCmd.Commands() {
		if c.Use == "serve" {
			serveCmd = c
			break
		}
	}

	// Wire AML engine into Base's serve lifecycle.
	defaultOrg := os.Getenv("AML_DEFAULT_ORG")
	if defaultOrg == "" {
		defaultOrg = "default"
	}

	starterRules := rules.StarterRules(defaultOrg)
	eng := engine.New(starterRules)
	caseStore := cases.NewStore()
	alertStore := api.NewAlertStore()
	sanctionsStore := api.NewSanctionsStore()
	// Per-source screening readiness. Built from the configured lists so a source
	// that has never once loaded is reported as unfit rather than being absent.
	screening := sanctions.NewMonitor(sanctions.DefaultLists())

	// The record plane. AML_TOKEN_KEY carries the KMS-held root that per-org
	// tokenisation keys are derived from: 32 bytes or more, hex encoded. There is
	// no default, so an instance without it reports itself unfit and refuses to
	// process transactions it could not record — which is the right failure for a
	// control whose whole job is to have kept the record.
	records := retention.New()
	keys := token.NewKeyring(token.Env("AML_TOKEN_KEY"))

	// The behavioural plane. Sliding aggregates are the substrate every
	// behavioural measure reads; the model reads them to score whether a
	// transaction is unusual for the entity, as a complement to the rules.
	//
	// It starts in shadow. Detection has to be testable before it is activated, so
	// a new deployment scores, learns, and records what it would have alerted on
	// at GET /v1/aml/anomaly, and contributes nothing to any transaction's outcome
	// until someone has read that and set AML_ANOMALY=live. Nothing about the
	// rules changes either way.
	windows := velocity.New(velocity.Config{})
	model, err := anomaly.New(anomaly.Config{Shadow: os.Getenv("AML_ANOMALY") != "live"}, windows)
	if err != nil {
		log.Fatalf("[aml] behavioural plane: %v", err)
	}
	eng.SetScorer(model)

	handler := &api.Handler{
		// The gateway authenticates the caller and sets X-Org-Id from the verified
		// JWT owner claim, and this service is reachable only through it. That is an
		// assumption about the deployment, so it is stated here rather than buried
		// in a handler.
		Identity:  api.TrustedProxyHeader("X-Org-Id"),
		Engine:    eng,
		Cases:     caseStore,
		Alerts:    alertStore,
		Sanctions: sanctionsStore,
		Screening: screening,
		Records:   records,
		Keys:      keys,
		Velocity:  windows,
		Anomaly:   model,
	}

	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(se *core.ServeEvent) error {
			// Wire TransactionStore (Base-backed) into engine helpers.
			txStore := engine.NewBaseStore(app)
			engine.RegisterStoreHelpers(eng, txStore)

			// Wire SanctionsStore (Base-backed) and register refresh cron.
			baseSanctionsStore := sanctions.NewBaseSanctionsStore(app)
			sanctions.RefreshCron(app, baseSanctionsStore, screening)

			// Destroy records whose retention period has run out, daily.
			retention.Cron(app, records)

			handler.Register(se)

			// Mount embedded admin UI at /_/aml/
			se.Router.GET("/_/aml/{path...}", apis.Static(uiaml.DistDirFS(), true))

			return se.Next()
		},
	})

	// Add version subcommand.
	app.RootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("amld", version)
		},
	})

	_ = serveCmd // Base already provides "serve" — we just hook into it.

	if err := app.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
