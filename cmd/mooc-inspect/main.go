// mooc-inspect reads existing-session catalog shapes without committing source data.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/dbbridge"
	"academic/backend/internal/model"
	"academic/backend/internal/mooc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	k, err := base64.StdEncoding.DecodeString(os.Getenv("CREDENTIAL_KEY"))
	if err != nil {
		return fmt.Errorf("configuration unavailable")
	}
	defer clear(k)
	db, err := dbbridge.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("database unavailable")
	}
	defer db.Close()
	vault, err := credential.New(k)
	if err != nil {
		return fmt.Errorf("vault unavailable")
	}
	targets, err := db.Platforms().Targets(ctx, "", 100)
	if err != nil {
		return fmt.Errorf("connections unavailable")
	}
	cs := []model.PlatformConnection{}
	for _, c := range targets {
		if c.Source == "mooc" && c.Enabled && c.State == "active" {
			cs = append(cs, c)
		}
	}
	if len(cs) != 1 {
		return fmt.Errorf("expected exactly one active MOOC connection; found %d", len(cs))
	}
	c := cs[0]
	lease, err := db.Platforms().Acquire(ctx, model.DWSScope{UserID: c.UserID, ConnectionID: c.ID})
	if err != nil {
		return fmt.Errorf("connection busy")
	}
	state, err := db.Platforms().Load(ctx, lease, "course")
	releaseErr := db.Platforms().Release(ctx, lease)
	if err != nil || releaseErr != nil {
		return fmt.Errorf("connection state unavailable")
	}
	session, err := vault.Open(c.UserID, c.ID, state.Session)
	if err != nil {
		return fmt.Errorf("session unavailable")
	}
	defer clear(session)
	if os.Getenv("MOOC_INSPECT_SYNC") == "1" {
		return mooc.InspectDiscussionSync(ctx, contract.Dependencies{}, contract.Scope{UserID: c.UserID, ConnectionID: c.ID}, session, func(v map[string]any) { _ = json.NewEncoder(os.Stdout).Encode(v) })
	}
	if os.Getenv("MOOC_INSPECT_COMPLETION") == "1" {
		return mooc.InspectCompletion(ctx, contract.Dependencies{}, contract.Scope{UserID: c.UserID, ConnectionID: c.ID}, session, func(v map[string]any) { _ = json.NewEncoder(os.Stdout).Encode(v) })
	}
	if os.Getenv("MOOC_INSPECT_COURSE_METADATA") == "1" {
		return mooc.InspectCourseMetadata(ctx, contract.Dependencies{}, contract.Scope{UserID: c.UserID, ConnectionID: c.ID}, session, func(v map[string]any) { _ = json.NewEncoder(os.Stdout).Encode(v) })
	}
	if os.Getenv("MOOC_INSPECT_DETAILS") == "1" {
		return mooc.InspectReadDetails(ctx, contract.Dependencies{}, contract.Scope{UserID: c.UserID, ConnectionID: c.ID}, session, func(v map[string]any) { _ = json.NewEncoder(os.Stdout).Encode(v) })
	}
	return mooc.InspectCatalog(ctx, contract.Dependencies{}, contract.Scope{UserID: c.UserID, ConnectionID: c.ID}, session, func(v map[string]any) { _ = json.NewEncoder(os.Stdout).Encode(v) })
}
