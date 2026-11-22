// Command control-drain parks only queued batches before a deployment.
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
	"regexp"
	"syscall"
	"time"

	"academic/backend/internal/dbbridge"
	"academic/backend/internal/model"
)

type options struct {
	round, output string
	execute       bool
}

func validate(o options) error {
	if !o.execute {
		return errors.New("--execute is required; queue unchanged")
	}
	if !regexp.MustCompile(`^(?:replay_)?[a-f0-9]{32}$`).MatchString(o.round) {
		return errors.New("exact --round is required")
	}
	if o.output == "" {
		return errors.New("new --output-dir is required")
	}
	return nil
}
func main() {
	var o options
	flag.StringVar(&o.round, "round", "", "exact round to drain")
	flag.StringVar(&o.output, "output-dir", "", "new private backup directory")
	flag.BoolVar(&o.execute, "execute", false, "explicitly park queued batches, never running batches")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e := run(ctx, o); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(ctx context.Context, o options) error {
	if e := validate(o); e != nil {
		return e
	}
	dir, e := filepath.Abs(o.output)
	if e != nil {
		return e
	}
	if e = os.Mkdir(dir, 0700); e != nil {
		return errors.New("backup directory must be new and creatable")
	}
	db, e := dbbridge.Open(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return errors.New("database unavailable")
	}
	defer db.Close()
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	count, e := db.PauseControlRoundQueue(c, o.round, func(batches []model.ControlBatch) error {
		return durableBackup(dir, map[string]any{"round_id": o.round, "at": time.Now().UTC(), "before": batches, "operation": "queued_to_pending", "reason": "DEPLOYMENT_DRAIN", "running_untouched": true})
	})
	if e != nil {
		return e
	}
	if e = durableJSON(filepath.Join(dir, "result.private.json"), map[string]any{"round_id": o.round, "at": time.Now().UTC(), "parked": count, "committed": true}); e != nil {
		return fmt.Errorf("drain committed (%d batches); result receipt failed, inspect backup and database: %w", count, e)
	}
	fmt.Printf("Drain committed: round=%s parked=%d; running batches were not changed.\n", o.round, count)
	return nil
}
func durableBackup(dir string, value any) error {
	if e := durableJSON(filepath.Join(dir, "before.private.json"), value); e != nil {
		return e
	}
	f, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func durableJSON(path string, value any) error {
	raw, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(raw); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	return f.Close()
}
