package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/types"
)

// event builds a request event a handler can be called with directly.
func event(method, target string, headers map[string]string) (*core.RequestEvent, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e := &core.RequestEvent{}
	e.Request = req
	e.Response = rec
	return e, rec
}

// handlers is every route that reads tenant-scoped data, so a new route cannot be
// added without deciding what it does about the tenant.
func (h *Handler) readers() map[string]func(*core.RequestEvent) error {
	return map[string]func(*core.RequestEvent) error{
		"getAlerts":           h.getAlerts(),
		"listCases":           h.listCases(),
		"testRule":            h.testRule(),
		"searchRelationships": h.searchRelationships(),
		"addCaseEvent":        h.addCaseEvent(),
		"resolveCase":         h.resolveCase(),
		"openRelationship":    h.openRelationship(),
		"closeRelationship":   h.closeRelationship(),
		"ingestTransaction":   h.ingestTransaction(),
	}
}

// With no Identity wired, every tenant-scoped route must refuse. The failure this
// prevents is a deployment that serves data because nobody configured who is
// allowed to ask for it.
func TestWithoutIdentityEveryRouteRefuses(t *testing.T) {
	h := &Handler{Alerts: NewAlertStore()}
	for name, handle := range h.readers() {
		e, rec := event(http.MethodGet, "/v1/aml/x", map[string]string{"X-Org-Id": "victim"})
		if err := handle(e); err != nil {
			t.Fatalf("%s returned a transport error: %v", name, err)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s served with no Identity configured: status %d, want 401", name, rec.Code)
		}
	}
}

// A caller-supplied tenant header must not be honoured on its own. This is the
// exact request that read another tenant's alerts: no credential, a header naming
// somebody else's org.
func TestForgedTenantHeaderIsRefused(t *testing.T) {
	alerts := NewAlertStore()
	alerts.Add("tx-1", []types.Alert{{ID: "a1", OrgID: "hanzo/victim"}})

	// An Identity that authenticates nobody — the shape of an unauthenticated
	// request reaching a service that requires one.
	h := &Handler{
		Alerts: alerts,
		Identity: func(*http.Request) (string, error) {
			return "", errors.New("no credential presented")
		},
	}

	e, rec := event(http.MethodGet, "/v1/aml/transactions/tx-1/alerts", map[string]string{"X-Org-Id": "victim"})
	if err := h.getAlerts()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	if body := rec.Body.String(); body != "" && rec.Code == http.StatusOK {
		t.Fatalf("unauthenticated request received a body: %s", body)
	}
}

// An authenticated tenant must see only its own alerts, even knowing the exact
// transaction id of another tenant's. A transaction id is not a secret.
func TestAuthenticatedTenantCannotReadAnotherTenant(t *testing.T) {
	alerts := NewAlertStore()
	alerts.Add("tx-1", []types.Alert{{ID: "victim-alert", OrgID: "hanzo/victim"}})
	alerts.Add("tx-1", []types.Alert{{ID: "attacker-alert", OrgID: "hanzo/attacker"}})

	// Authenticated as "attacker", asking for a transaction it shares an id with.
	h := &Handler{
		Alerts:   alerts,
		Identity: func(*http.Request) (string, error) { return "hanzo/attacker", nil },
	}

	e, rec := event(http.MethodGet, "/v1/aml/transactions/tx-1/alerts",
		map[string]string{"X-Org-Id": "victim"}) // header says victim; identity says attacker
	e.Request.SetPathValue("id", "tx-1")
	if err := h.getAlerts()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if contains(body, "victim-alert") {
		t.Fatalf("cross-tenant read: attacker received victim's alert. body=%s", body)
	}
	if !contains(body, "attacker-alert") {
		t.Fatalf("tenant did not receive its own alert. body=%s", body)
	}
}

// The identity, not the header, decides the tenant. If the header could override
// it, authenticating would not bound anything.
func TestHeaderDoesNotOverrideIdentity(t *testing.T) {
	h := &Handler{Identity: func(*http.Request) (string, error) { return "hanzo/real", nil }}
	e, _ := event(http.MethodGet, "/v1/aml/x", map[string]string{"X-Org-Id": "spoofed"})

	got, err := h.tenant(e)
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	if got != "hanzo/real" {
		t.Fatalf("tenant = %q, want %q — the header must not decide the tenant", got, "hanzo/real")
	}
}

// An Identity that resolves to anything but a brand-qualified tenant is a failure,
// not a tenant whose name happens to be odd. An empty org would scope every read to
// nothing, or to everything, depending on the store; a bare org would put two
// brands' institutions of the same name in one tenant and one vault.
//
// This is the boundary the value crosses into the store index, the history column
// and the vault salt, and it is checked here rather than trusted from the Identity,
// because the Identity is supplied by the deployment.
func TestUnqualifiedTenantIsRefused(t *testing.T) {
	for _, resolved := range []string{
		"",              // nothing at all
		"   ",           // whitespace
		"acme",          // a bare org: the RED-1 shape
		"/acme",         // no brand
		"hanzo/",        // no org
		"nobody/acme",   // a brand no registry row claims
		"hanzo/a/b",     // an org carrying the separator
		"hanzo / acme",  // padding around the separator
		" hanzo/acme",   // padding at the front
		"HANZO/acme",    // a brand id that is not canonical
		"hanzo/acme/",   // a trailing separator
		"hanzo\\acme",   // the wrong separator
		"hanzo:acme",    // another wrong separator
		"hanzo/ acme  ", // padding around the org
	} {
		h := &Handler{Identity: func(*http.Request) (string, error) { return resolved, nil }}
		e, _ := event(http.MethodGet, "/v1/aml/x", nil)
		if got, err := h.tenant(e); err == nil {
			t.Errorf("an identity resolving %q was accepted as tenant %q", resolved, got)
		}
	}
	// And the canonical form is accepted, so the rows above fail for their own
	// reason and not because everything fails.
	h := &Handler{Identity: func(*http.Request) (string, error) { return "hanzo/acme", nil }}
	e, _ := event(http.MethodGet, "/v1/aml/x", nil)
	if got, err := h.tenant(e); err != nil || got != "hanzo/acme" {
		t.Fatalf("tenant = %q, %v; want hanzo/acme", got, err)
	}
}

// The proxy header names the ORG. The brand comes from the request's own Host, so
// the two Identity implementations produce the same key and a deployment cannot
// take the brand from a header a client can write.
func TestTrustedProxyHeaderQualifiesWithTheHost(t *testing.T) {
	id := TrustedProxyHeader("X-Org-Id")

	proxied := func(host, org string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		if org != "" {
			r.Header.Set("X-Org-Id", org)
		}
		return r
	}

	for _, tc := range []struct{ host, org, want string }{
		{"api.hanzo.ai", " acme ", "hanzo/acme"},
		{"api.lux.network", "acme", "lux/acme"},
		{"console.zoo.cloud", "acme", "zoo/acme"},
	} {
		got, err := id(proxied(tc.host, tc.org))
		if err != nil {
			t.Fatalf("%s: %v", tc.host, err)
		}
		if got != tc.want {
			t.Errorf("%s + %q = %q, want %q", tc.host, tc.org, got, tc.want)
		}
	}

	// No header is no tenant; and no brand on the Host is no tenant space to put an
	// org in, however trusted the header is. An in-cluster caller that reaches this
	// service directly arrives on exactly those hosts.
	for _, tc := range []struct{ name, host, org string }{
		{"no header", "api.hanzo.ai", ""},
		{"blank header", "api.hanzo.ai", "   "},
		{"an org carrying the separator", "api.hanzo.ai", "lux/acme"},
		{"a pod IP", "10.42.0.7:8090", "acme"},
		{"a service name", "aml.aml.svc.cluster.local", "acme"},
		{"localhost", "localhost:8090", "acme"},
		{"no host at all", "", "acme"},
		{"a lookalike domain", "a.zoo.ngo.attacker.example", "acme"},
	} {
		if got, err := id(proxied(tc.host, tc.org)); err == nil {
			t.Errorf("%s resolved to tenant %q", tc.name, got)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
