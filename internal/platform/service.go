// Package platform owns authenticated platform workflows and explicitly scheduled polling.
package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/model"
	"github.com/skip2/go-qrcode"
)

type Service struct {
	repo     model.PlatformRepository
	vault    *credential.Vault
	adapters map[string]contract.Adapter
	interval time.Duration
	wake     chan struct{}
	qrMu     sync.Mutex
	qr       map[model.DWSScope]contract.AuthResult
}

func New(repo model.PlatformRepository, vault *credential.Vault, adapters []contract.Adapter) (*Service, error) {
	if repo == nil || vault == nil {
		return nil, model.ErrInvalidArgument
	}
	s := &Service{repo: repo, vault: vault, adapters: map[string]contract.Adapter{}, interval: time.Minute, wake: make(chan struct{}, 1), qr: map[model.DWSScope]contract.AuthResult{}}
	for _, a := range adapters {
		if a == nil {
			return nil, model.ErrInvalidArgument
		}
		d := a.Describe()
		if d.Source == "" || s.adapters[d.Source] != nil {
			return nil, model.ErrInvalidArgument
		}
		s.adapters[d.Source] = a
	}
	return s, nil
}
func (s *Service) Sources() []contract.Descriptor {
	out := []contract.Descriptor{}
	for _, key := range []string{"chaoxing", "mooc", "ucampus"} {
		if a := s.adapters[key]; a != nil {
			out = append(out, a.Describe())
		}
	}
	return out
}
func scopeOf(c model.PlatformConnection) model.DWSScope {
	return model.DWSScope{UserID: c.UserID, ConnectionID: c.ID}
}
func adapterScope(s model.DWSScope) contract.Scope {
	return contract.Scope{UserID: s.UserID, ConnectionID: s.ConnectionID}
}
func (s *Service) operate(ctx context.Context, scope model.DWSScope, kind string, fn func(context.Context, model.PlatformLease, model.PlatformState, contract.Adapter) error) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	lease, e := s.repo.Acquire(ctx, scope)
	if e != nil {
		return e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.repo.Release(cleanup, lease)
	}()
	state, e := s.repo.Load(ctx, lease, kind)
	if e != nil {
		return e
	}
	a := s.adapters[state.Connection.Source]
	if a == nil {
		return contract.ErrUnsupported
	}
	e = fn(ctx, lease, state, a)
	if e != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.repo.Fail(cleanup, lease, errorCode(e))
	}
	return e
}
func errorCode(e error) string {
	switch {
	case errors.Is(e, contract.ErrUnsupported):
		return "unsupported"
	case errors.Is(e, contract.ErrAuthRequired):
		return "auth_required"
	case errors.Is(e, contract.ErrParse):
		return "parse_failed"
	case errors.Is(e, contract.ErrRateLimited):
		return "rate_limited"
	case errors.Is(e, contract.ErrInvalid):
		return "invalid_argument"
	default:
		return "upstream_unavailable"
	}
}
func (s *Service) Begin(ctx context.Context, owner, source string) (model.PlatformConnection, contract.AuthResult, error) {
	a := s.adapters[source]
	if a == nil || !a.Describe().QRLogin {
		return model.PlatformConnection{}, contract.AuthResult{}, contract.ErrUnsupported
	}
	c, e := s.repo.Create(ctx, owner, source)
	if e != nil {
		return c, contract.AuthResult{}, e
	}
	scope := scopeOf(c)
	var result contract.AuthResult
	e = s.operate(ctx, scope, "", func(ctx context.Context, l model.PlatformLease, state model.PlatformState, a contract.Adapter) error {
		var e error
		result, e = a.BeginAuth(ctx, adapterScope(scope))
		if e != nil {
			return e
		}
		if e = s.saveAuth(ctx, l, result); e != nil {
			if closer, ok := a.(contract.AuthCanceller); ok {
				_ = closer.CancelAuth(ctx, adapterScope(scope))
			}
			return e
		}
		s.remember(scope, result)
		return nil
	})
	if e == nil {
		s.qrMu.Lock()
		cached, ok := s.qr[scope]
		s.qrMu.Unlock()
		if ok {
			result = cached
		}
	}
	return c, result, e
}
func (s *Service) saveAuth(ctx context.Context, l model.PlatformLease, r contract.AuthResult) error {
	state := r.Status
	if state == "authorized" {
		state = "active"
	}
	if state != "pending" && state != "scanned" && state != "active" && state != "expired" {
		return contract.ErrParse
	}
	if len(r.Session) == 0 && state != "expired" {
		return contract.ErrParse
	}
	sealed, e := s.vault.Seal(l.Scope.UserID, l.Scope.ConnectionID, r.Session)
	if e != nil {
		return e
	}
	expires := r.ExpiresAt
	if expires.IsZero() {
		expires = time.Now().Add(5 * time.Minute)
	}
	return s.repo.SaveAuth(ctx, l, sealed, state, expires)
}
func (s *Service) remember(scope model.DWSScope, r contract.AuthResult) {
	r.Session = nil
	if len(r.QRImage) == 0 && r.QRURL != "" {
		u, e := url.Parse(r.QRURL)
		if e == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" && (u.Hostname() == "sso.unipus.cn" || strings.HasSuffix(u.Hostname(), ".chaoxing.com") || strings.HasSuffix(u.Hostname(), ".icourse163.org")) && len(r.QRURL) <= 8192 {
			r.QRImage, _ = qrcode.Encode(r.QRURL, qrcode.Medium, 256)
			r.QRMediaType = "image/png"
		}
	}
	s.qrMu.Lock()
	defer s.qrMu.Unlock()
	now := time.Now()
	for key, value := range s.qr {
		if !value.ExpiresAt.IsZero() && !now.Before(value.ExpiresAt) {
			delete(s.qr, key)
		}
	}
	if r.Status == "authorized" || r.Status == "expired" {
		delete(s.qr, scope)
		return
	}
	if len(s.qr) >= 256 {
		delete(s.qr, scope)
		return
	}
	s.qr[scope] = r
}
func (s *Service) Auth(ctx context.Context, scope model.DWSScope) (contract.AuthResult, error) {
	var result contract.AuthResult
	e := s.operate(ctx, scope, "", func(ctx context.Context, l model.PlatformLease, state model.PlatformState, a contract.Adapter) error {
		if state.Connection.State == "active" {
			s.qrMu.Lock()
			delete(s.qr, scope)
			s.qrMu.Unlock()
			result = contract.AuthResult{Status: "authorized", ExpiresAt: state.AuthExpiresAt}
			return nil
		}
		if state.Connection.State != "pending" && state.Connection.State != "scanned" {
			result = contract.AuthResult{Status: "expired", ExpiresAt: state.AuthExpiresAt}
			return nil
		}
		if !time.Now().Before(state.AuthExpiresAt) {
			if closer, ok := a.(contract.AuthCanceller); ok {
				_ = closer.CancelAuth(ctx, adapterScope(scope))
			}
			s.qrMu.Lock()
			delete(s.qr, scope)
			s.qrMu.Unlock()
			result = contract.AuthResult{Status: "expired", ExpiresAt: state.AuthExpiresAt}
			return s.saveAuth(ctx, l, result)
		}
		raw, e := s.vault.Open(scope.UserID, scope.ConnectionID, state.Session)
		if e != nil {
			return e
		}
		defer clear(raw)
		result, e = a.PollAuth(ctx, adapterScope(scope), contract.Session(raw))
		if e != nil {
			return e
		}
		if e = s.saveAuth(ctx, l, result); e != nil {
			return e
		}
		s.qrMu.Lock()
		old := s.qr[scope]
		s.qrMu.Unlock()
		if result.Status == "pending" || result.Status == "scanned" {
			if result.QRURL == "" {
				result.QRURL = old.QRURL
			}
			if len(result.QRImage) == 0 {
				result.QRImage = old.QRImage
				result.QRMediaType = old.QRMediaType
			}
		}
		s.remember(scope, result)
		s.qrMu.Lock()
		cached, ok := s.qr[scope]
		s.qrMu.Unlock()
		if ok {
			result = cached
		}
		return nil
	})
	return result, e
}
func (s *Service) Sync(ctx context.Context, scope model.DWSScope, resource contract.Resource) error {
	if resource == contract.Discussion {
		return s.syncDiscussionCourses(ctx, scope)
	}
	return s.syncPage(ctx, scope, resource, "", "")
}
func (s *Service) syncPage(ctx context.Context, scope model.DWSScope, resource contract.Resource, courseID, classID string) error {
	key := string(resource)
	if courseID != "" {
		key = discussionPartitionKey(courseID, classID)
	}
	return s.operate(ctx, scope, key, func(ctx context.Context, l model.PlatformLease, state model.PlatformState, a contract.Adapter) (resultErr error) {
		l.CheckpointKey = key
		started := time.Now()
		pageCount := 0
		hasMore := false
		if observer, ok := s.repo.(model.SyncObserver); ok && validResource(resource) {
			if e := observer.StartSync(ctx, l, string(resource)); e != nil {
				return e
			}
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				code := ""
				if resultErr != nil {
					code = errorCode(resultErr)
				}
				_ = observer.FinishSync(cleanup, l, string(resource), code, time.Since(started).Milliseconds(), pageCount, hasMore)
			}()
		}
		if state.Connection.State != "active" {
			return contract.ErrAuthRequired
		}
		supported := false
		for _, r := range a.Describe().Resources {
			if r == resource {
				supported = true
			}
		}
		if !supported {
			return contract.ErrUnsupported
		}
		raw, e := s.vault.Open(scope.UserID, scope.ConnectionID, state.Session)
		if e != nil {
			return e
		}
		defer clear(raw)
		cursor := ""
		if len(state.Cursor) > 0 {
			b, e := s.vault.Open(scope.UserID, scope.ConnectionID, state.Cursor)
			if e != nil {
				return e
			}
			cursor = string(b)
			clear(b)
		}
		var page contract.Page
		if courseID != "" {
			partitioned, ok := a.(contract.DiscussionPartitioner)
			if !ok {
				return contract.ErrUnsupported
			}
			page, e = partitioned.SyncDiscussionCourse(ctx, adapterScope(scope), contract.Session(raw), courseID, classID, cursor)
		} else {
			page, e = a.Sync(ctx, adapterScope(scope), contract.Session(raw), resource, cursor)
		}
		if e != nil {
			return e
		}
		if page.HasMore && (page.NextCursor == "" || page.NextCursor == cursor) {
			return contract.ErrParse
		}
		if len(page.Records) > 500 {
			return contract.ErrParse
		}
		records := make([]model.PlatformRecord, 0, len(page.Records))
		for _, record := range page.Records {
			if record.ExternalID == "" || record.Kind != resource {
				return contract.ErrParse
			}
			observed := record.ObservedAt
			if observed.IsZero() {
				observed = time.Now().UTC()
			}
			record.ObservedAt = time.Time{}
			if record.Fields == nil {
				record.Fields = map[string]any{}
			}
			content, e := json.Marshal(record)
			if e != nil {
				return contract.ErrParse
			}
			hash := sha256.Sum256(content)
			records = append(records, model.PlatformRecord{Source: state.Connection.Source, Kind: string(resource), ExternalID: record.ExternalID, ParentID: record.ParentID, Hash: hex.EncodeToString(hash[:]), Content: content, ObservedAt: observed})
		}
		updated := page.Session
		if updated == nil {
			updated = contract.Session(raw)
		}
		sealed, e := s.vault.Seal(scope.UserID, scope.ConnectionID, updated)
		if e != nil {
			return e
		}
		next, e := s.vault.Seal(scope.UserID, scope.ConnectionID, []byte(page.NextCursor))
		if e != nil {
			return e
		}
		if e := s.repo.Commit(ctx, l, string(resource), records, sealed, next); e != nil {
			return e
		}
		pageCount = len(records)
		hasMore = page.HasMore
		return nil
	})
}
func validResource(r contract.Resource) bool {
	return r == contract.Course || r == contract.Assignment || r == contract.Exam || r == contract.Progress || r == contract.Notice || r == contract.Discussion || r == contract.Material
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) Run(ctx context.Context) error {
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, a := range s.adapters {
			if closer, ok := a.(contract.AuthCloser); ok {
				_ = closer.Close(cleanup)
			}
		}
	}()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.wake:
		case <-timer.C:
		}
		after := ""
		for {
			targets, e := s.repo.Targets(ctx, after, 100)
			if e != nil {
				break
			}
			for _, c := range targets {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				scope := scopeOf(c)
				if c.State == "pending" || c.State == "scanned" {
					_, _ = s.Auth(ctx, scope)
					continue
				}
				a := s.adapters[c.Source]
				if a == nil {
					continue
				}
				for _, resource := range a.Describe().Resources {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					_ = s.Sync(ctx, scope, resource)
				}
			}
			if len(targets) < 100 {
				break
			}
			after = targets[len(targets)-1].ID
		}
		timer.Reset(s.interval)
	}
}
func (s *Service) Disconnect(ctx context.Context, scope model.DWSScope) error {
	c, e := s.repo.Get(ctx, scope)
	if e != nil {
		return e
	}
	if e = s.repo.Disconnect(ctx, scope); e != nil {
		return e
	}
	if a := s.adapters[c.Source]; a != nil {
		if closer, ok := a.(contract.AuthCanceller); ok {
			_ = closer.CancelAuth(ctx, adapterScope(scope))
		}
	}
	s.qrMu.Lock()
	delete(s.qr, scope)
	s.qrMu.Unlock()
	return nil
}
