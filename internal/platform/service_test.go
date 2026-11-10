package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/model"
)

type testRepo struct {
	model.PlatformRepository
	state   model.PlatformState
	commits []model.PlatformRecord
	next    []byte
	failed  string
	reject  bool
}

func (r *testRepo) Get(_ context.Context, s model.DWSScope) (model.PlatformConnection, error) {
	if s.UserID != r.state.Connection.UserID || s.ConnectionID != r.state.Connection.ID {
		return model.PlatformConnection{}, model.ErrNotFound
	}
	return r.state.Connection, nil
}
func (r *testRepo) Acquire(_ context.Context, s model.DWSScope) (model.PlatformLease, error) {
	if s.UserID != r.state.Connection.UserID || s.ConnectionID != r.state.Connection.ID {
		return model.PlatformLease{}, model.ErrNotFound
	}
	return model.PlatformLease{Scope: s, Token: 1}, nil
}
func (r *testRepo) Release(context.Context, model.PlatformLease) error { return nil }
func (r *testRepo) Load(context.Context, model.PlatformLease, string) (model.PlatformState, error) {
	return r.state, nil
}
func (r *testRepo) Commit(_ context.Context, _ model.PlatformLease, _ string, records []model.PlatformRecord, session, cursor []byte) error {
	if r.reject {
		return model.ErrLeaseLost
	}
	r.commits = records
	r.next = cursor
	r.state.Session = session
	r.state.Cursor = cursor
	return nil
}
func (r *testRepo) Fail(_ context.Context, _ model.PlatformLease, code string) error {
	r.failed = code
	return nil
}

type testAdapter struct {
	contract.Adapter
	page   contract.Page
	err    error
	cursor string
}

func (a *testAdapter) Describe() contract.Descriptor {
	return contract.Descriptor{Source: "chaoxing", QRLogin: true, Resources: []contract.Resource{contract.Course}}
}
func (a *testAdapter) Sync(_ context.Context, _ contract.Scope, session contract.Session, _ contract.Resource, cursor string) (contract.Page, error) {
	a.cursor = cursor
	return a.page, a.err
}

type timestampAdapter struct {
	*testAdapter
	source string
}

func (a *timestampAdapter) Describe() contract.Descriptor {
	d := a.testAdapter.Describe()
	d.Source = a.source
	return d
}

func TestSyncObservationTimestampDoesNotChangeRecord(t *testing.T) {
	for _, source := range []string{"chaoxing", "mooc", "ucampus"} {
		t.Run(source, func(t *testing.T) {
			vault, err := credential.New([]byte(strings.Repeat("k", 32)))
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := vault.Seal("owner", "connection", []byte("fixture-session"))
			if err != nil {
				t.Fatal(err)
			}
			repo := &testRepo{state: model.PlatformState{Connection: model.PlatformConnection{UserID: "owner", ID: "connection", Source: source, State: "active"}, Session: sealed}}
			at := time.Date(2026, 11, 6, 1, 0, 0, 0, time.UTC)
			adapter := &timestampAdapter{source: source, testAdapter: &testAdapter{page: contract.Page{Records: []contract.Record{{Kind: contract.Course, ExternalID: "course", ParentID: "term", Title: "课程", Fields: map[string]any{"start_time": "2026-09-01"}, ObservedAt: at}}}}}
			service, err := New(repo, vault, []contract.Adapter{adapter})
			if err != nil {
				t.Fatal(err)
			}
			scope := model.DWSScope{UserID: "owner", ConnectionID: "connection"}
			if err = service.Sync(context.Background(), scope, contract.Course); err != nil {
				t.Fatal(err)
			}
			before := repo.commits[0]
			adapter.page.Records[0].ObservedAt = at.Add(time.Hour + time.Nanosecond)
			if err = service.Sync(context.Background(), scope, contract.Course); err != nil {
				t.Fatal(err)
			}
			after := repo.commits[0]
			if before.Hash != after.Hash || string(before.Content) != string(after.Content) || before.ExternalID != after.ExternalID || before.ParentID != after.ParentID {
				t.Fatal("collection timestamp changed business content or identity")
			}
			if !after.ObservedAt.Equal(adapter.page.Records[0].ObservedAt) {
				t.Fatal("collection timestamp was lost")
			}
			// Actual source time corrections must still be detected as updates.
			adapter.page.Records[0].Fields["start_time"] = "2026-09-02"
			if err = service.Sync(context.Background(), scope, contract.Course); err != nil {
				t.Fatal(err)
			}
			if repo.commits[0].Hash == after.Hash || repo.commits[0].ExternalID != after.ExternalID || repo.commits[0].ParentID != after.ParentID {
				t.Fatal("source time correction was ignored or changed identity")
			}
		})
	}
}

func TestSyncEncryptionAndCheckpoint(t *testing.T) {
	vault, e := credential.New([]byte(strings.Repeat("k", 32)))
	if e != nil {
		t.Fatal(e)
	}
	encrypted, e := vault.Seal("owner", "connection", []byte("private-cookie"))
	if e != nil {
		t.Fatal(e)
	}
	repo := &testRepo{state: model.PlatformState{Connection: model.PlatformConnection{UserID: "owner", ID: "connection", Source: "chaoxing", State: "active"}, Session: encrypted}}
	adapter := &testAdapter{page: contract.Page{Records: []contract.Record{{Kind: contract.Course, ExternalID: "course1", Title: "课程", ObservedAt: time.Now()}}, NextCursor: "secret-cursor", HasMore: true, Session: []byte("rotated-cookie")}}
	service, e := New(repo, vault, []contract.Adapter{adapter})
	if e != nil {
		t.Fatal(e)
	}
	scope := model.DWSScope{UserID: "owner", ConnectionID: "connection"}
	if e = service.Sync(context.Background(), scope, contract.Course); e != nil {
		t.Fatal(e)
	}
	if len(repo.commits) != 1 || strings.Contains(string(repo.next), "secret-cursor") {
		t.Fatal("cursor exposed or record missing")
	}
	plain, e := vault.Open(scope.UserID, scope.ConnectionID, repo.state.Session)
	if e != nil || string(plain) != "rotated-cookie" {
		t.Fatal("session rotation not encrypted")
	}
	h := sha256.Sum256(repo.commits[0].Content)
	if hex.EncodeToString(h[:]) != repo.commits[0].Hash {
		t.Fatal("hash mismatch")
	}
	if strings.Contains(string(repo.commits[0].Content), "private-cookie") {
		t.Fatal("credentials in record")
	}
	old := string(repo.next)
	adapter.err = contract.ErrParse
	if e = service.Sync(context.Background(), scope, contract.Course); !errors.Is(e, contract.ErrParse) || string(repo.next) != old {
		t.Fatal("failed page advanced cursor")
	}
	adapter.err = nil
	adapter.page.NextCursor = "secret-cursor"
	if e = service.Sync(context.Background(), scope, contract.Course); !errors.Is(e, contract.ErrParse) {
		t.Fatal("cursor loop accepted")
	}
	if e = service.Sync(context.Background(), model.DWSScope{UserID: "other", ConnectionID: "connection"}, contract.Course); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("cross owner access")
	}
}
func TestAuthDTONeverContainsSession(t *testing.T) {
	pending := authDTO(contract.AuthResult{Status: "pending", PollAfter: 3 * time.Second})
	if pending["poll_after_ms"] != int64(3000) {
		t.Fatal("adapter polling interval ignored")
	}
	r := authDTO(contract.AuthResult{Status: "authorized", Session: []byte("private-cookie"), QRURL: "https://sensitive", QRImage: []byte("private")})
	if len(r) != 3 {
		t.Fatal("authorized challenge exposes QR or cookie", r)
	}
	for _, key := range []string{"session", "qr_url", "qr_image"} {
		if _, ok := r[key]; ok {
			t.Fatal("sensitive field", key)
		}
	}
}
