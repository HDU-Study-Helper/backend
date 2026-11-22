// Package dws wraps one pinned official CLI with isolated per-connection workspaces.
package dws

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"academic/backend/internal/credential"
	"academic/backend/internal/model"
)

var errWorkspaceCommitted = errors.New("dws: workspace committed atomically")

type Config struct {
	BinaryPath      string
	BinarySHA256    string
	WorkspaceRoot   string
	PollInterval    time.Duration
	CommandTimeout  time.Duration
	Overlap         time.Duration
	InitialLookback time.Duration
}
type executor interface {
	execute(context.Context, string, []string, string) ([]byte, error)
}
type task struct {
	scope      model.DWSScope
	group, job string
}
type activeOperation struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type Service struct {
	workerMu    sync.Mutex
	repo        model.DWSRepository
	vault       *credential.Vault
	cfg         Config
	runner      executor
	queue       chan task
	activeMu    sync.Mutex
	active      map[model.DWSScope]*activeOperation
	linksMu     sync.Mutex
	links       map[string]*linkSession
	linksCtx    context.Context
	linksCancel context.CancelFunc
	linksWG     sync.WaitGroup
	authRunner  authExecutor
}

func New(repo model.DWSRepository, vault *credential.Vault, cfg Config) (*Service, error) {
	if repo == nil || vault == nil || !filepath.IsAbs(cfg.BinaryPath) || len(cfg.BinarySHA256) != 64 {
		return nil, model.ErrInvalidArgument
	}
	f, e := os.Open(cfg.BinaryPath)
	if e != nil {
		return nil, fmt.Errorf("dws: binary unavailable")
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return nil, fmt.Errorf("dws: invalid binary")
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil || hex.EncodeToString(h.Sum(nil)) != cfg.BinarySHA256 {
		return nil, fmt.Errorf("dws: binary checksum mismatch")
	}
	if cfg.WorkspaceRoot == "" {
		cfg.WorkspaceRoot = "/dev/shm"
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 10 * time.Minute
	}
	if cfg.CommandTimeout == 0 {
		cfg.CommandTimeout = 30 * time.Second
	}
	if cfg.Overlap == 0 {
		cfg.Overlap = 5 * time.Minute
	}
	if cfg.InitialLookback == 0 {
		cfg.InitialLookback = 30 * 24 * time.Hour
	}
	if cfg.PollInterval < time.Second || cfg.CommandTimeout < time.Second || cfg.CommandTimeout > time.Minute || cfg.Overlap < 0 || cfg.InitialLookback <= 0 || cfg.InitialLookback > 30*24*time.Hour {
		return nil, model.ErrInvalidArgument
	}
	root, e := newWorkspace(cfg.WorkspaceRoot)
	if e != nil {
		return nil, e
	}
	os.RemoveAll(root)
	return &Service{repo: repo, vault: vault, cfg: cfg, runner: &runner{binary: cfg.BinaryPath, timeout: cfg.CommandTimeout}, queue: make(chan task, 128)}, nil
}
func validProfile(profile, corp, user string) bool {
	return corp != "" && user != "" && !strings.ContainsAny(corp+user, ":, \t\n\r\x00") && profile == corp+":"+user && len(profile) <= 512
}
func newID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
func (s *Service) CreateConnection(ctx context.Context, owner string, c model.DWSConnection) (model.DWSConnection, error) {
	if c.Mode == "" {
		c.Mode = model.DWSModePersonal
	}
	if c.Mode != model.DWSModePersonal {
		return c, model.ErrUnsupported
	}
	if owner == "" || !validProfile(c.Profile, c.CorpID, c.UpstreamUserID) {
		return c, model.ErrInvalidArgument
	}
	c.ID = newID()
	if c.ID == "" {
		return c, model.ErrInternal
	}
	c.UserID = owner
	c.Enabled = true
	c.State = "pending"
	c.WorkspaceCiphertext = nil
	return s.repo.CreateConnection(ctx, c)
}

// Disconnect fences persistence first, then cancels this process's active CLI and waits
// for process exit, workspace removal and lease cleanup for at most 12 seconds.
func (s *Service) Disconnect(ctx context.Context, scope model.DWSScope) error {
	if err := s.repo.Disconnect(ctx, scope); err != nil {
		return err
	}
	s.activeMu.Lock()
	op := s.active[scope]
	if op != nil {
		op.cancel()
	}
	s.activeMu.Unlock()
	if op == nil {
		return nil
	}
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	select {
	case <-op.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("dws: disconnect cleanup timeout")
	}
}

// withWorkspace holds a connection-wide fencing lease until every CLI process has exited.
func (s *Service) withWorkspace(ctx context.Context, scope model.DWSScope, importRoot string, fn func(context.Context, model.DWSConnection, model.Lease, string) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	lease, e := s.repo.AcquireLease(ctx, scope, "", 6*time.Minute)
	if e != nil {
		return e
	}
	op := &activeOperation{cancel: cancel, done: make(chan struct{})}
	s.activeMu.Lock()
	if s.active == nil {
		s.active = make(map[model.DWSScope]*activeOperation)
	}
	// A valid DB lease is exclusive. Never replace another registered operation.
	if s.active[scope] != nil {
		s.activeMu.Unlock()
		cleanup, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = s.repo.ReleaseLease(cleanup, lease)
		return model.ErrConflict
	}
	s.active[scope] = op
	s.activeMu.Unlock()
	defer func() { s.activeMu.Lock(); delete(s.active, scope); close(op.done); s.activeMu.Unlock() }()
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = s.repo.ReleaseLease(cleanup, lease)
	}()
	conn, e := s.repo.GetConnection(ctx, scope)
	if e != nil {
		return e
	}
	if !conn.Enabled {
		return model.ErrConflict
	}
	if !validProfile(conn.Profile, conn.CorpID, conn.UpstreamUserID) {
		return model.ErrInvalidArgument
	}
	root, e := newWorkspace(s.cfg.WorkspaceRoot)
	if e != nil {
		return e
	}
	defer os.RemoveAll(root)
	if importRoot != "" {
		data, e := credential.Pack(importRoot)
		if e != nil {
			return e
		}
		defer clear(data)
		if e = credential.Unpack(data, root); e != nil {
			return e
		}
	} else {
		data, e := s.repo.LoadWorkspace(ctx, scope)
		if e != nil {
			return e
		}
		if e = s.vault.OpenDirectory(scope.UserID, scope.ConnectionID, data, root); e != nil {
			return e
		}
	}
	operationErr := fn(ctx, conn, lease, root)
	if errors.Is(operationErr, errWorkspaceCommitted) {
		return nil
	}
	sealed, sealErr := s.vault.SealDirectory(scope.UserID, scope.ConnectionID, root)
	if sealErr == nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sealErr = s.repo.SaveWorkspace(cleanup, lease, sealed)
	}
	if operationErr != nil {
		return operationErr
	}
	return sealErr
}

type AuthStatus struct {
	Success           bool   `json:"success"`
	Authenticated     bool   `json:"authenticated"`
	TokenValid        bool   `json:"token_valid"`
	RefreshTokenValid bool   `json:"refresh_token_valid"`
	CorpID            string `json:"corp_id"`
	UserID            string `json:"user_id"`
}

func (s *Service) status(ctx context.Context, c model.DWSConnection, root string) (AuthStatus, error) {
	var status AuthStatus
	b, e := s.runner.execute(ctx, root, []string{"auth", "status", "--profile", c.Profile, "--readonly", "-f", "json"}, "")
	if e != nil {
		return status, e
	}
	if json.Unmarshal(b, &status) != nil {
		return status, fmt.Errorf("parse_failed")
	}
	if !status.Success || !status.Authenticated || (!status.TokenValid && !status.RefreshTokenValid) || status.CorpID != c.CorpID || status.UserID != c.UpstreamUserID {
		return status, fmt.Errorf("auth_required")
	}
	return status, nil
}

// ImportWorkspace is an operator-only offline authorization entry point. Never expose root over HTTP.
func (s *Service) ImportWorkspace(ctx context.Context, owner, id, root string) error {
	scope := model.DWSScope{UserID: owner, ConnectionID: id}
	probe, err := newWorkspace(root)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(probe)
	e := s.withWorkspace(ctx, scope, root, func(ctx context.Context, c model.DWSConnection, l model.Lease, r string) error {
		status, e := s.status(ctx, c, r)
		if e == nil && !status.TokenValid {
			return fmt.Errorf("auth_required")
		}
		return e
	})
	if e != nil {
		return e
	}
	active := "active"
	_, e = s.repo.UpdateConnection(ctx, scope, model.DWSConnectionUpdate{State: &active})
	return e
}
func (s *Service) CheckSession(ctx context.Context, scope model.DWSScope) (AuthStatus, error) {
	var status AuthStatus
	e := s.withWorkspace(ctx, scope, "", func(ctx context.Context, c model.DWSConnection, l model.Lease, root string) error {
		var e error
		status, e = s.status(ctx, c, root)
		return e
	})
	return status, e
}
func (s *Service) Sync(ctx context.Context, scope model.DWSScope, group, job string) error {
	return s.syncWindow(ctx, scope, group, job, 0)
}

// SyncHistory is an operator entry point for bounded history repair. It preserves
// the allowlist, lease fencing, deduplication and atomic watermark commit of Sync.
// It does not delete or rewind a checkpoint. Subsequent Sync calls stay incremental.
func (s *Service) SyncHistory(ctx context.Context, scope model.DWSScope, group string, days int) error {
	if days < 1 || days > 30 {
		return model.ErrInvalidArgument
	}
	return s.syncWindow(ctx, scope, group, "", time.Duration(days)*24*time.Hour)
}

func (s *Service) syncWindow(ctx context.Context, scope model.DWSScope, group, job string, history time.Duration) error {
	e := s.withWorkspace(ctx, scope, "", func(ctx context.Context, c model.DWSConnection, l model.Lease, root string) (resultErr error) {
		defer func() {
			if resultErr != nil && !errors.Is(resultErr, errWorkspaceCommitted) && job != "" {
				failCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = s.repo.FailLeasedJob(failCtx, l, job, safeCode(resultErr))
			}
		}()
		if c.State != "active" {
			return model.ErrConflict
		}
		if job != "" {
			if e := s.repo.StartJob(ctx, l, job); e != nil {
				return e
			}
		}
		groups, e := s.repo.ListGroups(ctx, scope)
		if e != nil {
			return e
		}
		allowed := false
		for _, g := range groups {
			if g.ID == group {
				allowed = true
			}
		}
		if !allowed {
			return model.ErrNotFound
		}
		cp, e := s.repo.GetCheckpoint(ctx, scope, group)
		if e != nil && !errors.Is(e, model.ErrNotFound) {
			return e
		}
		end := time.Now().UTC()
		start := end.Add(-s.cfg.InitialLookback)
		if history > 0 {
			start = end.Add(-history)
		} else if !cp.PollWatermark.IsZero() {
			start = cp.PollWatermark.Add(-s.cfg.Overlap)
		}
		token := ""
		seen := map[string]bool{}
		messages := []model.Message{}
		hashes := map[string]bool{}
		totalBytes := 0
		for page := 0; page < 100; page++ {
			args := []string{"chat", "+chat-messages", "--group", group, "--start", start.Format(time.RFC3339Nano), "--end", end.Format(time.RFC3339Nano), "--order", "asc", "--limit", "100", "--profile", c.Profile, "-f", "json"}
			if token != "" {
				args = append(args, "--page-token", token)
			}
			data, e := s.runner.execute(ctx, root, args, "")
			if e != nil {
				return e
			}
			p, items, e := parsePage(data, group, start, end)
			if e != nil {
				return e
			}
			for _, m := range items {
				k := m.ExternalID + "/" + m.ContentHash + "/" + m.SourceVersion
				if !hashes[k] {
					hashes[k] = true
					messages = append(messages, m)
					totalBytes += len(m.Content)
				}
			}
			if len(messages) > 10000 || totalBytes > 16<<20 {
				return fmt.Errorf("budget_exceeded")
			}
			if !*p.HasMore {
				sealed, e := s.vault.SealDirectory(scope.UserID, scope.ConnectionID, root)
				if e != nil {
					return e
				}
				if err := s.repo.CommitSync(ctx, l, model.SyncCommit{Messages: messages, Checkpoint: model.Checkpoint{GroupID: group, PollWatermark: end}, WorkspaceCiphertext: sealed, JobID: job}); err != nil {
					return err
				}
				return errWorkspaceCommitted
			}
			if p.NextPageToken == "" || seen[p.NextPageToken] {
				return fmt.Errorf("parse_failed")
			}
			seen[p.NextPageToken] = true
			token = p.NextPageToken
		}
		return fmt.Errorf("budget_exceeded")
	})
	return e
}
func safeCode(e error) string {
	for _, x := range []struct {
		e error
		s string
	}{{model.ErrNotFound, "not_found"}, {model.ErrConflict, "conflict"}, {model.ErrLeaseLost, "conflict"}, {model.ErrUnsupported, "unsupported"}, {model.ErrInvalidArgument, "invalid_argument"}} {
		if errors.Is(e, x.e) {
			return x.s
		}
	}
	if e != nil {
		switch e.Error() {
		case "parse_failed", "auth_required", "budget_exceeded", "dws_org_denied", "dws_channel_denied", "dws_org_check_failed", "dws_token_exchange_failed", "dws_cli_save_failed", "dws_cli_after_challenge", "dws_cli_output_invalid", "dws_receipt_invalid", "dws_identity_unverified", "dws_status_failed", "dws_workspace_save_failed", "dws_connection_save_failed":
			return e.Error()
		}
	}
	return "upstream_unavailable"
}
func (s *Service) Run(ctx context.Context) error {
	if err := s.startLinks(ctx); err != nil {
		return err
	}
	defer func() { _ = s.CloseLinks(context.Background()) }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-s.queue:
			s.workerMu.Lock()
			_ = s.Sync(ctx, t.scope, t.group, t.job)
			s.workerMu.Unlock()
		}
	}
}

// Poll performs one scan, serially with queued manual DWS jobs.
func (s *Service) Poll(ctx context.Context) bool {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	s.pruneLinks()
	failed := false
	after := ""
	for {
		connections, err := s.repo.ListPollTargets(ctx, 100, after)
		if err != nil {
			model.RefreshFailure(ctx, "dws", "target_list_failed")
			return true
		}
		for _, c := range connections {
			if ctx.Err() != nil {
				return true
			}
			model.RefreshConnection(ctx, "dws")
			scope := model.DWSScope{UserID: c.UserID, ConnectionID: c.ID}
			pending, err := s.repo.ListPendingJobs(ctx, scope, 100)
			if err != nil {
				failed = true
				model.RefreshFailure(ctx, "dws", "pending_jobs_failed")
			} else {
				for _, j := range pending {
					err := s.Sync(ctx, scope, j.GroupID, j.ID)
					code := ""
					if err != nil {
						failed = true
						code = safeCode(err)
					}
					model.RefreshAttempt(ctx, "dws", code)
				}
			}
			groups, err := s.repo.ListGroups(ctx, scope)
			if err != nil {
				failed = true
				model.RefreshFailure(ctx, "dws", "group_list_failed")
				continue
			}
			for _, g := range groups {
				if ctx.Err() != nil {
					return true
				}
				job, err := s.repo.CreateJob(ctx, scope, g.ID)
				if err != nil {
					failed = true
					model.RefreshFailure(ctx, "dws", "job_create_failed")
					continue
				}
				err = s.Sync(ctx, scope, g.ID, job.ID)
				code := ""
				if err != nil {
					failed = true
					code = safeCode(err)
				}
				model.RefreshAttempt(ctx, "dws", code)
			}
		}
		if len(connections) < 100 {
			return failed
		}
		after = connections[len(connections)-1].ID
	}
}
