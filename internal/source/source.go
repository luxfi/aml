// Package source reads a package's own Go source and refuses a shape.
//
// Some properties are not about what the code does today but about what it is
// still able to express. "No tenant is ever evicted to make room for another"
// is one of those: a behavioural test proves it of the code as written, and the
// same defect walks back in the moment somebody adds a map keyed by tenant with
// a cap over it. So the shape itself is refused, in the package that must not
// have it, by reading the file.
//
// It is deliberately an AST walk and not a grep. A grep for "delete(" matches a
// comment and misses a method named remove; the parser sees the declaration.
package source

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// file parses one file of the package under test.
func file(t *testing.T, name string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return fset, parsed
}

// NoMapByTenant refuses a map keyed by a string on the named type.
//
// A process-wide store holding every tenant's state in one map with one cap is
// the shape that lets one institution's traffic evict another's — silently, with
// no error, on a control whose absence looks exactly like a clean result. Per
// tenant state belongs in roster.Roster, which admits and never removes, so the
// eviction path is not reachable rather than merely unused.
func NoMapByTenant(t *testing.T, name, typeName string) {
	t.Helper()
	fset, parsed := file(t, name)
	found := false
	ast.Inspect(parsed, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != typeName {
			return true
		}
		found = true
		structure, ok := spec.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, f := range structure.Fields.List {
			m, ok := f.Type.(*ast.MapType)
			if !ok {
				continue
			}
			if key, ok := m.Key.(*ast.Ident); ok && key.Name == "string" {
				t.Errorf("%s: %s holds a map keyed by a string. One map of every tenant's state under one cap is how one institution's traffic evicts another's; per-tenant state goes in roster.Roster, which cannot remove.",
					fset.Position(f.Pos()), typeName)
			}
		}
		return false
	})
	if !found {
		t.Fatalf("%s declares no type %s, so this test is checking nothing", name, typeName)
	}
}

// NoRemoval refuses any removal from a map in the named file.
//
// It is what makes roster's promise structural: a set that admits and never
// removes has no expression that could take one tenant's state, so a caller
// cannot reach one however it is composed.
func NoRemoval(t *testing.T, name string) {
	t.Helper()
	fset, parsed := file(t, name)
	ast.Inspect(parsed, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "delete" {
			t.Errorf("%s: delete() — this set admits and never removes, which is the whole of what it is for",
				fset.Position(call.Pos()))
		}
		return true
	})
}
