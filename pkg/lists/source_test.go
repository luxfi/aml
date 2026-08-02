package lists

import (
	"os"
	"testing"
)

// read is how the source-level invariants read this package's own files.
func read(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}
