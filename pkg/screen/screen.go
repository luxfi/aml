// Package screen holds the loaded sanctions and politically-exposed-person lists
// and answers screening questions against them.
//
// There is one store. The engine's rules and the search endpoint consult the same
// loaded set, because the alternative — which is what the previous wiring did — is
// two stores where the refresh fills one and the endpoint queries the other, so
// screening answers "no match" to everything for ever and nothing in the system
// says so.
package screen

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/sanctions"
)

// Store holds the loaded lists.
type Store struct {
	mu       sync.RWMutex
	entries  []sanctions.Entry
	loaded   map[string]time.Time
	maxStale time.Duration
	now      func() time.Time
}

// New builds an empty store. maxStale is how old the newest load may be before
// screening reports an error rather than a clean result; zero disables the check,
// which is appropriate only in tests.
func New(maxStale time.Duration, now func() time.Time) *Store {
	return &Store{loaded: map[string]time.Time{}, maxStale: maxStale, now: now}
}

// Load replaces the entries for one list source.
//
// Replacing per source rather than wholesale means a failure to fetch one
// publisher leaves the others in place, and an empty result for a source is
// refused rather than silently clearing that publisher's designations.
func (s *Store) Load(source string, entries []sanctions.Entry) error {
	if len(entries) == 0 {
		return fmt.Errorf("screen: refusing to load zero entries for %q, which would clear its designations", source)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	kept := make([]sanctions.Entry, 0, len(s.entries)+len(entries))
	for _, e := range s.entries {
		if e.List != source {
			kept = append(kept, e)
		}
	}
	s.entries = append(kept, entries...)
	s.loaded[source] = s.reference()
	return nil
}

// Sources reports when each loaded list was last replaced.
func (s *Store) Sources() map[string]time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]time.Time, len(s.loaded))
	for k, v := range s.loaded {
		out[k] = v
	}
	return out
}

// Len reports how many designated subjects are loaded.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Ready reports why screening cannot be relied on, if it cannot.
//
// An empty or stale list is an error rather than a clean answer. A screening
// system that has loaded nothing answers "not designated" about every person on
// earth, and it does so at exactly the speed and with exactly the shape of a
// working one.
func (s *Store) Ready() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.entries) == 0 {
		return fmt.Errorf("screen: no list is loaded, so no name can be screened")
	}
	if s.maxStale <= 0 {
		return nil
	}
	var newest time.Time
	for _, at := range s.loaded {
		if at.After(newest) {
			newest = at
		}
	}
	if age := s.reference().Sub(newest); age > s.maxStale {
		return fmt.Errorf("screen: newest list load is %s old, past the %s limit", age.Truncate(time.Minute), s.maxStale)
	}
	return nil
}

// Search returns the matches for a query.
func (s *Store) Search(q sanctions.Query, threshold float64) ([]sanctions.Match, error) {
	if err := s.Ready(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	entries := s.entries
	s.mu.RUnlock()
	return sanctions.Screen(q, entries, threshold), nil
}

// Chain returns the matches for a blockchain address.
func (s *Store) Chain(address string) ([]sanctions.Match, error) {
	if err := s.Ready(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	entries := s.entries
	s.mu.RUnlock()
	return sanctions.ScreenChain(address, entries), nil
}

// Hit implements the engine's screening provider.
//
// A name that cannot be screened is an error, never a negative. The engine turns
// that into a review rather than an approval, which is the only safe reading of "we
// do not know".
func (s *Store) Hit(_ context.Context, name, class string) (engine.Hit, error) {
	switch class {
	case engine.ClassSanctions:
	case engine.ClassPEP:
		// There is no authoritative public list of politically exposed persons, and
		// none of the four sanctions publishers is one. Answering this from the
		// sanctions lists would report every screened customer as not politically
		// exposed, which is a control that reads as working and is not.
		return engine.Hit{}, fmt.Errorf("screen: no politically-exposed-person list is loaded; this deployment cannot answer %q", class)
	default:
		return engine.Hit{}, fmt.Errorf("screen: unknown class %q", class)
	}

	matches, err := s.Search(sanctions.Query{Name: name}, sanctions.Threshold)
	if err != nil {
		return engine.Hit{}, err
	}
	if len(matches) == 0 {
		return engine.Hit{}, nil
	}
	best := matches[0]
	return engine.Hit{
		Matched: true,
		Score:   best.Score,
		List:    best.Entry.List,
		EntryID: best.Entry.RefID,
		Name:    best.Name.Full,
	}, nil
}

func (s *Store) reference() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}
