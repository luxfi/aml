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
	return fetch(ctx, func(ctx context.Context, url string) ([]byte, string, error) {
		return get(ctx, client, url)
	}, sleep)
}

// download fetches one list and returns its bytes and digest. It is a parameter of
// fetch rather than a call into the network, so the retry behaviour that decides
// whether a blip costs a day of screening can be tested without one.
type download func(ctx context.Context, url string) (body []byte, digest string, err error)

// attempts is how many times a publisher is asked before its list is recorded as
// failed. The refresh runs daily, so without a retry a single lost packet costs a
// whole day of screening against a list one designation out of date — and the
// measured failures are exactly that kind: the EU file answered a TLS record
// version error once and served 24.8 MB correctly on the next request, from the
// same host, seconds later.
const attempts = 3

// backoff is the wait before retrying a publisher. It grows per attempt, because a
// publisher that just failed under load is not helped by being asked again
// immediately.
const backoff = 5 * time.Second

// fetch downloads and parses every list, retrying a publisher that fails.
//
// A failure for one publisher does not abandon the others, and every failure is
// reported rather than returned as an empty list. Only transport is retried: a
// parse failure is a schema disagreement and will fail identically on the next
// attempt, so retrying it delays the refresh without changing the outcome.
func fetch(ctx context.Context, fetchOne download, wait func(context.Context, time.Duration) error) []Result {
	out := make([]Result, 0, len(sources))

	for _, s := range sources {
		var res Result
		res.Source = s.name

		for attempt := 1; attempt <= attempts; attempt++ {
			body, digest, err := fetchOne(ctx, s.url)
			if err != nil {
				res.Err = fmt.Errorf("attempt %d of %d: %w", attempt, attempts, err)
				// The context ending is the deployment shutting down or giving up, not
				// the publisher failing, so there is nothing to retry into.
				if ctx.Err() != nil {
					break
				}
				if attempt < attempts {
					if werr := wait(ctx, time.Duration(attempt)*backoff); werr != nil {
						break
					}
					continue
				}
				break
			}

			res.Digest = digest
			entries, perr := s.parse(body)
			if perr != nil {
				res.Err = perr
				break
			}
			res.Entries, res.Err = entries, nil
			break
		}
		out = append(out, res)
	}
	return out
}

// sleep waits, or returns early if the context ends first.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

// SourceNames are the publishers this build screens against, in declaration order.
// Readiness is built from these so a source that has never loaded is still reported.
func SourceNames() []string {
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		out = append(out, s.name)
	}
	return out
}
