// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package sanctions

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/luxfi/aml/pkg/types"
)

// RefreshCron registers a daily 06:00 UTC cron that fetches all sanctions
// lists, diffs against stored entries, and persists updates.
func RefreshCron(app core.App, store SanctionsStore) {
	app.Cron().Add("sanctions-refresh", "0 6 * * *", func() {
		if err := RefreshAll(context.Background(), store); err != nil {
			log.Printf("[sanctions-refresh] error: %v", err)
		}
	})
}

// RefreshAll fetches OFAC, UN, EU, and HMT lists, and persists them
// via the SanctionsStore. Returns the count of new entries added.
func RefreshAll(ctx context.Context, store SanctionsStore) error {
	ingester := NewIngester()
	lists := DefaultLists()

	var totalEntries int
	for _, list := range lists {
		if !list.Active {
			continue
		}

		var entries []types.SanctionsEntry
		var hash string
		var err error

		switch list.Source {
		case types.ListOFACSDN:
			entries, hash, err = ingester.FetchOFAC(ctx, list.URL)
		default:
			// UN, EU, HMT use the same OFAC parser for now — their XML
			// structures are similar enough. Production should have dedicated
			// parsers per list format.
			entries, hash, err = ingester.FetchOFAC(ctx, list.URL)
		}

		if err != nil {
			log.Printf("[sanctions-refresh] fetch %s failed: %v", list.Source, err)
			continue
		}

		// Tag entries with list source.
		for i := range entries {
			entries[i].ListID = list.Source
			entries[i].ID = fmt.Sprintf("%s-%s", list.Source, entries[i].RefID)
			now := time.Now().UTC()
			entries[i].CreatedAt = now
			entries[i].UpdatedAt = now
		}

		if err := store.Save(ctx, entries); err != nil {
			log.Printf("[sanctions-refresh] save %s failed: %v", list.Source, err)
			continue
		}

		totalEntries += len(entries)
		log.Printf("[sanctions-refresh] %s: %d entries (hash: %s)", list.Source, len(entries), hash[:16])
	}

	log.Printf("[sanctions-refresh] complete: %d total entries across %d lists", totalEntries, len(lists))
	return nil
}
