// academic-refresh performs one complete authorized Chaoxing/MOOC read pass.
// It saves evidence and projection atomically; it never starts Agent workers or DWS.
package main

import (
	"academic/backend/internal/chaoxing"
	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/dbbridge"
	"academic/backend/internal/model"
	"academic/backend/internal/mooc"
	"academic/backend/internal/platform"
	"academic/backend/internal/runcontrol"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// selectedResources narrows an explicit maintenance pass without changing production descriptors.
type selectedResources struct {
	contract.Adapter
	resources []contract.Resource
}

func (s selectedResources) Describe() contract.Descriptor {
	d := s.Adapter.Describe()
	d.Resources = s.resources
	return d
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "academic collection incomplete; inspect report and source coverage")
		os.Exit(1)
	}
}
func run() error {
	options, err := parseCollectionOptions(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	db, err := dbbridge.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(os.Getenv("CREDENTIAL_KEY"))
	if err != nil {
		return err
	}
	defer clear(key)
	vault, err := credential.New(key)
	if err != nil {
		return err
	}
	deps := contract.Dependencies{HTTPClient: &http.Client{
		Timeout:   40 * time.Second,
		Transport: newPacedTransport(http.DefaultTransport, options.interval),
	}}
	cx, err := chaoxing.New(deps)
	if err != nil {
		return err
	}
	mc, err := mooc.New(deps)
	if err != nil {
		return err
	}
	adapters := []contract.Adapter{cx, mc}
	if source := os.Getenv("ACADEMIC_SOURCE"); source != "" {
		adapters = nil
		switch source {
		case "chaoxing":
			adapters = []contract.Adapter{cx}
		case "mooc":
			adapters = []contract.Adapter{mc}
		default:
			return contract.ErrInvalid
		}
	}
	if selected := os.Getenv("ACADEMIC_RESOURCES"); selected != "" {
		if len(adapters) != 1 || adapters[0].Describe().Source != "mooc" {
			return contract.ErrInvalid
		}
		available := map[string]bool{}
		for _, resource := range adapters[0].Describe().Resources {
			available[string(resource)] = true
		}
		resources := []contract.Resource{}
		seen := map[string]bool{}
		for _, name := range strings.Split(selected, ",") {
			name = strings.TrimSpace(name)
			if !available[name] || seen[name] {
				return contract.ErrInvalid
			}
			seen[name] = true
			resources = append(resources, contract.Resource(name))
		}
		adapters[0] = selectedResources{adapters[0], resources}
	}
	svc, err := platform.New(db.Platforms(), vault, adapters)
	if err != nil {
		return err
	}
	ctx, _ = model.WithRefreshReport(ctx)
	round, err := collectRound(ctx, db.Control(), func(ctx context.Context) error {
		return svc.CollectOnceWithTimeout(ctx, options.timeout)
	})
	if round == "" {
		return err
	}
	results := model.RefreshSnapshot(ctx)
	report := map[string]any{"round_id": round, "results": results, "failed": err != nil, "agents_started": false}
	raw, encodeErr := json.MarshalIndent(report, "", "  ")
	if encodeErr != nil {
		return encodeErr
	}
	output := os.Getenv("ACADEMIC_COLLECTION_REPORT")
	if output == "" {
		output = "../.build/academic-collection-report.json"
	}
	if writeErr := os.WriteFile(output, raw, 0600); writeErr != nil {
		return writeErr
	}
	fmt.Println(string(raw))
	return err
}

func collectRound(ctx context.Context, repo model.ControlRepository, collect func(context.Context) error) (round string, err error) {
	instance := "academic-refresh-" + rand.Text()
	round, err = repo.StartRound(ctx, instance)
	if err != nil {
		return "", err
	}
	ctx = model.WithControlRound(ctx, round)
	stopHeartbeat := runcontrol.KeepRefreshAlive(ctx, repo, round, instance)
	defer func() {
		stopHeartbeat()
		cleanup, end := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer end()
		err = errors.Join(err, repo.FinishRound(cleanup, round, model.RefreshSnapshot(ctx), err != nil || ctx.Err() != nil))
		err = errors.Join(err, repo.Seal(cleanup))
	}()
	err = collect(ctx)
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return round, err
}
