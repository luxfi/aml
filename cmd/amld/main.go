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
	"os"

	"github.com/hanzoai/base"
	"github.com/hanzoai/base/apis"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tools/hook"
	"github.com/spf13/cobra"

	"github.com/luxfi/aml/pkg/api"
	"github.com/luxfi/aml/pkg/cases"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/rules"
	"github.com/luxfi/aml/pkg/sanctions"
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

	handler := &api.Handler{
		Engine:    eng,
		Cases:     caseStore,
		Alerts:    alertStore,
		Sanctions: sanctionsStore,
	}

	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(se *core.ServeEvent) error {
			// Wire TransactionStore (Base-backed) into engine helpers.
			txStore := engine.NewBaseStore(app)
			engine.RegisterStoreHelpers(eng, txStore)

			// Wire SanctionsStore (Base-backed) and register refresh cron.
			baseSanctionsStore := sanctions.NewBaseSanctionsStore(app)
			sanctions.RefreshCron(app, baseSanctionsStore)

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
