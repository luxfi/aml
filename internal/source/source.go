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

// NoTable refuses a map keyed by a string on the named type.
//
// A table held by a long-lived value grows with whatever the keys come from, and
// in this engine the keys come from callers. Two shapes, one refusal:
//
//   - keyed by TENANT, it is one map of every institution's state under one cap,
//     which is how one institution's traffic evicts another's — silently, with no
//     error, on a control whose absence looks exactly like a clean result. Per
//     tenant state belongs in roster.Roster, which admits and never removes, so
//     the eviction path is not reachable rather than merely unused.
//   - keyed by anything else a request carries, it is an unbounded cache: it has
//     no cap at all, and the caller decides how many distinct keys exist.
//
// why states which of those the named type would be, in the caller's own words,
// so a failure names the defect rather than the rule.
func NoTable(t *testing.T, name, typeName, why string) {
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
				t.Errorf("%s: %s holds a map keyed by a string. %s",
					fset.Position(f.Pos()), typeName, why)
			}
		}
		return false
	})
	if !found {
		t.Fatalf("%s declares no type %s, so this test is checking nothing", name, typeName)
	}
}

// NoLiteral refuses a composite literal of pkg.Name in the named file.
//
// It is how "there is ONE way to build this" is kept true. A type with a dozen
// fields can be built by hand anywhere, and a second construction is not wrong
// on the day it is written — it is wrong on the day a field is added to one copy
// and not the other, which is a day nobody is looking.
func NoLiteral(t *testing.T, name, pkg, typeName, why string) {
	t.Helper()
	fset, parsed := file(t, name)
	ast.Inspect(parsed, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != typeName {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
			t.Errorf("%s: %s builds a %s.%s of its own. %s",
				fset.Position(lit.Pos()), name, pkg, typeName, why)
		}
		return true
	})
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
