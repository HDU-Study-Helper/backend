// Command collect-once performs exactly one bounded collection pass, never Agent processing.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"academic/backend/internal/app"
)

func main() {
	output := flag.String("output", "", "write safe collection report JSON to this path")
	timeout := flag.Duration("timeout", 30*time.Minute, "collection deadline (maximum 30m)")
	flag.Parse()
	if *output == "" || *timeout <= 0 || *timeout > 30*time.Minute {
		fmt.Fprintln(os.Stderr, "output is required; timeout must be within (0,30m]")
		os.Exit(2)
	}
	cfg, err := app.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	instance, err := app.New(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer instance.Close()
	result, err := instance.RunCollectionOnce(ctx)
	report := struct {
		app.CollectionResult
		Error string `json:"error,omitempty"`
	}{CollectionResult: result}
	if err != nil {
		report.Error = "collection incomplete; inspect per-source results and resource sync status"
	}
	raw, encodeErr := json.MarshalIndent(report, "", "  ")
	if encodeErr != nil {
		fmt.Fprintln(os.Stderr, "cannot encode collection report")
		os.Exit(1)
	}
	if writeErr := os.WriteFile(*output, append(raw, '\n'), 0600); writeErr != nil {
		fmt.Fprintln(os.Stderr, "cannot write collection report")
		os.Exit(1)
	}
	fmt.Println(string(raw))
	if err != nil {
		os.Exit(1)
	}
}
