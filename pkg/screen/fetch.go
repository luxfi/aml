package screen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/luxfi/aml/pkg/sanctions"
)

// source is one publisher: where to fetch it and how to read it.
//
// There is one entry per publisher and no default. The previous refresh fell back
// to the OFAC parser for the other three lists on the stated grounds that the
// formats were similar; they are not — the roots are sdnList, CONSOLIDATED_LIST,
// export, and a CSV — so three of the four failed every night, logged a line, and
// left the store holding only OFAC.
type source struct {
	name  string
	url   string
	parse func([]byte) ([]sanctions.Entry, error)
}

// sources are the four published list families.
//
// The EU access token in the URL below is the fixed value the Commission publishes
// for anonymous access to the consolidated file. It is not a credential and there
// is nothing to rotate; it is here because the endpoint requires it.
var sources = []source{
	{sanctions.OFAC, "https://www.treasury.gov/ofac/downloads/sdn.xml", sanctions.ParseOFAC},
	{sanctions.UN, "https://scsanctions.un.org/resources/xml/en/consolidated.xml", sanctions.ParseUN},
	{sanctions.EU, "https://webgate.ec.europa.eu/fsd/fsf/public/files/xmlFullSanctionsList_1_1/content?token=dG9rZW4tMjAxNw", sanctions.ParseEU},
	{sanctions.OFSI, "https://ofsistorage.blob.core.windows.net/publishlive/2022format/ConList.csv", sanctions.ParseOFSI},
}

// maxList bounds a downloaded list. The largest of the four is about 30 MB.
const maxList = 128 << 20

// Result is the outcome of loading one publisher's list.
type Result struct {
	Source  string
	Entries []sanctions.Entry
	Digest  string
	Err     error
}

// Fetch downloads and parses every list, returning one result per publisher.
//
// A failure for one publisher does not abandon the others, and every failure is
// reported rather than returned as an empty list. The caller decides what to do
// with a partial refresh; what it must not be able to do is mistake a failure for
// a publisher that designates nobody.
func Fetch(ctx context.Context) []Result {
	client := &http.Client{Timeout: 3 * time.Minute}
	out := make([]Result, 0, len(sources))
	for _, s := range sources {
		body, digest, err := get(ctx, client, s.url)
		if err != nil {
			out = append(out, Result{Source: s.name, Err: err})
			continue
		}
		entries, err := s.parse(body)
		if err != nil {
			out = append(out, Result{Source: s.name, Digest: digest, Err: err})
			continue
		}
		out = append(out, Result{Source: s.name, Entries: entries, Digest: digest})
	}
	return out
}

func get(ctx context.Context, client *http.Client, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "luxfi-aml/1")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxList))
	if err != nil {
		return nil, "", err
	}
	// A response that reached the read limit was truncated, and a truncated
	// sanctions list parses into a shorter list of designations rather than into an
	// error.
	if len(body) == maxList {
		return nil, "", fmt.Errorf("response from %s reached the %d byte limit and may be truncated", url, maxList)
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}
