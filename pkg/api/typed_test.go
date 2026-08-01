package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/lists"
	"github.com/luxfi/aml/pkg/models"
	"github.com/luxfi/aml/pkg/suppress"
	"github.com/luxfi/aml/pkg/topology"
	"github.com/luxfi/aml/pkg/watch"
)

// TestQueryBindingRefusesRatherThanZeroing.
//
// `?limit=all` silently becoming 0 becoming the default is how a caller asks for
// everything and is handed a page; `?live=yes` silently becoming false is how a
// filter turns itself off. Both read as a successful request for something else.
func TestQueryBindingRefusesRatherThanZeroing(t *testing.T) {
	var in watch.FeedIn
	q := url.Values{"since": {"2026-03-01T12:00:00Z"}, "rule": {"r1"}, "live": {"true"}, "limit": {"25"}}
	if err := bind(q, &in); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if in.Rule != "r1" || !in.Live || in.Limit != 25 {
		t.Fatalf("bound %+v", in)
	}
	if !in.Since.Equal(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("since = %v", in.Since)
	}

	for _, bad := range []url.Values{
		{"limit": {"all"}},
		{"live": {"yes"}},
		{"since": {"yesterday"}},
	} {
		var out watch.FeedIn
		if err := bind(bad, &out); err == nil {
			t.Errorf("%v must be refused rather than read as a zero", bad)
		}
	}
}

// TestPathWinsOverTheBody. A body naming a different list than the URL is a
// request whose meaning depends on which half the reader believes, and the URL is
// the one the router matched on.
func TestPathWinsOverTheBody(t *testing.T) {
	in := lists.AddIn{Name: "from-the-body"}
	req := httptest.NewRequest(http.MethodPost, "/v1/aml/lists/from-the-path/entries", nil)
	req.SetPathValue("name", "from-the-path")
	if err := path(req, &in); err != nil {
		t.Fatal(err)
	}
	if in.Name != "from-the-path" {
		t.Fatalf("name = %q, want the path's", in.Name)
	}

	// A route with no such parameter leaves the field alone.
	in = lists.AddIn{Name: "from-the-body"}
	bare := httptest.NewRequest(http.MethodPost, "/v1/aml/lists", nil)
	if err := path(bare, &in); err != nil {
		t.Fatal(err)
	}
	if in.Name != "from-the-body" {
		t.Fatalf("name = %q, want the body's", in.Name)
	}
}

// TestEveryPlaneSentinelIsClassified.
//
// A refusal the table does not name answers 500, which is the right failure —
// loud — but it must not happen for an error a plane publishes. This walks every
// exported sentinel of every plane and checks the transport has an opinion about
// it, so adding one and forgetting the table is a red test rather than a 500 in
// production.
func TestEveryPlaneSentinelIsClassified(t *testing.T) {
	classified := map[error]bool{}
	for _, set := range [][]error{gone, taken, refused, broken} {
		for _, err := range set {
			if classified[err] {
				t.Errorf("%v appears twice in the refusal table", err)
			}
			classified[err] = true
		}
	}

	for name, sentinels := range map[string][]error{
		"lists":    {lists.ErrNoList, lists.ErrExists, lists.ErrName, lists.ErrKind, lists.ErrClass, lists.ErrValue, lists.ErrEmpty, lists.ErrDecider, lists.ErrReason, lists.ErrCrowded, lists.ErrNoEntry, lists.ErrStore, lists.ErrRetired, lists.ErrMaxValues},
		"suppress": {suppress.ErrReason, suppress.ErrDecider, suppress.ErrBroad, suppress.ErrKind, suppress.ErrSubject, suppress.ErrWindow, suppress.ErrNotHere, suppress.ErrLifted, suppress.ErrStore},
		"watch":    {watch.ErrRule, watch.ErrSubject, watch.ErrKind, watch.ErrAction, watch.ErrTo, watch.ErrCount, watch.ErrWithin, watch.ErrReason, watch.ErrDecider, watch.ErrNotHere, watch.ErrRetired, watch.ErrStore},
		"models":   {models.ErrNoHistory, models.ErrNoModel, models.ErrNotHere, models.ErrNoFit, models.ErrDecider, models.ErrShape, models.ErrAdopted, models.ErrStore},
		"topology": {topology.ErrEmptySpace, topology.ErrHuge, topology.ErrNoHistory, topology.ErrEmpty, topology.ErrShape, topology.ErrOrg},
	} {
		for _, err := range sentinels {
			if !classified[err] {
				t.Errorf("%s publishes %v and the transport has no status for it", name, err)
			}
		}
	}
}

// TestEveryPlaneRouteIsATypedOperation.
//
// The routing table is read as source. A handler with a body in planes.go is the
// second copy of a contract that the cloud mount would then have to re-implement
// differently, and the difference is what a customer finds.
func TestEveryPlaneRouteIsATypedOperation(t *testing.T) {
	src := readSource(t, "planes.go")
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "se.Router.") {
			continue
		}
		if !strings.Contains(line, "get(h,") && !strings.Contains(line, "post(h,") {
			t.Errorf("route is not a typed operation: %s", line)
		}
	}
	if strings.Count(src, "se.Router.") < 18 {
		t.Fatalf("the routing table looks truncated: %d routes", strings.Count(src, "se.Router."))
	}
}

// TestPathParametersMatchTheirFields.
//
// A route naming {name} binds onto the In struct's json tag `name`. A mismatch
// does not error: the field simply stays empty and the operation refuses for a
// reason that names the wrong thing. Checked here against the actual structs.
func TestPathParametersMatchTheirFields(t *testing.T) {
	for param, in := range map[string]any{
		"name": lists.AddIn{},
		"id":   suppress.LiftIn{},
	} {
		if !hasTag(reflect.TypeOf(in), param) {
			t.Errorf("%T has no field tagged %q, so the route parameter binds to nothing", in, param)
		}
	}
	for _, in := range []any{lists.EntriesIn{}, lists.RemoveIn{}, lists.LookupIn{}} {
		if !hasTag(reflect.TypeOf(in), "name") {
			t.Errorf("%T has no field tagged \"name\"", in)
		}
	}
	for _, in := range []any{watch.RetireIn{}, models.RefIn{}, models.AdoptIn{}} {
		if !hasTag(reflect.TypeOf(in), "id") {
			t.Errorf("%T has no field tagged \"id\"", in)
		}
	}
}

func hasTag(t reflect.Type, want string) bool {
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == want {
			return true
		}
	}
	return false
}

// TestTenantKeyIsTheBrandRegistrys. The api package must not hold a second
// definition of the tenant key's shape.
func TestTenantKeyIsTheBrandRegistrys(t *testing.T) {
	key, err := qualify("hanzo", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if key != "hanzo/acme" || !qualified(key) {
		t.Fatalf("tenant key = %q", key)
	}
	if qualified("acme") {
		t.Fatal("a bare org is not a tenant key")
	}
	if _, err := qualify("hanzo", ""); err == nil {
		t.Fatal("an empty org acts for no tenant")
	}
}

// readSource reads one of this package's own files, which is how the routing
// table is held to being a routing table.
func readSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}
