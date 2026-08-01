package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/internal/instance"
	"github.com/luxfi/aml/pkg/lists"
	"github.com/luxfi/aml/pkg/models"
	"github.com/luxfi/aml/pkg/suppress"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/watch"
)

// Every plane asks a governed act for a reason and a decider so that a supervisor
// can be told who turned a control off. These are the tests that the answer is
// worth something: the decider is the verified subject of the credential and not
// a string the caller sent, and there is no way for a caller to send one.

// governed is a handler with the suppression and rung planes wired, authenticated
// as one subject.
func governed(t *testing.T, subject string) (*Handler, *suppress.Shelf, *watch.Shelf) {
	t.Helper()
	app := instance.New(t)
	t.Cleanup(app.Cleanup)
	for _, ensure := range []func(core.App) error{suppress.Ensure, watch.Ensure, lists.Ensure} {
		if err := ensure(app); err != nil {
			t.Fatalf("ensure: %v", err)
		}
	}
	silence := suppress.NewBase(app)
	monitor := watch.NewBase(app)
	monitor.Cover = silence

	h := plane(t)
	h.Identity = func(*http.Request) (Caller, error) { return Caller{Tenant: acme, Subject: subject}, nil }
	h.Planes = Planes{Suppress: silence, Watch: monitor, Lists: lists.NewBase(app)}
	return h, silence, monitor
}

// TestTheDeciderIsTheCredentialAndNotTheBody.
//
// The request body names somebody else, in the field the old contract used, and
// the record names the token's subject. This is the whole of the finding: a
// suppression whose "by" is free text records that alerts stopped and attributes
// it to whoever the caller typed, including a real colleague who did not do it.
func TestTheDeciderIsTheCredentialAndNotTheBody(t *testing.T) {
	h, silence, monitor := governed(t, "u-7")
	ctx := context.Background()

	e, rec := send(http.MethodPost, "/v1/aml/suppressions", map[string]any{
		"rule": "ctr", "kind": "account", "value": "acct-1",
		"reason": "treasury sweep, agreed with the MLRO",
		"by":     "the head of compliance", // the lie
	})
	if err := post(h, h.Planes.Suppress.Suppress, true)(e); err != nil {
		t.Fatalf("transport: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("declare = %d: %s", rec.Code, rec.Body.String())
	}

	ledger, err := silence.Ledger(ctx, acme, &suppress.LedgerIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Suppressions) != 1 {
		t.Fatalf("suppressions = %d", len(ledger.Suppressions))
	}
	if by := ledger.Suppressions[0].By; by != "u-7" {
		t.Fatalf("the suppression is attributed to %q, want the token's subject u-7", by)
	}
	if strings.Contains(rec.Body.String(), "the head of compliance") {
		t.Fatalf("the caller's own decider reached the record: %s", rec.Body.String())
	}

	// The same for a rung, which is the other control that changes what reaches a
	// queue, and for lifting, which is the decision to make it noisy again.
	e, rec = send(http.MethodPost, "/v1/aml/rungs", map[string]any{
		"rule": "ctr", "kind": "account", "count": 2, "within": "1h", "to": "block",
		"reason": "two reportable transactions in an hour", "by": "somebody else",
	})
	if err := post(h, h.Planes.Watch.Declare, true)(e); err != nil {
		t.Fatalf("transport: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("declare rung = %d: %s", rec.Code, rec.Body.String())
	}
	ladder, err := monitor.Ladder(ctx, acme, &watch.LadderIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ladder.Rungs) != 1 || ladder.Rungs[0].By != "u-7" {
		t.Fatalf("the rung is attributed to %+v, want u-7", ladder.Rungs)
	}

	lift, rec := send(http.MethodPost, "/v1/aml/suppressions/x/lift", map[string]any{
		"reason": "the sweep ended", "by": "somebody else",
	})
	lift.Request.SetPathValue("id", ledger.Suppressions[0].ID)
	if err := post(h, h.Planes.Suppress.Lift, false)(lift); err != nil {
		t.Fatalf("transport: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("lift = %d: %s", rec.Code, rec.Body.String())
	}
	var lifted suppress.Suppression
	if err := json.Unmarshal(rec.Body.Bytes(), &lifted); err != nil {
		t.Fatal(err)
	}
	if lifted.LiftedBy != "u-7" {
		t.Fatalf("the lift is attributed to %q, want u-7", lifted.LiftedBy)
	}
}

// TestACredentialThatNamesNobodyRecordsNoDecision.
//
// Fail secure, and fail where it costs a request rather than where it costs a
// payment: authentication still succeeds, so reads and ingest work, and every
// operation that would write a decision refuses with the plane's own ErrDecider.
func TestACredentialThatNamesNobodyRecordsNoDecision(t *testing.T) {
	h, silence, _ := governed(t, "")

	e, rec := send(http.MethodPost, "/v1/aml/suppressions", map[string]any{
		"rule": "ctr", "reason": "no", "by": "the head of compliance",
	})
	if err := post(h, h.Planes.Suppress.Suppress, true)(e); err != nil {
		t.Fatalf("transport: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 — a decision naming nobody must not be recorded", rec.Code)
	}
	ledger, err := silence.Ledger(context.Background(), acme, &suppress.LedgerIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Suppressions) != 0 {
		t.Fatalf("a suppression was recorded with no decider: %+v", ledger.Suppressions)
	}
}

// TestNoDeciderIsOnTheWire.
//
// The binding above is only sound if the caller cannot reach the field at all. A
// decider is a distinct type carrying `json:"-"`, so the body decoder, the query
// binder and the path overlay all skip it — and there is no OTHER field named
// `by` on an input, because one would be a second, caller-writable decider that
// a plane might read instead.
func TestNoDeciderIsOnTheWire(t *testing.T) {
	inputs := []any{
		lists.DeclareIn{}, lists.AddIn{}, lists.RemoveIn{}, lists.EntriesIn{}, lists.LookupIn{}, lists.CatalogIn{},
		suppress.SuppressIn{}, suppress.LiftIn{}, suppress.LedgerIn{}, suppress.CoverIn{},
		watch.DeclareIn{}, watch.RetireIn{}, watch.RecordIn{}, watch.FeedIn{}, watch.RatesIn{}, watch.LadderIn{},
		models.SearchIn{}, models.FitIn{}, models.AdoptIn{}, models.RunsIn{}, models.FitsIn{}, models.RefIn{},
		resolution{},
	}
	deciders := 0
	for _, in := range inputs {
		typ := reflect.TypeOf(in)
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.Type == deciderType {
				deciders++
				if name != "-" {
					t.Errorf("%s.%s is a decider tagged %q; it must be json:\"-\" or a caller can write it",
						typ.Name(), f.Name, f.Tag.Get("json"))
				}
				continue
			}
			if name == "by" || strings.EqualFold(f.Name, "by") {
				t.Errorf("%s.%s is a caller-writable decider: it must be a types.Decider with json:\"-\"",
					typ.Name(), f.Name)
			}
		}
	}
	// A count, so that changing every decider to a plain string and deleting the
	// type does not make this test pass by having nothing to check.
	if deciders < 8 {
		t.Fatalf("%d decider fields found across the planes, want every governed input to carry one", deciders)
	}
}

// TestTheDeciderIsWrittenAfterTheBodyAndThePath. Order is the contract: the body
// is decoded, the path overlays it, and the credential overlays both. Anything
// else leaves a request whose meaning depends on which half the reader believes.
func TestTheDeciderIsWrittenAfterTheBodyAndThePath(t *testing.T) {
	in := lists.AddIn{Name: "from-the-body", By: "from-the-body"}
	req := reqWithPath(t, "name", "from-the-path")
	if err := path(req, &in); err != nil {
		t.Fatal(err)
	}
	decide(&in, "u-7")
	if in.Name != "from-the-path" {
		t.Fatalf("name = %q, want the path's", in.Name)
	}
	if in.By != "u-7" {
		t.Fatalf("by = %q, want the credential's", in.By)
	}

	// And an input with no decider is untouched, so the overlay is not a
	// requirement on every operation.
	feed := watch.FeedIn{Rule: "ctr"}
	decide(&feed, "u-7")
	if feed.Rule != "ctr" {
		t.Fatalf("feed = %+v", feed)
	}
}

// TestTheDeciderTypeIsOneType. types.Decider.Trim is the one conversion, so a
// plane cannot half-trim a value into a decider that is a space.
func TestTheDeciderTypeIsOneType(t *testing.T) {
	for raw, want := range map[types.Decider]string{
		"  u-7 ": "u-7",
		"":       "",
		"   ":    "",
		"u-7":    "u-7",
	} {
		if got := raw.Trim(); got != want {
			t.Errorf("%q.Trim() = %q, want %q", string(raw), got, want)
		}
	}
}

func reqWithPath(t *testing.T, key, value string) *http.Request {
	t.Helper()
	e, _ := send(http.MethodPost, "/v1/aml/lists/"+value+"/entries", nil)
	e.Request.SetPathValue(key, value)
	return e.Request
}
