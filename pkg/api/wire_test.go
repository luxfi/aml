// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/internal/instance"
	"github.com/luxfi/aml/internal/source"
	"github.com/luxfi/aml/pkg/reference"
	"github.com/luxfi/aml/pkg/screen"
	"github.com/luxfi/aml/pkg/token"
	"github.com/luxfi/aml/pkg/types"
)

// There is ONE way this engine is assembled, and both the deployment and these
// tests use it.
//
// This is the defect that produced every other one in the last review, and it is
// not a bug in a plane: it is two copies of the wiring. cmd/amld built a Handler
// by hand and the tests built a different Handler by hand, so a plane could be
// added to the ingest path, tested green, and never constructed in production —
// which is exactly what happened to the record fingerprint. The suite proved a
// property of a shelf the deployment does not use.
//
// Deleting one copy is the fix. [Wire] is the assembly; cmd/amld calls it, the
// tests call it, and a plane that is not wired there does not exist for either.

// TestTheDeploymentDoesNotAssembleItsOwnHandler reads cmd/amld and refuses a
// second copy of the wiring.
//
// A behavioural test cannot catch this: a handler built by hand in main is
// perfectly correct until the day it omits a field, and the day it omits one is
// the day no test in this package is looking at it.
func TestTheDeploymentDoesNotAssembleItsOwnHandler(t *testing.T) {
	source.NoLiteral(t, "../../cmd/amld/main.go", "api", "Handler",
		"There is one assembly and it is api.Wire. A second one is how a plane comes to be wired in the tests and absent from the deployment.")
}

// TestWireLeavesNothingUnwired asks the assembly itself.
//
// Every exported field of Handler is something a route reaches for, so an unset
// one is a route that answers 503 or, worse, a plane that silently does nothing.
// Reflection is the point: a hand-written list of fields is a third copy of the
// wiring and would go stale the same way the second one did.
func TestWireLeavesNothingUnwired(t *testing.T) {
	app := instance.New(t)
	t.Cleanup(app.Cleanup)
	h, err := Wire(app, deployment())
	if err != nil {
		t.Fatal(err)
	}

	v := reflect.ValueOf(h).Elem()
	for i := range v.NumField() {
		f := v.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		switch f.Type.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func:
			if v.Field(i).IsNil() {
				t.Errorf("Handler.%s is nil after Wire", f.Name)
			}
		case reflect.String:
			if v.Field(i).String() == "" {
				t.Errorf("Handler.%s is empty after Wire", f.Name)
			}
		case reflect.Struct:
			if v.Field(i).IsZero() {
				t.Errorf("Handler.%s is the zero value after Wire", f.Name)
			}
		}
	}
}

// TestWireCreatesEveryCollectionItWritesTo. A shelf whose column does not exist
// reads back a zero value, and a zero value is a plausible answer — which is how
// a record fingerprint came to be a struct field no column stored.
func TestWireCreatesEveryCollectionItWritesTo(t *testing.T) {
	app := instance.New(t)
	t.Cleanup(app.Cleanup)
	h, err := Wire(app, deployment())
	if err != nil {
		t.Fatal(err)
	}
	// One transaction exercises every plane the ingest path writes to. If a
	// collection is missing, the write fails and the offer is refused.
	if rec := ingest(t, h, payment("tx-1", 25_000)); rec.Code != http.StatusOK {
		t.Fatalf("ingest on a wired deployment = %d: %s", rec.Code, rec.Body.String())
	}
}

// deployment is the test's installation: the same choices cmd/amld reads from
// the environment, with an identity that names one tenant.
func deployment() Deployment {
	return Deployment{
		Identity: func(*http.Request) (Caller, error) { return Caller{Tenant: acme, Subject: "u-analyst"}, nil },
		ClientID: "aml-test",
		Rules: []types.Rule{{
			ID: "ctr", Name: "CTR Threshold", DSL: "Tx.Notional > 10000.0",
			Severity: types.SeverityHigh, Weight: 0.3, Action: types.ActionReport, Enabled: true,
		}},
		Keys:      token.NewKeyring(func(string) ([]byte, error) { return root, nil }),
		Screen:    screen.New(48*time.Hour, nil),
		Readiness: screen.NewReadiness(screen.SourceNames()),
		Zone:      time.UTC,
		Rate:      reference.Rates{},
	}
}

// TestTheDegradationDoorIsServed. Every bound in this process can bind, and when
// one does a control degrades without an error — deliberately, because refusing a
// payment because a cache is full would be the worse failure. What is not
// allowed is for that to be unreadable: a control that switched itself off
// quietly is worse than no control. So there is one door for it, and a route
// registered nowhere is a report nobody can read.
func TestTheDegradationDoorIsServed(t *testing.T) {
	if !strings.Contains(readSource(t, "routes.go"), `se.Router.GET("/v1/aml/load"`) {
		t.Error("nothing serves GET /v1/aml/load, so a tenant cannot ask whether anything of its own is quietly degraded")
	}
}

// TestTheDegradationReportNamesEveryBoundedStore. A report that covers three of
// four bounded stores is worse than none, because the fourth is the one somebody
// will assume is fine. Reflection rather than a list, for the same reason Wire's
// completeness is checked by reflection.
func TestTheDegradationReportNamesEveryBoundedStore(t *testing.T) {
	app := shelves(t)
	h, err := Wire(app, deployment())
	if err != nil {
		t.Fatal(err)
	}
	if rec := ingest(t, h, payment("tx-1", 25_000)); rec.Code != http.StatusOK {
		t.Fatalf("ingest: %d", rec.Code)
	}
	out, err := h.load(context.Background(), acme, &LoadIn{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Org != acme {
		t.Errorf("the report names %q, not the caller", out.Org)
	}
	// Each bounded store answers with its own ceiling, and a ceiling of zero is a
	// store nobody can size a pod against.
	if out.Aggregates.Ceiling == 0 {
		t.Error("the sliding aggregates report no ceiling")
	}
	if out.Engine.Ceiling == 0 || out.Engine.Room == 0 {
		t.Error("the process reports no aggregate ceiling or no room")
	}
	if out.Fields.Ceiling == 0 || out.Fields.Room == 0 {
		t.Error("the field catalog reports no ceiling or no room")
	}
	if !out.Model.Planted {
		t.Error("a tenant that has been scored reports no model")
	}
}

// TestTheBehaviouralModelIsShadowUntilSomebodyArmsIt.
//
// A default that fails towards "the statistical plane is now deciding" is the one
// direction a default must not fail in: detection has to be testable before it is
// activated, and a model that acts before anyone has read what it would have done
// is a model nobody chose. So the field is stated from the LIVE side and the zero
// value is shadow.
func TestTheBehaviouralModelIsShadowUntilSomebodyArmsIt(t *testing.T) {
	app := shelves(t)
	d := deployment()
	d.Live = false
	h, err := Wire(app, d)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Anomaly.Config().Shadow {
		t.Error("a deployment that said nothing about the behavioural model got one that contributes to verdicts")
	}
	if armed, err := Wire(shelves(t), func() Deployment { d := deployment(); d.Live = true; return d }()); err != nil {
		t.Fatal(err)
	} else if armed.Anomaly.Config().Shadow {
		t.Error("a deployment that armed the model still got shadow")
	}
}
