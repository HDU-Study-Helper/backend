package dws

import (
	"academic/backend/internal/credential"
	"academic/backend/internal/dbbridge"
	"academic/backend/internal/model"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestDiagnoseLiveWindow(t *testing.T) {
	if os.Getenv("DWS_DIAGNOSE_LIVE") != "1" {
		t.Skip("explicit live diagnostic only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db, e := dbbridge.Open(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		t.Fatal("db unavailable")
	}
	defer db.Close()
	user, e := db.Users().GetByUsername(ctx, os.Getenv("BOOTSTRAP_ADMIN_USERNAME"))
	if e != nil {
		t.Fatal("owner unavailable")
	}
	key, e := base64.StdEncoding.DecodeString(os.Getenv("CREDENTIAL_KEY"))
	if e != nil {
		t.Fatal("key unavailable")
	}
	vault, e := credential.New(key)
	if e != nil {
		t.Fatal("vault unavailable")
	}
	s, e := New(db.DWS(), vault, Config{BinaryPath: os.Getenv("DWS_BINARY"), BinarySHA256: os.Getenv("DWS_BINARY_SHA256"), WorkspaceRoot: "/dev/shm", CommandTimeout: time.Minute})
	if e != nil {
		t.Fatal("service unavailable")
	}
	cs, e := db.DWS().ListConnections(ctx, user.ID)
	if e != nil {
		t.Fatal("connections unavailable")
	}
	for _, c := range cs {
		if !c.Enabled || c.State != "active" {
			continue
		}
		scope := model.DWSScope{UserID: user.ID, ConnectionID: c.ID}
		groups, e := db.DWS().ListGroups(ctx, scope)
		if e != nil {
			t.Fatal("groups unavailable")
		}
		for _, g := range groups {
			if os.Getenv("DWS_REPAIR_HISTORY") == "1" {
				if e := s.SyncHistory(ctx, scope, g.ID, 30); e != nil {
					t.Fatal("history repair failed: " + safeCode(e))
				}
				fmt.Println("history_repair_committed=true")
				continue
			}
			cp, e := db.DWS().GetCheckpoint(ctx, scope, g.ID)
			fmt.Printf("checkpoint_present=%v watermark=%s\n", e == nil, cp.PollWatermark.Format(time.RFC3339))
			e = s.withWorkspace(ctx, scope, "", func(ctx context.Context, c model.DWSConnection, _ model.Lease, root string) error {
				for _, days := range []int{1, 30} {
					end := time.Now().UTC()
					start := end.Add(-time.Duration(days) * 24 * time.Hour)
					data, err := s.runner.execute(ctx, root, []string{"chat", "+chat-messages", "--group", g.ID, "--start", start.Format(time.RFC3339Nano), "--end", end.Format(time.RFC3339Nano), "--order", "asc", "--limit", "100", "--profile", c.Profile, "-f", "json"}, "")
					if err != nil {
						fmt.Printf("days=%d error=%s\n", days, safeCode(err))
						continue
					}
					var p messagePage
					_ = json.Unmarshal(data, &p)
					_, items, err := parsePage(data, g.ID, start, end)
					count := -1
					if p.Count != nil {
						count = *p.Count
					}
					fmt.Printf("days=%d upstream_count=%d parsed_count=%d complete=%v more=%v parse_ok=%v\n", days, count, len(items), p.Complete != nil && *p.Complete, p.HasMore != nil && *p.HasMore, err == nil)
				}
				return nil
			})
			if e != nil {
				t.Fatal("diagnostic failed: " + safeCode(e))
			}
		}
	}
}
