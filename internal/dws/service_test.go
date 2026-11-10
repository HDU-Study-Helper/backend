package dws

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/credential"
	"academic/backend/internal/model"
)

type fakeRepo struct {
	model.DWSRepository
	scope               model.DWSScope
	sealed              []byte
	commits             []model.SyncCommit
	fail                bool
	lease               model.Lease
	released            bool
	failedBeforeRelease bool
}

func (f *fakeRepo) AcquireLease(_ context.Context, s model.DWSScope, g string, d time.Duration) (model.Lease, error) {
	if s != f.scope {
		return model.Lease{}, model.ErrNotFound
	}
	f.lease = model.Lease{Scope: s, Token: 1, ExpiresAt: time.Now().Add(d)}
	return f.lease, nil
}
func (f *fakeRepo) ReleaseLease(context.Context, model.Lease) error     { f.released = true; return nil }
func (f *fakeRepo) StartJob(context.Context, model.Lease, string) error { return nil }
func (f *fakeRepo) FailLeasedJob(_ context.Context, l model.Lease, id, code string) error {
	f.failedBeforeRelease = !f.released && l.Token == f.lease.Token
	return nil
}
func (f *fakeRepo) GetConnection(_ context.Context, s model.DWSScope) (model.DWSConnection, error) {
	if s != f.scope {
		return model.DWSConnection{}, model.ErrNotFound
	}
	return model.DWSConnection{ID: s.ConnectionID, UserID: s.UserID, Profile: "corp:user", CorpID: "corp", UpstreamUserID: "user", Enabled: true, State: "active"}, nil
}
func (f *fakeRepo) LoadWorkspace(context.Context, model.DWSScope) ([]byte, error) {
	return f.sealed, nil
}
func (f *fakeRepo) SaveWorkspace(_ context.Context, l model.Lease, b []byte) error {
	if l.Token != f.lease.Token {
		return model.ErrLeaseLost
	}
	f.sealed = b
	return nil
}
func (f *fakeRepo) ListGroups(context.Context, model.DWSScope) ([]model.Group, error) {
	return []model.Group{{ID: "g"}}, nil
}
func (f *fakeRepo) GetCheckpoint(context.Context, model.DWSScope, string) (model.Checkpoint, error) {
	return model.Checkpoint{}, model.ErrNotFound
}
func (f *fakeRepo) CommitSync(_ context.Context, l model.Lease, c model.SyncCommit) error {
	if f.fail {
		return model.ErrLeaseLost
	}
	f.commits = append(f.commits, c)
	return nil
}

type fakeExec struct {
	calls [][]string
	pages [][]byte
	err   error
}

func (f *fakeExec) execute(ctx context.Context, root string, args []string, download string) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	f.calls = append(f.calls, args)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.pages) == 0 {
		return nil, errors.New("unexpected call")
	}
	b := f.pages[0]
	f.pages = f.pages[1:]
	return b, nil
}
func testService(t *testing.T) (*Service, *fakeRepo, *fakeExec) {
	t.Helper()
	v, e := credential.New(make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	root, e := newWorkspace("/dev/shm")
	if e != nil {
		t.Skip(e)
	}
	defer os.RemoveAll(root)
	scope := model.DWSScope{UserID: "owner-a", ConnectionID: "connection-a"}
	sealed, e := v.SealDirectory(scope.UserID, scope.ConnectionID, root)
	if e != nil {
		t.Fatal(e)
	}
	repo := &fakeRepo{scope: scope, sealed: sealed}
	exec := &fakeExec{}
	s := &Service{repo: repo, vault: v, cfg: Config{WorkspaceRoot: "/dev/shm", InitialLookback: time.Hour, Overlap: time.Minute}, runner: exec}
	return s, repo, exec
}
func fixturePage(more bool, token string) []byte {
	now := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	p := map[string]any{"contractVersion": "im.message-list.v1", "count": 1, "messages": []any{map[string]any{"messageId": "m", "conversationId": "g", "messageType": "text", "text": "测试作业", "createTime": now, "resourceRefs": []any{}}}, "complete": !more, "hasMore": more, "paginationKnown": true, "nextPageToken": token}
	b, _ := json.Marshal(p)
	return b
}
func arg(args []string, name string) string {
	for i, v := range args {
		if v == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
func TestSyncTwoPagesAndDuplicateObservation(t *testing.T) {
	s, r, x := testService(t)
	first := fixturePage(true, "page2")
	var second map[string]any
	_ = json.Unmarshal(first, &second)
	second["complete"] = true
	second["hasMore"] = false
	second["nextPageToken"] = ""
	b, _ := json.Marshal(second)
	x.pages = [][]byte{first, b}
	if e := s.Sync(context.Background(), r.scope, "g", ""); e != nil {
		t.Fatal(e)
	}
	if len(r.commits) != 1 || len(r.commits[0].Messages) != 1 || r.commits[0].Checkpoint.PollWatermark.IsZero() {
		t.Fatalf("bad commit %+v", r.commits)
	}
	if len(x.calls) != 2 || arg(x.calls[1], "--page-token") != "page2" || arg(x.calls[0], "--start") != arg(x.calls[1], "--start") || arg(x.calls[0], "--end") != arg(x.calls[1], "--end") {
		t.Fatal("window or pagination changed")
	}
}
func TestSyncFailureNeverAdvances(t *testing.T) {
	for _, mode := range []string{"decrypt", "cycle", "cancel", "fence"} {
		t.Run(mode, func(t *testing.T) {
			s, r, x := testService(t)
			ctx := context.Background()
			switch mode {
			case "decrypt":
				b := fixturePage(false, "")
				x.pages = [][]byte{[]byte(strings.Replace(string(b), `"count":1`, `"decryptFailedCount":1,"count":1`, 1))}
			case "cycle":
				x.pages = [][]byte{fixturePage(true, "same"), fixturePage(true, "same")}
			case "cancel":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			case "fence":
				r.fail = true
				x.pages = [][]byte{fixturePage(false, "")}
			}
			if e := s.Sync(ctx, r.scope, "g", ""); e == nil {
				t.Fatal("expected failure")
			}
			if len(r.commits) != 0 {
				t.Fatal("failure advanced checkpoint")
			}
		})
	}
}
func TestScopeAndAllowlist(t *testing.T) {
	s, r, x := testService(t)
	if e := s.Sync(context.Background(), model.DWSScope{UserID: "other", ConnectionID: r.scope.ConnectionID}, "g", ""); !errors.Is(e, model.ErrNotFound) {
		t.Fatal(e)
	}
	if e := s.Sync(context.Background(), r.scope, "unauthorized", ""); !errors.Is(e, model.ErrNotFound) {
		t.Fatal(e)
	}
	if len(x.calls) != 0 {
		t.Fatal("CLI called without authorization")
	}
}

type historyCheckpointRepo struct{ *fakeRepo }

func (f *historyCheckpointRepo) GetCheckpoint(context.Context, model.DWSScope, string) (model.Checkpoint, error) {
	return model.Checkpoint{GroupID: "g", PollWatermark: time.Now().Add(-time.Minute)}, nil
}

// A successful empty incremental scan must not prevent explicitly backfilling
// older messages. The repair uses a bounded window and the normal atomic commit.
func TestSyncHistoryUsesBoundedWindow(t *testing.T) {
	s, repo, exec := testService(t)
	s.repo = &historyCheckpointRepo{repo}
	exec.pages = [][]byte{fixturePage(false, "")}
	if err := s.SyncHistory(context.Background(), repo.scope, "g", 30); err != nil {
		t.Fatal(err)
	}
	args := exec.calls[0]
	start, err := time.Parse(time.RFC3339Nano, args[5])
	if err != nil {
		t.Fatal(err)
	}
	end, err := time.Parse(time.RFC3339Nano, args[7])
	if err != nil {
		t.Fatal(err)
	}
	if end.Sub(start) != 30*24*time.Hour || len(repo.commits) != 1 || len(repo.commits[0].Messages) != 1 {
		t.Fatal("history window or commit incorrect")
	}
	for _, days := range []int{0, 31} {
		if !errors.Is(s.SyncHistory(context.Background(), repo.scope, "g", days), model.ErrInvalidArgument) {
			t.Fatal("unbounded history accepted")
		}
	}
}
