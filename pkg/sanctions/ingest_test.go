package sanctions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIngestXMLEntityExpansion (RED-05) verifies that the OFAC XML parser
// rejects entity expansion attacks (billion laughs / XML bombs). Without
// the streaming decoder fix, this would cause exponential memory growth.
func TestIngestXMLEntityExpansion(t *testing.T) {
	// Classic billion-laughs XML bomb with nested entity expansion.
	bomb := `<?xml version="1.0"?>
<!DOCTYPE lolz [
  <!ENTITY lol "lol">
  <!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">
  <!ENTITY lol3 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">
  <!ENTITY lol4 "&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;">
]>
<sdnList>
  <sdnEntry>
    <uid>1</uid>
    <firstName>&lol4;</firstName>
    <lastName>Test</lastName>
  </sdnEntry>
</sdnList>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(bomb))
	}))
	defer srv.Close()

	ing := NewIngester()
	_, _, err := ing.FetchOFAC(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error parsing XML bomb with entity expansion, got nil")
	}
	// The error should be about entity references, not OOM.
	t.Logf("correctly rejected XML bomb: %v", err)
}

// TestIngestValidOFACXML ensures normal OFAC XML still parses correctly
// after the streaming decoder change.
func TestIngestValidOFACXML(t *testing.T) {
	validXML := `<?xml version="1.0"?>
<sdnList>
  <sdnEntry>
    <uid>1234</uid>
    <firstName>John</firstName>
    <lastName>Doe</lastName>
    <sdnType>Individual</sdnType>
    <akaList>
      <aka>
        <firstName>Johnny</firstName>
        <lastName>Doe</lastName>
      </aka>
    </akaList>
  </sdnEntry>
  <sdnEntry>
    <uid>5678</uid>
    <firstName></firstName>
    <lastName>Evil Corp</lastName>
    <sdnType>Entity</sdnType>
  </sdnEntry>
</sdnList>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(validXML))
	}))
	defer srv.Close()

	ing := NewIngester()
	entries, hash, err := ing.FetchOFAC(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Name != "John Doe" {
		t.Errorf("entry[0].Name = %q, want %q", entries[0].Name, "John Doe")
	}
	if entries[0].Type != "individual" {
		t.Errorf("entry[0].Type = %q, want %q", entries[0].Type, "individual")
	}
	if len(entries[0].Aliases) != 1 || entries[0].Aliases[0] != "Johnny Doe" {
		t.Errorf("entry[0].Aliases = %v, want [Johnny Doe]", entries[0].Aliases)
	}
	if entries[1].Type != "entity" {
		t.Errorf("entry[1].Type = %q, want %q", entries[1].Type, "entity")
	}
}
