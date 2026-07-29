// amld is the AML engine daemon: real-time transaction monitoring, sanctions
// screening, and case management.
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
	"os"
	"time"

	"github.com/hanzoai/base"
	"github.com/hanzoai/base/apis"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tools/hook"
	"github.com/spf13/cobra"

	"github.com/luxfi/aml/pkg/api"
	"github.com/luxfi/aml/pkg/cases"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/history"
	"github.com/luxfi/aml/pkg/reference"
	"github.com/luxfi/aml/pkg/rules"
	"github.com/luxfi/aml/pkg/screen"
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
	// endpoints read it; there is no second store for either to talk to instead.
	lists := screen.New(listStale, nil)

	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(se *core.ServeEvent) error {
			events := history.NewBase(app)
			rates := reference.RatesFromEnv()

			eng := engine.New(engine.Providers{
				History:   events,
				Screen:    lists,
				Reference: reference.JurisdictionsFromEnv(),
				Rate:      rates,
				Zone:      zone,
			})

			// Installing the library is what checks that every rule's evidence
			// exists. A failure is a configuration error and the process refuses to
			// start: a monitoring system that comes up with part of its catalog
			// silently missing is worse than one that does not come up, because only
			// the second is noticed.
			if err := eng.SetRules(rules.Library(org)); err != nil {
				return fmt.Errorf("refusing to start, the detection library cannot be installed: %w", err)
			}

			handler := &api.Handler{
				Engine:  eng,
				Cases:   cases.NewStore(),
				Screen:  lists,
				History: events,
				Rate:    rates,
			}
			handler.Register(se)

			refresh(app, lists)
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
func refresh(app core.App, lists *screen.Store) {
	load := func() {
		for _, r := range screen.Fetch(context.Background()) {
			if r.Err != nil {
				app.Logger().Error("sanctions list load failed", "source", r.Source, "error", r.Err)
				continue
			}
			if err := lists.Load(r.Source, r.Entries); err != nil {
				app.Logger().Error("sanctions list rejected", "source", r.Source, "error", err)
				continue
			}
			app.Logger().Info("sanctions list loaded", "source", r.Source, "designations", len(r.Entries))
		}
		if err := lists.Ready(); err != nil {
			app.Logger().Error("screening is not ready after a refresh", "error", err)
		}
	}
	app.Cron().Add("sanctions-refresh", "0 6 * * *", load)
	go load()
}
