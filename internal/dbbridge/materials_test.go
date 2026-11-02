package dbbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/chaoxing"
	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/model"
	"academic/backend/internal/platform"
	"academic/backend/internal/trigger"
)

type materialTransport struct{ target *url.URL }

func (m materialTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	u := *r.URL
	u.Scheme, u.Host = m.target.Scheme, m.target.Host
	clone.URL = &u
	return http.DefaultTransport.RoundTrip(clone)
}

func TestPostgresMaterialSyncAndPreview(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	owner := fixtureUser(t, db, "materials_owner", model.RoleUser)
	other := fixtureUser(t, db, "materials_other", model.RoleUser)
	repo := db.Platforms()
	connection, err := repo.Create(ctx, owner.ID, "chaoxing")
	must(t, err)
	scope := model.DWSScope{UserID: owner.ID, ConnectionID: connection.ID}
	vault, err := credential.New([]byte(strings.Repeat("k", 32)))
	must(t, err)
	state, err := json.Marshal(map[string]any{"Version": 1, "Scope": contract.Scope{UserID: owner.ID, ConnectionID: connection.ID}, "Authorized": true})
	must(t, err)
	sealed, err := vault.Seal(owner.ID, connection.ID, state)
	must(t, err)
	lease, err := repo.Acquire(ctx, scope)
	must(t, err)
	must(t, repo.SaveAuth(ctx, lease, sealed, "active", time.Now().Add(time.Hour)))
	must(t, repo.Release(ctx, lease))
	broken, renamed := false, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mycourse/backclazzdata":
			fmt.Fprint(w, `{"result":1,"channelList":[{"cpi":"private-cpi","content":{"id":"class","course":{"data":[{"id":"course","name":"脱敏课程"}]}}}]}`)
		case "/mooc2-ans/coursedata/stu-datalist":
			if broken {
				fmt.Fprint(w, "unknown page")
				return
			}
			fmt.Fprint(w, `<input id=pages value=1><input id=totalPages value=1><div class=dataBody>`)
			if r.URL.Query().Get("dataId") == "0" {
				name := "旧讲义.pdf"
				if renamed {
					name = "新讲义.pdf"
				}
				fmt.Fprintf(w, `<ul class=dataBody_td id=folder dataName=课件 type=afolder enc=private-ticket></ul><ul class=dataBody_td id=file dataName=%s type=pdf></ul>`, name)
			} else {
				fmt.Fprint(w, `<ul class=dataBody_td id=child dataName=课程.pptx type=pptx></ul>`)
			}
			fmt.Fprint(w, `</div>`)
		default:
			t.Errorf("unexpected upstream request: %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	adapter, err := chaoxing.New(contract.Dependencies{HTTPClient: &http.Client{Transport: materialTransport{target}}})
	must(t, err)
	service, err := platform.New(repo, vault, []contract.Adapter{adapter})
	must(t, err)
	var changes []trigger.Change
	db.SetTriggerHandler(trigger.HandlerFunc(func(_ context.Context, c trigger.Change) error { changes = append(changes, c); return nil }))
	must(t, service.Sync(ctx, scope, contract.Material))
	load := func() model.PlatformState {
		t.Helper()
		l, e := repo.Acquire(ctx, scope)
		must(t, e)
		s, e := repo.Load(ctx, l, "material")
		must(t, e)
		must(t, repo.Release(ctx, l))
		return s
	}
	checkpoint := load()
	if len(checkpoint.Cursor) == 0 || strings.Contains(string(checkpoint.Cursor), "private-ticket") {
		t.Fatal("missing or unencrypted folder checkpoint")
	}
	broken = true
	if err = service.Sync(ctx, scope, contract.Material); !errors.Is(err, contract.ErrParse) {
		t.Fatal("malformed continuation accepted", err)
	}
	if string(load().Cursor) != string(checkpoint.Cursor) {
		t.Fatal("failed page advanced checkpoint")
	}
	broken = false
	must(t, service.Sync(ctx, scope, contract.Material))
	rows, err := db.Preview().Records(ctx, owner.ID, "chaoxing", connection.ID, "material", 0, 20)
	must(t, err)
	if len(rows) != 3 || len(changes) != 3 {
		t.Fatal("directory records or business triggers missing")
	}
	fileID := rows[1].ID
	for _, row := range rows {
		if strings.Contains(string(row.Content), "private-") {
			t.Fatal("navigation credentials persisted in business records")
		}
	}
	// Rescan is idempotent; a renamed file updates its existing central ID.
	must(t, service.Sync(ctx, scope, contract.Material))
	must(t, service.Sync(ctx, scope, contract.Material))
	if len(changes) != 3 {
		t.Fatal("unchanged rescan emitted changes")
	}
	renamed = true
	must(t, service.Sync(ctx, scope, contract.Material))
	rows, err = db.Preview().Records(ctx, owner.ID, "chaoxing", connection.ID, "material", 0, 20)
	must(t, err)
	if len(rows) != 3 || rows[1].ID != fileID || !strings.Contains(string(rows[1].Content), "新讲义.pdf") || len(changes) != 4 || changes[3].Operation != trigger.Updated {
		t.Fatal("renamed material was not updated in place")
	}
	courseRepo := db.Preview().(model.CoursePreviewRepository)
	grouped, err := courseRepo.CourseRecords(ctx, owner.ID, "chaoxing", connection.ID, "material", coursePreviewKey("course", "class"), 0, 20)
	must(t, err)
	if len(grouped) != 3 {
		t.Fatal("course-filtered materials missing")
	}
	overview, err := db.Preview().Overview(ctx, owner.ID)
	must(t, err)
	if len(overview) != 1 || len(overview[0].Resources) != 1 || overview[0].Resources[0].Kind != "material" || overview[0].Resources[0].LastSuccessAt == nil {
		t.Fatal("material sync status missing")
	}
	if _, err = db.Preview().Records(ctx, other.ID, "chaoxing", connection.ID, "material", 0, 20); !errors.Is(err, model.ErrNotFound) {
		t.Fatal("material archive crossed owner scope", err)
	}
}
