// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import (
	"net/http"
	"time"

	"github.com/hanzoai/base/core"
)

// screenStale mirrors the daemon's list-staleness budget. The lists refresh daily,
// so two days means two consecutive failures have gone unnoticed.
const screenStale = 48 * time.Hour

// screeningSources answers, per list, whether this instance is fit to screen
// against it and when it last loaded.
//
// The question is worth a route of its own because the failure it reports is
// otherwise invisible: a list that has stopped loading returns no matches, and no
// matches is exactly what a clean party returns. An operator has no way to tell
// "nobody on your payment is designated" from "the list you are screening against
// is empty" without being told the date and the count.
//
// It answers 503 when any source is unfit, so a readiness probe and a dashboard
// read the same truth rather than each deciding for itself.
func (h *Handler) screeningSources() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		if _, err := h.tenant(e); err != nil {
			return refuse(e, err)
		}
		if h.Readiness == nil {
			return fail(e, http.StatusServiceUnavailable, "screening readiness is not wired")
		}

		now := time.Now().UTC()
		lists := h.Readiness.Lists()
		unfit := h.Readiness.Unfit(now, screenStale)
		ready := len(unfit) == 0

		type source struct {
			Source      string  `json:"source"`
			Entries     int     `json:"entries"`
			LoadedAt    *string `json:"loaded_at"`
			AgeHours    float64 `json:"age_hours"`
			Fresh       bool    `json:"fresh"`
			SHA256      string  `json:"sha256,omitempty"`
			Error       string  `json:"error,omitempty"`
			AttemptedAt *string `json:"attempted_at"`
		}

		out := make([]source, 0, len(lists))
		total := 0
		for _, s := range lists {
			row := source{
				Source:  s.Source,
				Entries: s.Designations,
				Fresh:   s.Fit(now, screenStale),
				SHA256:  s.Digest,
				Error:   s.Err,
			}
			if !s.LoadedAt.IsZero() {
				t := s.LoadedAt.Format(time.RFC3339)
				row.LoadedAt = &t
				row.AgeHours = s.Age(now).Hours()
			}
			if !s.AttemptedAt.IsZero() {
				t := s.AttemptedAt.Format(time.RFC3339)
				row.AttemptedAt = &t
			}
			total += s.Designations
			out = append(out, row)
		}

		code := http.StatusOK
		if !ready {
			code = http.StatusServiceUnavailable
		}
		return e.JSON(code, map[string]any{
			"ready":         ready,
			"unfit":         unfit,
			"total_entries": total,
			"sources":       out,
		})
	}
}
