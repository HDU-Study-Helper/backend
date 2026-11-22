package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDrainExplicitAndDurable(t *testing.T) {
	if validate(options{round: "replay_0123456789abcdef0123456789abcdef", output: "x"}) == nil {
		t.Fatal("accepted no execute")
	}
	if validate(options{execute: true, round: "../public", output: "x"}) == nil {
		t.Fatal("accepted bad round")
	}
	dir := t.TempDir()
	if e := durableBackup(dir, map[string]int{"before": 1}); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(dir, "before.private.json")
	s, e := os.Stat(p)
	if e != nil || s.Mode().Perm() != 0600 {
		t.Fatal(s, e)
	}
	if durableBackup(dir, map[string]int{"before": 2}) == nil {
		t.Fatal("overwrote previous backup")
	}
}
