// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import (
	"net/http"

	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/rules"
)

// catalog reports what this deployment detects, what obliges it, and what it does
// not detect.
//
// It serves the rule set actually installed rather than a document describing one,
// so the coverage claim is the running configuration and cannot drift from it. The
// gap list is published in the same response for the same reason: a catalog that
// shows only what is covered invites the reader to assume the remainder is covered
// too, and the honest surface names both.
func (h *Handler) catalog() func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		if _, err := h.tenant(e); err != nil {
			return refuse(e, err)
		}
		if h.Engine == nil {
			return fail(e, http.StatusServiceUnavailable, "no engine is wired")
		}

		installed := h.Engine.Rules()
		entries := make([]map[string]any, 0, len(installed))
		for _, r := range installed {
			entries = append(entries, map[string]any{
				"id":          r.ID,
				"name":        r.Name,
				"typology":    r.Typology,
				"description": r.Description,
				"expression":  r.DSL,
				"severity":    r.Severity,
				"action":      r.Action,
				"enabled":     r.Enabled,
				"citations":   r.Citations,
			})
		}
		return e.JSON(http.StatusOK, map[string]any{
			"typologies":  rules.Typologies(),
			"rules":       entries,
			"obligations": rules.Obligations(),
			"gaps":        rules.Gaps(),
		})
	}
}
