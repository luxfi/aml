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
	alerts.Add("tx-1", []types.Alert{{ID: "a1", OrgID: "victim"}})

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
	alerts.Add("tx-1", []types.Alert{{ID: "victim-alert", OrgID: "victim"}})
	alerts.Add("tx-1", []types.Alert{{ID: "attacker-alert", OrgID: "attacker"}})

	// Authenticated as "attacker", asking for a transaction it shares an id with.
	h := &Handler{
		Alerts:   alerts,
		Identity: func(*http.Request) (string, error) { return "attacker", nil },
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
	h := &Handler{Identity: func(*http.Request) (string, error) { return "real", nil }}
	e, _ := event(http.MethodGet, "/v1/aml/x", map[string]string{"X-Org-Id": "spoofed"})

	got, err := h.tenant(e)
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	if got != "real" {
		t.Fatalf("tenant = %q, want %q — the header must not decide the tenant", got, "real")
	}
}

// An Identity that resolves to an empty tenant is a failure, not a tenant whose
// name happens to be empty: an empty org would scope every read to nothing, or
// to everything, depending on the store.
func TestEmptyTenantIsRefused(t *testing.T) {
	h := &Handler{Identity: func(*http.Request) (string, error) { return "   ", nil }}
	e, _ := event(http.MethodGet, "/v1/aml/x", nil)
	if _, err := h.tenant(e); err == nil {
		t.Fatal("an empty tenant was accepted")
	}
}

func TestTrustedProxyHeaderRequiresTheHeader(t *testing.T) {
	id := TrustedProxyHeader("X-Org-Id")

	if _, err := id(httptest.NewRequest(http.MethodGet, "/", nil)); err == nil {
		t.Fatal("absent header resolved to a tenant")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Org-Id", " acme ")
	got, err := id(req)
	if err != nil {
		t.Fatalf("present header: %v", err)
	}
	if got != "acme" {
		t.Fatalf("tenant = %q, want %q", got, "acme")
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
