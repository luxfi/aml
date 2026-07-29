package screen

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/sanctions"
)

// stub replaces the publisher table with one served by a local handler, so retry
// behaviour is observed rather than inferred.
func stub(t *testing.T, srcs []source) {
	t.Helper()
	saved := sources
	sources = srcs
	t.Cleanup(func() { sources = saved })
}

// nowait runs the retries without the backoff, so a test does not sleep.
func nowait(context.Context, time.Duration) error { return nil }

// A publisher that fails once must be retried, not written off for the day.
//
// This is the measured failure: the EU list answered a TLS record-version error on
// one request and served 24.8 MB correctly on the next, from the same host, seconds
// later. The refresh runs daily, so without a retry that single lost packet cost a
// full day of screening against a list nobody knew was stale.
func TestATransientFailureIsRetried(t *testing.T) {
	var calls atomic.Int32
	stub(t, []source{{
		name: "flaky",
		url:  "stub://flaky",
		parse: func(b []byte) ([]sanctions.Entry, error) {
			return []sanctions.Entry{{List: "flaky", RefID: "1"}}, nil
		},
	}})
	// The first call fails as the network actually did; the second succeeds.
	one := func(context.Context, string) ([]byte, string, error) {
		if calls.Add(1) == 1 {
			return nil, "", errors.New("tls: received record with version 302 when expecting version 303")
		}
		return []byte("ok"), "digest", nil
	}

	out := fetch(context.Background(), one, nowait)
	if len(out) != 1 {
		t.Fatalf("got %d results, want 1", len(out))
	}
	if out[0].Err != nil {
		t.Fatalf("a publisher that failed once was recorded as failed: %v", out[0].Err)
	}
	if len(out[0].Entries) != 1 {
		t.Fatalf("got %d entries, want the retry's", len(out[0].Entries))
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("publisher was asked %d times, want 2 — one failure and one retry", n)
	}
}

// A publisher that is genuinely down must still be reported, with the attempts
// named. Retrying must not turn a real outage into silence.
func TestAPersistentFailureIsReportedAfterEveryAttempt(t *testing.T) {
	var calls atomic.Int32
	stub(t, []source{{name: "down", url: "stub://down",
		parse: func([]byte) ([]sanctions.Entry, error) { return nil, nil }}})
	one := func(context.Context, string) ([]byte, string, error) {
		calls.Add(1)
		return nil, "", errors.New("connection refused")
	}

	out := fetch(context.Background(), one, nowait)
	if out[0].Err == nil {
		t.Fatal("a publisher that never answered was recorded as a successful load")
	}
	if n := calls.Load(); n != attempts {
		t.Fatalf("publisher was asked %d times, want %d", n, attempts)
	}
	// The error has to say how hard we tried, or an operator cannot tell a blip
	// from an outage.
	if !strings.Contains(out[0].Err.Error(), "attempt 3 of 3") {
		t.Errorf("the failure does not say how many attempts were made: %v", out[0].Err)
	}
	if len(out[0].Entries) != 0 {
		t.Error("a failed publisher contributed entries")
	}
}

// A parse failure must not be retried. It is a disagreement about the schema and
// will fail identically every time, so retrying only delays the refresh.
func TestAParseFailureIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	stub(t, []source{{name: "badschema", url: "stub://bad",
		parse: func([]byte) ([]sanctions.Entry, error) { return nil, errors.New("unexpected root element") }}})
	one := func(context.Context, string) ([]byte, string, error) {
		calls.Add(1)
		return []byte("<wrong/>"), "digest", nil
	}

	out := fetch(context.Background(), one, nowait)
	if out[0].Err == nil {
		t.Fatal("a list that could not be parsed was recorded as loaded")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("a parse failure was retried %d times; it will fail identically every time", n)
	}
}

// One publisher failing must not abandon the others. Screening against three lists
// is wrong, but screening against none because the first one blipped is worse.
func TestOnePublisherFailingDoesNotAbandonTheRest(t *testing.T) {
	stub(t, []source{
		{name: "first", url: "stub://a", parse: func([]byte) ([]sanctions.Entry, error) { return nil, nil }},
		{name: "second", url: "stub://b", parse: func([]byte) ([]sanctions.Entry, error) {
			return []sanctions.Entry{{List: "second", RefID: "1"}}, nil
		}},
	})
	one := func(_ context.Context, url string) ([]byte, string, error) {
		if strings.HasSuffix(url, "a") {
			return nil, "", errors.New("down")
		}
		return []byte("ok"), "d", nil
	}

	out := fetch(context.Background(), one, nowait)
	if len(out) != 2 {
		t.Fatalf("got %d results, want one per publisher", len(out))
	}
	if out[0].Err == nil {
		t.Error("the failing publisher was reported as loaded")
	}
	if out[1].Err != nil || len(out[1].Entries) != 1 {
		t.Errorf("the healthy publisher was abandoned: %v", out[1].Err)
	}
}

// A cancelled context stops immediately. It means the deployment is shutting down,
// not that the publisher failed, so there is nothing to retry into.
func TestCancellationStopsRetrying(t *testing.T) {
	var calls atomic.Int32
	stub(t, []source{{name: "x", url: "stub://x",
		parse: func([]byte) ([]sanctions.Entry, error) { return nil, nil }}})
	one := func(context.Context, string) ([]byte, string, error) {
		calls.Add(1)
		return nil, "", errors.New("interrupted")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out := fetch(ctx, one, nowait)
	if out[0].Err == nil {
		t.Fatal("a cancelled refresh reported a successful load")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("a cancelled refresh made %d attempts, want 1", n)
	}
}
