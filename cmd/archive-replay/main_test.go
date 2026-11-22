package main

import (
	"os"
	"path/filepath"
	"testing"

	"academic/backend/internal/dbbridge"
)

func TestPrivateReplayManifestImmutableAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	r := dbbridge.ArchiveReplayReport{Key: "same", RoundID: "round", Digest: "frozen"}
	if e := immutableReport(dir, "prepared.private.json", r); e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(filepath.Join(dir, "prepared.private.json"))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, e)
	}
	r.Existing = true
	if e = immutableReport(dir, "prepared.private.json", r); e != nil {
		t.Fatal("same manifest retry refused", e)
	}
	r.BatchIDs = []string{"new-batch"}
	if immutableReport(dir, "prepared.private.json", r) == nil {
		t.Fatal("changed prepared batch identities reused an old backup")
	}
	r.BatchIDs = nil
	r.Digest = "different"
	if immutableReport(dir, "prepared.private.json", r) == nil {
		t.Fatal("different manifest overwrote backup")
	}
	raw, e := os.ReadFile(filepath.Join(dir, "prepared.private.json"))
	if e != nil {
		t.Fatal(e)
	}
	if string(raw) == "" {
		t.Fatal("backup empty")
	}
	if e = os.Chmod(dir, 0755); e != nil {
		t.Fatal(e)
	}
	if immutableReport(dir, "other.json", r) == nil {
		t.Fatal("nonprivate directory accepted")
	}
}
