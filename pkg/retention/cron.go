// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package retention

import (
	"log"
	"time"

	"github.com/hanzoai/base/core"
)

// Cron registers the daily disposal at 03:30 UTC.
//
// A run that cannot prove what it destroyed reports nothing as disposed and logs
// the failure, and the next run tries again. Deletion is an obligation with a
// deadline measured in years, so a day of retries is safe where a false "done"
// is not.
func Cron(app core.App, l *Ledger) {
	app.Cron().Add("aml-retention-dispose", "30 3 * * *", func() {
		d, err := l.Dispose(time.Now().UTC())
		if err != nil {
			log.Printf("[aml-retention] disposal not proven, nothing reported as disposed: %v", err)
			return
		}
		log.Printf("[aml-retention] examined %d records, disposed %d", d.Examined, len(d.Disposed))
	})
}
