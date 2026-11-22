// Command archive-replay creates a new pending control round from authorized
// frozen database archives. It never calls models or upstream platforms.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"academic/backend/internal/dbbridge"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	var key, output string
	var execute, park bool
	flag.StringVar(&key, "key", "", "explicit idempotency key for this frozen archive replay")
	flag.StringVar(&output, "output", "", "private immutable output directory (required)")
	flag.BoolVar(&execute, "execute", false, "prepare a new pending round; default is read-only inventory")
	flag.BoolVar(&park, "park-queued", false, "dequeue existing unclaimed batches to pending, preserving their private backup and history")
	flag.Parse()
	if flag.NArg() != 0 || key == "" || output == "" {
		return errors.New("--key and --output are required; no positional arguments")
	}
	if park && !execute {
		return errors.New("--park-queued requires --execute")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	db, err := dbbridge.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	// Migrations are an explicit deployment operation, never an implicit dry-run.
	options := dbbridge.ArchiveReplayOptions{Key: key, Execute: execute, ParkQueued: park}
	if execute {
		options.Backup = func(_ context.Context, r dbbridge.ArchiveReplayReport) error {
			return immutableReport(output, "prepared.private.json", r)
		}
	}
	report, err := db.PrepareArchiveReplay(ctx, options)
	if err != nil {
		return err
	}
	name := "inventory.private.json"
	if execute {
		name = "committed.private.json"
	}
	if err = immutableReport(output, name, report); err != nil {
		return fmt.Errorf("report persistence failed (database operation may have committed; reuse the same key): %w", err)
	}
	fmt.Printf("archive replay: execute=%t existing=%t round=%s frozen=%d batches=%d queued_backups=%d; new batches remain pending, no activation performed\n", execute, report.Existing, report.RoundID, report.FreezeCount, len(report.BatchIDs), len(report.Parked))
	return nil
}
func immutableReport(dir, name string, value any) error {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("output directory must be private (0700)")
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := filepath.Join(absolute, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		existing, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var old, current dbbridge.ArchiveReplayReport
		if json.Unmarshal(existing, &old) == nil && json.Unmarshal(raw, &current) == nil && old.Key == current.Key && old.RoundID == current.RoundID && old.Digest == current.Digest {
			if name == "prepared.private.json" && (!reflect.DeepEqual(old.Parked, current.Parked) || !reflect.DeepEqual(old.BatchIDs, current.BatchIDs)) {
				return errors.New("prepared backup differs from the current queue or batch identities; use a new private output directory after review")
			}
			return nil
		}
		return errors.New("immutable replay report already exists with different manifest")
	}
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	parent, err := os.Open(absolute)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
