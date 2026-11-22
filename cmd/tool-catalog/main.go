// Command tool-catalog exports the actual host tool registrations offline.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	f "academic/agent/framework"
	"academic/backend/internal/actions"
	"academic/backend/internal/agent"
	stage "academic/backend/internal/learnerstage"
	"academic/backend/internal/model"
)

// Only specs are exported. No live repositories, handlers, models or timers.
type catalogActions struct{ model.ActionRepository }

func hostTools() ([]f.ToolSpec, error) {
	r := f.NewToolRegistry()
	if err := agent.RegisterScheduleTools(r, nil); err != nil {
		return nil, err
	}
	session := stage.New(nil, nil, "catalog-owner", "catalog-agent", "INITIALIZE", "")
	if err := agent.RegisterLearnerStageTools(r, session); err != nil {
		return nil, err
	}
	if err := actions.RegisterTools(r, catalogActions{}); err != nil {
		return nil, err
	}
	return r.Catalog(), nil
}
func main() {
	check := flag.String("check", "", "Compare the registered host contracts with this JSON file")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(1)
	}
	items, err := hostTools()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	if *check != "" {
		raw, e := os.ReadFile(*check)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		var previous any
		if json.Unmarshal(raw, &previous) != nil {
			fmt.Fprintln(os.Stderr, "invalid catalog JSON")
			os.Exit(1)
		}
		actualRaw, _ := json.Marshal(items)
		var normalized any
		_ = json.Unmarshal(actualRaw, &normalized)
		a, _ := json.Marshal(normalized)
		b, _ := json.Marshal(previous)
		if string(a) != string(b) {
			fmt.Fprintln(os.Stderr, "host catalog differs from actual registrations")
			os.Exit(1)
		}
		fmt.Printf("Verified %d actual host tool contracts against %s\n", len(items), *check)
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(items); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
