// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package sanctions

import (
	"sync"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// Monitor holds the outcome of the most recent refresh of each list.
//
// It exists because the expensive failure in screening is silent. A list that
// stopped loading — a retired endpoint, a changed schema, a token that now
// answers 403 — produces no matches, and no matches is what a clean party also
// produces. Two of the four built-in sources had been returning nothing for
// months without any surface reporting it. Readiness has to be a question the
// operator can ask, per source, and get a date for.
type Monitor struct {
	mu    sync.RWMutex
	state map[string]Source
}

// Source is the readiness of one list.
type Source struct {
	// Name is the list source identifier.
	Name string `json:"source"`
	// Entries is how many entries the last successful load produced.
	Entries int `json:"entries"`
	// LoadedAt is when that load happened. Zero means the list has never loaded
	// in this process, which is not the same as a list with nobody on it.
	LoadedAt time.Time `json:"loaded_at"`
	// SHA256 is the digest of the payload that produced Entries, so an operator
	// can tell a refresh that changed nothing from a refresh that did not run.
	SHA256 string `json:"sha256,omitempty"`
	// Err is why the last attempt failed, empty if it succeeded.
	Err string `json:"error,omitempty"`
	// AttemptedAt is when the last attempt happened, successful or not.
	AttemptedAt time.Time `json:"attempted_at"`
}

// Fresh reports whether this source is fit to screen against as of now.
//
// A source that has never loaded is not fresh, whatever its last attempt said: an
// empty list matches nobody. A source whose last attempt failed is still fresh
// while its loaded data is inside maxListAge, because yesterday's designations
// remain better than none — but it stops being fresh when that data ages out,
// rather than staying fresh forever on the strength of one old success.
func (s Source) Fresh(now time.Time) bool {
	if s.LoadedAt.IsZero() || s.Entries == 0 {
		return false
	}
	return now.Sub(s.LoadedAt) <= maxListAge
}

// Age is how long since this source last loaded.
func (s Source) Age(now time.Time) time.Duration {
	if s.LoadedAt.IsZero() {
		return 0
	}
	return now.Sub(s.LoadedAt)
}

// NewMonitor builds a Monitor with every configured source recorded as never
// loaded, so a source that has never once succeeded is visible rather than absent
// from the report.
func NewMonitor(lists []types.SanctionsList) *Monitor {
	m := &Monitor{state: make(map[string]Source, len(lists))}
	for _, l := range lists {
		if l.Active {
			m.state[l.Source] = Source{Name: l.Source}
		}
	}
	return m
}

// Record folds the results of a refresh into the monitor. A failed attempt keeps
// the previous successful load's entry count and date: what is stored is still
// what screening runs against, and forgetting it would report the list as empty
// when it is merely stale.
func (m *Monitor) Record(results []Result, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range results {
		s := m.state[r.Source]
		s.Name = r.Source
		s.AttemptedAt = at
		if r.Err != nil {
			s.Err = r.Err.Error()
		} else {
			s.Err = ""
			s.Entries = r.Entries
			s.SHA256 = r.SHA256
			s.LoadedAt = at
		}
		m.state[r.Source] = s
	}
}

// Sources returns the readiness of every configured list.
func (m *Monitor) Sources() []Source {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Source, 0, len(m.state))
	for _, s := range m.state {
		out = append(out, s)
	}
	sortSources(out)
	return out
}

// Ready reports whether every configured source is fit to screen against, and
// names those that are not.
//
// It is all-or-nothing by design. Screening against three of four lists is not
// three-quarters of a control: a party designated only on the missing list passes
// entirely, and the result is indistinguishable from a clean one.
func (m *Monitor) Ready(now time.Time) (bool, []string) {
	var stale []string
	for _, s := range m.Sources() {
		if !s.Fresh(now) {
			stale = append(stale, s.Name)
		}
	}
	return len(stale) == 0, stale
}

func sortSources(s []Source) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Name < s[j-1].Name; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
