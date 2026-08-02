package brand

import (
	"errors"
	"strings"
	"testing"
)

// TestTenantKeyNeverRegresses holds the one property every record plane rests on:
// a tenant is `<brand>/<org>` and a bare org is not a tenant.
//
// It is here rather than in each plane because the shape is defined once. A plane
// that grew its own copy of the check would be free to disagree with this one, and
// the disagreement is a set of rows written under a key that opens another brand's
// vault.
func TestTenantKeyNeverRegresses(t *testing.T) {
	key, err := Qualify("hanzo", "acme")
	if err != nil {
		t.Fatalf("qualify: %v", err)
	}
	if key != "hanzo/acme" {
		t.Fatalf("tenant key = %q, want hanzo/acme", key)
	}
	if !Qualified(key) {
		t.Fatal("a key Qualify produced must be one Qualified accepts, or the two have drifted apart")
	}

	// A bare org is refused everywhere a key is taken.
	for _, bare := range []string{"acme", "", "   ", "/acme", "acme/", "unknownbrand/acme"} {
		if Qualified(bare) {
			t.Errorf("%q is not a tenant key and must not be accepted as one", bare)
		}
		if err := Tenant(bare); !errors.Is(err, ErrTenant) {
			t.Errorf("Tenant(%q) = %v, want ErrTenant", bare, err)
		}
	}
}

// TestBrandQualifiesTheOrg is the sharp end: two brands' institutions of the same
// name must be two tenants.
//
// Keyed on the bare org they are one — one set of rows, and one tokenisation
// vault, because the salt is this key. That is a cross-brand plaintext disclosure
// of retained personal data, and it is what qualifying at the key makes
// uncomputable rather than merely disallowed.
func TestBrandQualifiesTheOrg(t *testing.T) {
	hanzo, err := Qualify("hanzo", "acme")
	if err != nil {
		t.Fatal(err)
	}
	zoo, err := Qualify("zoo", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if hanzo == zoo {
		t.Fatalf("two brands' institutions named acme resolved to one tenant key %q", hanzo)
	}
	if !strings.HasPrefix(hanzo, "hanzo"+Sep) || !strings.HasPrefix(zoo, "zoo"+Sep) {
		t.Fatalf("a tenant key must lead with the brand that vouched for it: %q, %q", hanzo, zoo)
	}
}

// TestUnreadableOrgRefused keeps the key readable back to the institution it
// names. `zoo/lux/acme` does not say which one that is.
func TestUnreadableOrgRefused(t *testing.T) {
	if _, err := Qualify("zoo", "lux/acme"); err == nil {
		t.Fatal("an org containing the separator makes the key ambiguous and must be refused")
	}
	if _, err := Qualify("nobody", "acme"); err == nil {
		t.Fatal("a brand no registry claims vouches for nothing and must be refused")
	}
	if _, err := Qualify("hanzo", "   "); err == nil {
		t.Fatal("an empty org acts for no tenant and must be refused")
	}
}
