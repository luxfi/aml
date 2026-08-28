// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package instance opens a second Base app over a copy of a first one's data
// directory — which is what a restarted pod reads, and the only honest way to
// test that a record plane is durable.
//
// It is one helper because there is one question: is the row still there when the
// process that wrote it is gone. A test that reopens the SAME app answers a
// different and much weaker question, and a per-package copy of this walk is five
// chances for one of them to quietly reopen the same handle.
package instance

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/base/tests"
)

// New opens a fresh instance over an empty data directory.
func New(t testing.TB) *tests.TestApp {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("instance: %v", err)
	}
	return app
}

// Restart takes the first instance's data directory as it stands, drops the
// instance, and opens a second one over those bytes.
//
// The first app is cleaned up here rather than left to the caller, because a test
// that keeps both open is testing two handles on one file and not a restart.
func Restart(t testing.TB, first *tests.TestApp) *tests.TestApp {
	t.Helper()
	dir := copyTree(t, first.DataDir())
	first.Cleanup()

	second, err := tests.NewTestApp(dir)
	if err != nil {
		t.Fatalf("instance: second instance: %v", err)
	}
	t.Cleanup(second.Cleanup)
	return second
}

func copyTree(t testing.TB, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
	if err != nil {
		t.Fatalf("instance: copy data dir: %v", err)
	}
	return dst
}
