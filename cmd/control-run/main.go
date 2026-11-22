// Command control-run executes one explicitly activated archived round.
// It never mounts HTTP, collectors, recovery, activation or notification delivery.
package main

import (
	"context"
	"encoding/base64"
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

	"academic/backend/internal/agentadmin"
	"academic/backend/internal/bridge"
	"academic/backend/internal/credential"
	"academic/backend/internal/dbbridge"
	"academic/backend/internal/runcontrol"
	"academic/backend/internal/schedule"
)

type options struct {
	round, output  string
	execute, trace bool
}

var roundPattern = regexp.MustCompile(`^(?:replay_)?[a-f0-9]{32}$`)

func validate(o options) error {
	if !o.execute {
		return errors.New("--execute is required; no processing started")
	}
	if !roundPattern.MatchString(o.round) {
		return errors.New("--round must be a 32-hex ID or replay_ followed by 32 hex characters")
	}
	if o.output == "" {
		return errors.New("--output-dir is required")
	}
	return nil
}
func main() {
	var o options
	flag.StringVar(&o.round, "round", "", "exact activated archived round ID")
	flag.BoolVar(&o.execute, "execute", false, "explicitly execute only this round")
	flag.StringVar(&o.output, "output-dir", "", "new private output directory")
	flag.BoolVar(&o.trace, "trace", false, "record private model requests/responses by Agent Run")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, o options) error {
	if err := validate(o); err != nil {
		return err
	}
	// New directory only: don't overwrite evidence from an earlier execution.
	output, err := filepath.Abs(o.output)
	if err != nil {
		return err
	}
	if err = os.Mkdir(output, 0700); err != nil {
		return errors.New("output directory must be new and creatable")
	}
	db, err := dbbridge.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return errors.New("database unavailable")
	}
	defer db.Close()
	before, err := db.ControlRoundProgress(ctx, o.round)
	if err != nil {
		return errors.New("cannot inspect requested round")
	}
	if err = before.Preflight(); err != nil {
		return err
	}
	if err = save(filepath.Join(output, "preflight.private.json"), before); err != nil {
		return err
	}
	if before.Done() {
		fmt.Println("Round is already terminal; no processing started")
		return save(filepath.Join(output, "progress.private.json"), before)
	}
	key, err := base64.StdEncoding.DecodeString(os.Getenv("CREDENTIAL_KEY"))
	if err != nil || len(key) != 32 {
		return errors.New("valid CREDENTIAL_KEY required")
	}
	vault, err := credential.New(key)
	clear(key)
	if err != nil {
		return errors.New("credential vault unavailable")
	}
	console, err := agentadmin.New(ctx, db.Agents(), db.AgentAdmin(), db.Users(), vault, false, db.AgentData(), schedule.New(db.Schedules(), bridge.Demo{}))
	if err != nil {
		return errors.New("Agent service configuration unavailable")
	}
	store, err := db.ControlRoundRepository(o.round)
	if err != nil {
		return err
	}
	if o.trace {
		console.Service.Runtime.Resolver = &traceResolver{inner: console.Settings, dir: output}
	}
	service := runcontrol.New(store, console.Service, db.AgentData())
	// Recheck immediately before workers start; the scoped Claim is the final
	// boundary if someone queues another round after this read.
	if before, err = db.ControlRoundProgress(ctx, o.round); err != nil {
		return err
	}
	if err = before.Preflight(); err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- service.Run(workerCtx) }()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	persist := func(c context.Context) (dbbridge.ControlRoundProgress, error) {
		p, e := db.ControlRoundProgress(c, o.round)
		if e != nil {
			return p, e
		}
		if e = save(filepath.Join(output, "progress.private.json"), p); e != nil {
			return p, e
		}
		fmt.Printf("round=%s total=%d queued=%d running=%d succeeded=%d partial=%d ignored=%d failed=%d blocked=%d artifacts=%d\n", p.RoundID, p.Total, p.Batches["queued"], p.Batches["running"], p.Batches["succeeded"], p.Batches["partial"], p.Batches["ignored"], p.Batches["failed"], p.Batches["blocked"], p.Artifacts)
		return p, nil
	}
	shutdown := func(reason error) error {
		cancel()
		workerErr := <-finished
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_, saveErr := persist(cleanup)
		if reason != nil {
			return reason
		}
		if saveErr != nil {
			return saveErr
		}
		if workerErr != nil && !errors.Is(workerErr, context.Canceled) {
			return workerErr
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return shutdown(ctx.Err())
		case workerErr := <-finished:
			cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
			_, saveErr := persist(cleanup)
			stop()
			if workerErr != nil {
				return workerErr
			}
			return saveErr
		case <-ticker.C:
			p, e := persist(ctx)
			if e != nil {
				return shutdown(errors.New("progress persistence failed; processing cancelled"))
			}
			if p.OtherQueued != 0 || p.OtherRunning != 0 {
				return shutdown(errors.New("queue guard changed: unrelated round became queued/running; processing cancelled"))
			}
			if p.Done() {
				return shutdown(nil)
			}
		}
	}
}
func save(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".progress-*")
	if err != nil {
		return err
	}
	name := file.Name()
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err = file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
