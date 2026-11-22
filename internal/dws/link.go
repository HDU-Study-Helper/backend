package dws

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"time"

	"academic/backend/internal/model"
)

const linkLifetime = 4 * time.Minute

// LinkChallenge contains only the current owner's short-lived authorization challenge.
type LinkChallenge struct {
	ID              string    `json:"id"`
	ConnectionID    string    `json:"connection_id"`
	Status          string    `json:"status"`
	VerificationURI string    `json:"verification_uri,omitempty"`
	UserCode        string    `json:"user_code,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
	ErrorCode       string    `json:"error_code,omitempty"`
}
type linkSession struct {
	scope     model.DWSScope
	challenge LinkChallenge
	cancel    context.CancelFunc
	done      chan struct{}
}
type deviceChallenge struct {
	uri, code string
	lifetime  time.Duration
}
type authExecutor interface {
	login(context.Context, string, func(deviceChallenge) error) ([]byte, error)
}

func linkActive(status string) bool { return status == "starting" || status == "pending" }
func (s *Service) startLinks(ctx context.Context) error {
	s.linksMu.Lock()
	defer s.linksMu.Unlock()
	if s.linksCtx != nil {
		return model.ErrConflict
	}
	s.linksCtx, s.linksCancel = context.WithCancel(ctx)
	s.links = make(map[string]*linkSession)
	return nil
}

// CloseLinks stops accepting links, cancels all managed authorization processes and joins them.
// A deadline only limits this caller's wait; Run always joins without a deadline.
func (s *Service) CloseLinks(ctx context.Context) error {
	s.linksMu.Lock()
	if s.linksCancel != nil {
		s.linksCancel()
	}
	for _, l := range s.links {
		if linkActive(l.challenge.Status) {
			l.cancel()
		}
	}
	s.linksMu.Unlock()
	s.linksMu.Lock()
	sessions := make([]*linkSession, 0, len(s.links))
	for _, l := range s.links {
		sessions = append(sessions, l)
	}
	s.linksMu.Unlock()
	for _, l := range sessions {
		select {
		case <-l.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.linksWG.Wait()
	s.linksMu.Lock()
	s.linksCtx = nil
	s.linksCancel = nil
	clear(s.links)
	s.linksMu.Unlock()
	return nil
}

// Link starts the official CLI only after Run has bound its parent lifecycle.
// The request context is used for admission only, never as the authorization lifetime.
func (s *Service) Link(ctx context.Context, scope model.DWSScope) (LinkChallenge, error) {
	c, err := s.repo.GetConnection(ctx, scope)
	if err != nil {
		return LinkChallenge{}, err
	}
	if !c.Enabled || (c.State != "pending" && c.State != "active" && c.State != "expired" && c.State != "error") {
		return LinkChallenge{}, model.ErrConflict
	}
	if !linkableIdentity(c) {
		return LinkChallenge{}, model.ErrInvalidArgument
	}
	s.linksMu.Lock()
	defer s.linksMu.Unlock()
	if err := s.admitLinkLocked(scope.UserID); err != nil {
		return LinkChallenge{}, err
	}
	return s.beginLinkLocked(scope)
}

func linkableIdentity(c model.DWSConnection) bool {
	return c.Mode != model.DWSModeSchool && (validProfile(c.Profile, c.CorpID, c.UpstreamUserID) || (c.State == "pending" && c.Profile == "" && c.CorpID == "" && c.UpstreamUserID == ""))
}

// LinkNew requires no upstream identity; only the final official login may bind it.
func (s *Service) LinkNew(ctx context.Context, owner string) (LinkChallenge, error) {
	if owner == "" {
		return LinkChallenge{}, model.ErrInvalidArgument
	}
	s.linksMu.Lock()
	defer s.linksMu.Unlock()
	if err := ctx.Err(); err != nil {
		return LinkChallenge{}, err
	}
	if err := s.admitLinkLocked(owner); err != nil {
		return LinkChallenge{}, err
	}
	c, err := s.repo.CreatePendingConnection(ctx, owner)
	if err != nil {
		return LinkChallenge{}, err
	}
	return s.beginLinkLocked(model.DWSScope{UserID: owner, ConnectionID: c.ID})
}

func (s *Service) admitLinkLocked(owner string) error {
	if s.linksCtx == nil || s.linksCtx.Err() != nil {
		return model.ErrConflict
	}
	now := time.Now().UTC()
	active := 0
	for id, l := range s.links {
		select {
		case <-l.done:
			if !now.Before(l.challenge.ExpiresAt) {
				delete(s.links, id)
			}
		default:
			active++
			if l.scope.UserID == owner {
				return model.ErrConflict
			}
		}
	}
	if active >= 16 || len(s.links) >= 256 {
		return model.ErrConflict
	}
	return nil
}

func (s *Service) beginLinkLocked(scope model.DWSScope) (LinkChallenge, error) {
	now := time.Now().UTC()
	id := newID()
	if id == "" {
		return LinkChallenge{}, model.ErrInternal
	}
	runCtx, cancel := context.WithDeadline(s.linksCtx, now.Add(linkLifetime))
	l := &linkSession{scope: scope, challenge: LinkChallenge{ID: id, ConnectionID: scope.ConnectionID, Status: "starting", ExpiresAt: now.Add(linkLifetime)}, cancel: cancel, done: make(chan struct{})}
	s.links[id] = l
	s.linksWG.Add(1)
	go s.runLink(runCtx, l)
	return l.challenge, nil
}
func (s *Service) LinkStatus(ctx context.Context, scope model.DWSScope, id string) (LinkChallenge, error) {
	if err := ctx.Err(); err != nil {
		return LinkChallenge{}, err
	}
	s.linksMu.Lock()
	defer s.linksMu.Unlock()
	l := s.links[id]
	if l == nil || l.scope != scope {
		return LinkChallenge{}, model.ErrNotFound
	}
	if linkActive(l.challenge.Status) && !time.Now().Before(l.challenge.ExpiresAt) {
		l.cancel()
		s.finishLinkLocked(l, "expired", "challenge_expired")
	}
	return l.challenge, nil
}
func (s *Service) pruneLinks() {
	s.linksMu.Lock()
	defer s.linksMu.Unlock()
	for id, l := range s.links {
		if !time.Now().Before(l.challenge.ExpiresAt) {
			if linkActive(l.challenge.Status) {
				l.cancel()
				s.finishLinkLocked(l, "expired", "challenge_expired")
			}
			select {
			case <-l.done:
				delete(s.links, id)
			default:
			}
		}
	}
}
func (s *Service) CancelLink(ctx context.Context, scope model.DWSScope, id string) (LinkChallenge, error) {
	s.linksMu.Lock()
	l := s.links[id]
	if l == nil || l.scope != scope {
		s.linksMu.Unlock()
		return LinkChallenge{}, model.ErrNotFound
	}
	if linkActive(l.challenge.Status) {
		l.cancel()
		s.finishLinkLocked(l, "cancelled", "")
	}
	s.linksMu.Unlock()
	select {
	case <-l.done:
		return s.LinkStatus(ctx, scope, id)
	case <-ctx.Done():
		return LinkChallenge{}, ctx.Err()
	}
}
func (s *Service) finishLinkLocked(l *linkSession, status, code string) {
	l.challenge.Status = status
	l.challenge.ErrorCode = code
	l.challenge.VerificationURI = ""
	l.challenge.UserCode = ""
}
func (s *Service) runLink(ctx context.Context, l *linkSession) {
	defer s.linksWG.Done()
	defer close(l.done)
	defer l.cancel()
	err := s.authorizeLink(ctx, l)
	s.linksMu.Lock()
	defer s.linksMu.Unlock()
	if !linkActive(l.challenge.Status) {
		return
	}
	status, code := "authorized", ""
	if err != nil {
		status, code = "failed", safeCode(err)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || !time.Now().Before(l.challenge.ExpiresAt) {
			status, code = "expired", "challenge_expired"
		} else if ctx.Err() != nil {
			status, code = "cancelled", ""
		}
	}
	if status == "failed" {
		slog.Warn("DWS link failed", "code", code)
	}
	s.finishLinkLocked(l, status, code)
}
func (s *Service) authorizeLink(ctx context.Context, l *linkSession) error {
	lease, err := s.repo.AcquireLease(ctx, l.scope, "", 5*time.Minute)
	if err != nil {
		return err
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.repo.ReleaseLease(cleanup, lease)
	}
	op := &activeOperation{cancel: l.cancel, done: l.done}
	s.activeMu.Lock()
	if s.active == nil {
		s.active = make(map[model.DWSScope]*activeOperation)
	}
	if s.active[l.scope] != nil {
		s.activeMu.Unlock()
		release()
		return model.ErrConflict
	}
	s.active[l.scope] = op
	s.activeMu.Unlock()
	defer func() { s.activeMu.Lock(); delete(s.active, l.scope); s.activeMu.Unlock() }()
	defer release()
	c, err := s.repo.GetConnection(ctx, l.scope)
	if err != nil {
		return err
	}
	if !c.Enabled || (c.State != "pending" && c.State != "active" && c.State != "expired" && c.State != "error") || !linkableIdentity(c) {
		return model.ErrConflict
	}
	root, err := newWorkspace(s.cfg.WorkspaceRoot)
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	executor := s.authRunner
	if executor == nil {
		executor = &deviceRunner{binary: s.cfg.BinaryPath}
	}
	// Callback runs synchronously on the bounded stderr writer. A timer only ever
	// shortens the enclosing lifetime and is stopped before this operation exits.
	hardDeadline, _ := ctx.Deadline()
	var expiry *time.Timer
	var generation uint64
	defer func() {
		if expiry != nil {
			expiry.Stop()
		}
	}()
	output, err := executor.login(ctx, root, func(d deviceChallenge) error {
		s.linksMu.Lock()
		defer s.linksMu.Unlock()
		if ctx.Err() != nil || !linkActive(l.challenge.Status) {
			return context.Canceled
		}
		generation++
		if expiry != nil {
			expiry.Stop()
			expiry = nil
		}
		if d.uri == "" {
			l.challenge.Status = "starting"
			l.challenge.VerificationURI = ""
			l.challenge.UserCode = ""
			l.challenge.ExpiresAt = hardDeadline
			return nil
		}
		deadline := time.Now().UTC().Add(d.lifetime)
		if deadline.After(hardDeadline) {
			deadline = hardDeadline
		}
		l.challenge.ExpiresAt = deadline
		current := generation
		expiry = time.AfterFunc(time.Until(deadline), func() {
			s.linksMu.Lock()
			defer s.linksMu.Unlock()
			if generation == current && linkActive(l.challenge.Status) {
				l.cancel()
				s.finishLinkLocked(l, "expired", "challenge_expired")
			}
		})
		l.challenge.Status = "pending"
		l.challenge.VerificationURI = d.uri
		l.challenge.UserCode = d.code
		return nil
	})
	defer clear(output)
	if err != nil {
		return err
	}
	var receipt struct {
		Success    bool   `json:"success"`
		TokenValid bool   `json:"token_valid"`
		CorpID     string `json:"corp_id"`
		UserID     string `json:"user_id"`
	}
	if json.Unmarshal(output, &receipt) != nil {
		return errors.New("dws_receipt_invalid")
	}
	identity := model.DWSIdentity{Profile: receipt.CorpID + ":" + receipt.UserID, CorpID: receipt.CorpID, UpstreamUserID: receipt.UserID}
	if !receipt.Success || !receipt.TokenValid || !validProfile(identity.Profile, identity.CorpID, identity.UpstreamUserID) || (c.Profile != "" && (identity.Profile != c.Profile || identity.CorpID != c.CorpID || identity.UpstreamUserID != c.UpstreamUserID)) {
		return errors.New("dws_identity_unverified")
	}
	// Readonly verification must select the final official identity, never a server scope ID.
	c.Profile, c.CorpID, c.UpstreamUserID = identity.Profile, identity.CorpID, identity.UpstreamUserID
	status, err := s.status(ctx, c, root)
	if err != nil || !status.TokenValid {
		return errors.New("dws_status_failed")
	}
	sealed, err := s.vault.SealDirectory(l.scope.UserID, l.scope.ConnectionID, root)
	if err != nil {
		return errors.New("dws_workspace_save_failed")
	}
	defer clear(sealed)
	// Serialize cancellation against persistence. DB fencing independently rejects
	// disconnects and revoked leases, including other server processes.
	s.linksMu.Lock()
	defer s.linksMu.Unlock()
	if ctx.Err() != nil || !linkActive(l.challenge.Status) {
		return context.Canceled
	}
	if !time.Now().Before(l.challenge.ExpiresAt) {
		return context.DeadlineExceeded
	}
	err = s.repo.CompleteLink(ctx, lease, identity, sealed)
	if err == nil {
		s.finishLinkLocked(l, "authorized", "")
	} else if safeCode(err) == "upstream_unavailable" {
		return errors.New("dws_connection_save_failed")
	}
	return err
}
