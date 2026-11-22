package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionArguments(t *testing.T) {
	for _, o := range []options{{}, {round: "abc", execute: true, output: "x"}, {round: "0123456789abcdef0123456789abcdef", output: "x"}, {round: "0123456789abcdef0123456789abcdef", execute: true}} {
		if validate(o) == nil {
			t.Fatal("unsafe options accepted", o)
		}
	}
	for _, id := range []string{"0123456789abcdef0123456789abcdef", "replay_0123456789abcdef0123456789abcdef"} {
		if e := validate(options{round: id, execute: true, output: "x"}); e != nil {
			t.Fatal(e)
		}
	}
}
func TestReportPrivateAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if e := save(path, map[string]int{"count": 1}); e != nil {
		t.Fatal(e)
	}
	if e := save(path, map[string]int{"count": 2}); e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, e)
	}
	raw, e := os.ReadFile(path)
	if e != nil || string(raw) != "{\n  \"count\": 2\n}" {
		t.Fatal(string(raw), e)
	}
}
