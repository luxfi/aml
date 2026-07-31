package cases

import (
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// age rewinds a case's clock so a test can make one look old. It goes through
// the shelf because the state is the shelf's, and the two shelves have to be
// aged the same way for the same test to hold against both.
func age(t *testing.T, s *Store, id string, opened, updated time.Time) *types.Case {
	t.Helper()
	c, err := s.shelf.get(id)
	if err != nil || c == nil {
		t.Fatalf("age %s: %v", id, err)
	}
	if !opened.IsZero() {
		c.OpenedAt = opened
	}
	if !updated.IsZero() {
		c.UpdatedAt = updated
	}
	if err := s.shelf.put(c); err != nil {
		t.Fatalf("age %s: %v", id, err)
	}
	return c
}
