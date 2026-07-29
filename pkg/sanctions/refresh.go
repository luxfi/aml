// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package sanctions

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/hanzoai/base/core"
)

// RefreshCron registers a daily 06:00 UTC refresh of every active list.
func RefreshCron(app core.App, store SanctionsStore) {
	app.Cron().Add("sanctions-refresh", "0 6 * * *", func() {
		if _, err := RefreshAll(context.Background(), store); err != nil {
			log.Printf("[sanctions-refresh] FAILED: %v", err)
		}
	})
}

// Result reports what one list contributed to a refresh.
type Result struct {
	Source  string
	Entries int
	SHA256  string
	Err     error
}

// RefreshAll fetches every active list, parses it with that publisher's parser,
// and persists the entries. It returns one Result per list and a non-nil error if
// any list failed.
//
// A failing list does not abort the others — OFAC being unreachable must not stop
// the UN list loading — but it is never swallowed either. Each failure is joined
// into the returned error so the caller decides what a partial refresh means, and
// the freshness of what is stored stays answerable through Stale.
func RefreshAll(ctx context.Context, store SanctionsStore) ([]Result, error) {
	ingester := NewIngester()
	lists := DefaultLists()

	results := make([]Result, 0, len(lists))
	var errs []error

	for _, list := range lists {
		if !list.Active {
			continue
		}

		res := Result{Source: list.Source}
		entries, hash, err := ingester.Fetch(ctx, list)
		if err != nil {
			res.Err = err
			errs = append(errs, err)
			results = append(results, res)
			log.Printf("[sanctions-refresh] %s: %v", list.Source, err)
			continue
		}

		now := time.Now().UTC()
		for i := range entries {
			entries[i].ListID = list.Source
			entries[i].ID = fmt.Sprintf("%s-%s", list.Source, entries[i].RefID)
			entries[i].CreatedAt = now
			entries[i].UpdatedAt = now
		}

		if err := store.Save(ctx, entries); err != nil {
			res.Err = fmt.Errorf("save %s: %w", list.Source, err)
			errs = append(errs, res.Err)
			results = append(results, res)
			log.Printf("[sanctions-refresh] %s: %v", list.Source, res.Err)
			continue
		}

		res.Entries, res.SHA256 = len(entries), hash
		results = append(results, res)
		log.Printf("[sanctions-refresh] %s: %d entries (sha256 %s)", list.Source, len(entries), hash[:16])
	}

	if len(errs) > 0 {
		return results, fmt.Errorf("%d of %d lists failed: %w", len(errs), len(results), errors.Join(errs...))
	}
	log.Printf("[sanctions-refresh] complete: %d lists", len(results))
	return results, nil
}

// maxListAge is how long stored screening data may go without a successful
// refresh before it is treated as unfit to screen against. Publishers add
// designations on their own schedule and the duty to act on a new designation
// runs from the designation, not from whenever a fetch next happens to succeed,
// so silence for this long is a failure rather than a quiet period.
const maxListAge = 48 * time.Hour

// Stale reports whether stored sanctions data is too old to screen against, and
// how old it is. A zero refresh time means nothing has ever loaded, which is
// stale by definition rather than fresh.
//
// This exists because the expensive failure here is silent: with no freshness
// check, screening against an empty or months-old list returns no matches and
// looks exactly like screening a clean party.
func Stale(ctx context.Context, store SanctionsStore) (bool, time.Duration, error) {
	last, err := store.LastRefresh(ctx)
	if err != nil {
		return true, 0, err
	}
	if last.IsZero() {
		return true, 0, nil
	}
	age := time.Since(last)
	return age > maxListAge, age, nil
}
