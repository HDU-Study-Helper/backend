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
	"context"
	"encoding/base64"
	"encoding/json"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
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
	deps := contract.Dependencies{HTTPClient: &http.Client{Timeout: 40 * time.Second}}
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
	round, err := db.Control().StartRound(ctx, "academic-structure-refactor")
	if err != nil {
		return err
	}
	ctx = model.WithControlRound(ctx, round)
	err = svc.CollectOnce(ctx)
	cleanup, end := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer end()
	results := model.RefreshSnapshot(ctx)
	finishErr := db.Control().FinishRound(cleanup, round, results, err != nil)
	if finishErr != nil {
		return finishErr
	}
	if sealErr := db.Control().Seal(cleanup); sealErr != nil {
		return sealErr
	}
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
