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
	"os"
	"strings"
	"time"

	"github.com/hanzoai/base"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tools/hook"
	"github.com/spf13/cobra"

	"github.com/luxfi/aml/pkg/api"
	"github.com/luxfi/aml/pkg/reference"
	"github.com/luxfi/aml/pkg/rules"
	"github.com/luxfi/aml/pkg/screen"
	"github.com/luxfi/aml/pkg/token"
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

	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(se *core.ServeEvent) error {
			// Refuse to serve unauthenticated. This is checked here, at the start,
			// rather than one request at a time: an instance that cannot identify a
			// caller has nothing it can safely answer, and an operator finds a process
			// that will not start far sooner than one that 401s everything.
			if client == "" {
				return errors.New("refusing to start, AML_CLIENT_ID is not set: it is this deployment's IAM application clientId, and without it no token's audience can be checked")
			}

			// The whole engine, over this store, assembled in one place.
			//
			// api.Wire is the ONE assembly: every collection, every shelf, every join
			// between them, and everything that has to run on a cadence for the durable
			// state to stay honest. It is what the tests build too, so a plane cannot be
			// proven green against an arrangement this deployment does not have — which
			// is how a record fingerprint came to be a field no column stored.
			//
			// What stays here is what an INSTALLATION answers differently, and it is
			// read from the environment above.
			handler, err := api.Wire(app, api.Deployment{
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
				Identity:      api.IAMIdentity(api.JWKS(keysTTL, keysStale), client),
				ClientID:      client,
				Rules:         rules.Library(org),
				Keys:          keys,
				Screen:        screening,
				Readiness:     readiness,
				Zone:          zone,
				Rate:          reference.RatesFromEnv(),
				Jurisdictions: reference.JurisdictionsFromEnv(),
				// The behavioural model contributes to a verdict only once somebody
				// has read what it would have done. The rules are unaffected either
				// way. See api.Deployment.Live, which is stated from the live side so
				// that the zero value is shadow.
				Live: os.Getenv("AML_ANOMALY") == "live",
			})
			if err != nil {
				return fmt.Errorf("refusing to start: %w", err)
			}
			handler.Register(se)

			refresh(app, screening, readiness)

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
