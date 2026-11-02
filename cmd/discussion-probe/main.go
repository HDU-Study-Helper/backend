package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"academic/backend/internal/app"
	"academic/backend/internal/chaoxing"
	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/dbbridge"
	"academic/backend/internal/model"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "Discussion probe failed:", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cfg, e := app.LoadConfig()
	if e != nil {
		return errors.New("configuration unavailable")
	}
	db, e := dbbridge.Open(ctx, cfg.DatabaseURL)
	if e != nil {
		return errors.New("database unavailable")
	}
	defer db.Close()
	user, e := db.Users().GetByUsername(ctx, "admin")
	if e != nil {
		return errors.New("application owner not found")
	}
	connections, e := db.Platforms().List(ctx, user.ID)
	if e != nil {
		return errors.New("connections unavailable")
	}
	var selected model.PlatformConnection
	count := 0
	for _, c := range connections {
		if c.Source == "chaoxing" && c.Enabled && c.State == "active" {
			selected = c
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("expected one active Chaoxing connection, found %d", count)
	}
	scope := model.DWSScope{UserID: user.ID, ConnectionID: selected.ID}
	lease, e := db.Platforms().Acquire(ctx, scope)
	if e != nil {
		return errors.New("connection busy; retry after current sync")
	}
	defer db.Platforms().Release(context.Background(), lease)
	state, e := db.Platforms().Load(ctx, lease, "")
	if e != nil {
		return errors.New("session unavailable")
	}
	vault, e := credential.New(cfg.CredentialKey)
	if e != nil {
		return errors.New("credential key unavailable")
	}
	raw, e := vault.Open(scope.UserID, scope.ConnectionID, state.Session)
	if e != nil {
		return errors.New("session decryption failed")
	}
	defer clear(raw)
	result, e := chaoxing.ProbeCourseDiscussion(ctx, contract.Dependencies{HTTPClient: &http.Client{Timeout: 25 * time.Second}}, contract.Scope{UserID: scope.UserID, ConnectionID: scope.ConnectionID}, raw, "工科数学分析")
	if e != nil {
		return e
	}
	b, e := json.MarshalIndent(result, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll("reports", 0700); e != nil {
		return e
	}
	if e = os.WriteFile("reports/chaoxing-math-discussion.json", append(b, '\n'), 0600); e != nil {
		return e
	}
	fmt.Printf("course=%s status=%s topics=%d bbs_available=%t bytes=%d\n", result.CourseName, result.Status, len(result.Topics), result.BBSAvailable, result.PageBytes)
	return nil
}
