package sanctions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luxfi/aml/pkg/types"
)

// serve returns a list pointed at a test server yielding the given body.
func serve(t *testing.T, source, body string) types.SanctionsList {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return types.SanctionsList{Source: source, URL: srv.URL, Format: "xml", Active: true}
}

// Entity expansion must fail the parse rather than expand. A billion-laughs
// payload is the reachable denial of service on any XML ingest.
func TestEntityExpansionIsRejected(t *testing.T) {
	bomb := `<?xml version="1.0"?>
<!DOCTYPE lolz [
  <!ENTITY lol "lol">
  <!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">
  <!ENTITY lol3 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">
  <!ENTITY lol4 "&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;&lol3;">
]>
<sdnList><sdnEntry><uid>1</uid><firstName>&lol4;</firstName><lastName>Test</lastName></sdnEntry></sdnList>`

	if _, _, err := NewIngester().Fetch(context.Background(), serve(t, types.ListOFACSDN, bomb)); err == nil {
		t.Fatal("XML bomb with entity expansion was accepted")
	}
}

func TestFetchParsesAndHashes(t *testing.T) {
	valid := `<?xml version="1.0"?>
<sdnList>
  <sdnEntry><uid>1234</uid><firstName>John</firstName><lastName>Doe</lastName><sdnType>Individual</sdnType>
    <akaList><aka><firstName>Johnny</firstName><lastName>Doe</lastName></aka></akaList></sdnEntry>
  <sdnEntry><uid>5678</uid><lastName>Evil Corp</lastName><sdnType>Entity</sdnType></sdnEntry>
</sdnList>`

	entries, hash, err := NewIngester().Fetch(context.Background(), serve(t, types.ListOFACSDN, valid))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(hash) != 64 {
		t.Fatalf("hash = %q, want a 64-char sha256 hex digest", hash)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Name != "John Doe" || entries[0].Type != types.SanctionIndividual {
		t.Errorf("entry[0] = %q/%q", entries[0].Name, entries[0].Type)
	}
	if len(entries[0].Aliases) != 1 || entries[0].Aliases[0] != "Johnny Doe" {
		t.Errorf("entry[0].Aliases = %v, want [Johnny Doe]", entries[0].Aliases)
	}
	if entries[1].Name != "Evil Corp" || entries[1].Type != types.SanctionEntity {
		t.Errorf("entry[1] = %q/%q", entries[1].Name, entries[1].Type)
	}
}

// The defect this guards is the one that hid two dead endpoints: a list that
// yields nothing must be an error, because "no entries" and "nobody is
// designated" are indistinguishable to every caller downstream.
func TestEmptyListIsAnError(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"well-formed but empty", `<?xml version="1.0"?><sdnList></sdnList>`},
		{"publisher error page", `<?xml version="1.0"?><Error><Code>BlobNotFound</Code></Error>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := NewIngester().Fetch(context.Background(), serve(t, types.ListOFACSDN, tc.body))
			if err == nil {
				t.Fatal("a list parsing to zero entries was reported as success")
			}
			if !strings.Contains(err.Error(), "0 entries") && !strings.Contains(err.Error(), "parse") {
				t.Fatalf("error does not say what went wrong: %v", err)
			}
		})
	}
}

// A non-200 must fail rather than parse the error body. OFSI's dead ConList
// answers 404 with an XML body, which would otherwise parse to zero entries.
func TestNonOKStatusIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>BlobNotFound</Code></Error>`))
	}))
	defer srv.Close()

	list := types.SanctionsList{Source: types.ListHMT, URL: srv.URL, Active: true}
	_, _, err := NewIngester().Fetch(context.Background(), list)
	if err == nil {
		t.Fatal("HTTP 404 was reported as success")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error hides the status code: %v", err)
	}
}

// Every source must resolve to a parser written for its own schema. Falling back
// to another publisher's parser is what made three of four lists silently empty.
func TestEveryDefaultListHasItsOwnParser(t *testing.T) {
	seen := map[string]bool{}
	for _, list := range DefaultLists() {
		p, err := ParserFor(list.Source)
		if err != nil {
			t.Errorf("%s: %v", list.Source, err)
			continue
		}
		if p == nil {
			t.Errorf("%s: nil parser", list.Source)
		}
		seen[list.Source] = true
	}
	for _, want := range []string{types.ListOFACSDN, types.ListUN, types.ListEU, types.ListHMT} {
		if !seen[want] {
			t.Errorf("%s is not in DefaultLists", want)
		}
	}
	if _, err := ParserFor("some_new_regime"); err == nil {
		t.Fatal("an unregistered source resolved to a parser instead of erroring")
	}
}

// The URL that broke: OFSI closed the Consolidated List on 28 January 2026 and
// ConList.xml has answered 404 since. A screening product pointed at it ingests
// nothing, and nothing is silent.
func TestDefaultListsDoNotUseTheClosedOFSIList(t *testing.T) {
	for _, list := range DefaultLists() {
		if strings.Contains(list.URL, "ofsistorage") || strings.Contains(list.URL, "ConList") {
			t.Errorf("%s points at the closed OFSI Consolidated List: %s", list.Source, list.URL)
		}
		if list.Source == types.ListHMT && !strings.Contains(list.URL, "sanctionslist.fcdo.gov.uk") {
			t.Errorf("UK list is not the FCDO UK Sanctions List: %s", list.URL)
		}
		// The EU endpoint answers 403 without its token.
		if list.Source == types.ListEU && !strings.Contains(list.URL, "token=") {
			t.Errorf("EU list carries no token and will 403: %s", list.URL)
		}
	}
}
