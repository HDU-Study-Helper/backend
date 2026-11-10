// Package platform owns authenticated platform workflows and explicitly scheduled polling.
package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
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
	qrMu     sync.Mutex
	qr       map[model.DWSScope]contract.AuthResult
}

func New(repo model.PlatformRepository, vault *credential.Vault, adapters []contract.Adapter) (*Service, error) {
	if repo == nil || vault == nil {
		return nil, model.ErrInvalidArgument
	}
	s := &Service{repo: repo, vault: vault, adapters: map[string]contract.Adapter{}, qr: map[model.DWSScope]contract.AuthResult{}}
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
	for _, key := range []string{"chaoxing", "mooc", "ucampus", "jwxt"} {
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
	case errors.Is(e, contract.ErrVerificationRequired):
		return "verification_required"
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
		connection, err := s.repo.Get(ctx, scope)
		if err != nil {
			return err
		}
		if _, partitioned := s.adapters[connection.Source].(contract.DiscussionPartitioner); partitioned {
			return s.syncDiscussionCourses(ctx, scope)
		}
	}
	return s.syncPage(ctx, scope, resource, "", "")
}
func (s *Service) syncPage(ctx context.Context, scope model.DWSScope, resource contract.Resource, courseID, classID string) error {
	return s.syncPageTerm(ctx, scope, resource, courseID, classID, 0, 0)
}
func (s *Service) syncPageTerm(ctx context.Context, scope model.DWSScope, resource contract.Resource, courseID, classID string, year, term int) error {
	_, err := s.syncPageResult(ctx, scope, resource, courseID, classID, year, term)
	return err
}

// syncPageResult exposes continuation only after a successful atomic page commit.
func (s *Service) syncPageResult(ctx context.Context, scope model.DWSScope, resource contract.Resource, courseID, classID string, year, term int) (bool, error) {
	committedMore := false
	key := string(resource)
	if year != 0 {
		key += ":" + strconv.Itoa(year) + ":" + strconv.Itoa(term)
	}
	if courseID != "" {
		key = discussionPartitionKey(courseID, classID)
	}
	err := s.operate(ctx, scope, key, func(ctx context.Context, l model.PlatformLease, state model.PlatformState, a contract.Adapter) (resultErr error) {
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
		ctx, resultErr = s.collectionContext(ctx, scope)
		if resultErr != nil {
			return resultErr
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
		if year != 0 {
			reader, ok := a.(contract.AcademicTermReader)
			if !ok {
				return contract.ErrUnsupported
			}
			page, e = reader.SyncTerm(ctx, adapterScope(scope), contract.Session(raw), resource, year, term, cursor)
		} else if courseID != "" {
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
		committedMore = page.HasMore
		return nil
	})
	return committedMore, err
}
func validResource(r contract.Resource) bool {
	return r == contract.Course || r == contract.Chapter || r == contract.Lesson || r == contract.Schedule || r == contract.Assignment || r == contract.Exam || r == contract.Progress || r == contract.Notice || r == contract.Discussion || r == contract.Material
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
	<-ctx.Done()
	return ctx.Err()
}

// Poll performs one scan; the app owns the shared scheduling clock.
func (s *Service) Poll(ctx context.Context) bool {
	failed := false
	after := ""
	for {
		targets, err := s.repo.Targets(ctx, after, 100)
		if err != nil {
			for _, source := range []string{"chaoxing", "mooc", "ucampus"} {
				model.RefreshFailure(ctx, source, "target_list_failed")
			}
			return true
		}
		for _, c := range targets {
			if ctx.Err() != nil {
				return true
			}
			a := s.adapters[c.Source]
			if a == nil || a.Describe().ManualSyncOnly || c.State != "active" {
				continue
			}
			model.RefreshConnection(ctx, c.Source)
			for _, resource := range a.Describe().Resources {
				if ctx.Err() != nil {
					return true
				}
				err := s.Sync(ctx, scopeOf(c), resource)
				code := ""
				if err != nil {
					failed = true
					code = errorCode(err)
				}
				model.RefreshAttempt(ctx, c.Source, code)
				if errors.Is(err, contract.ErrVerificationRequired) || errors.Is(err, contract.ErrRateLimited) {
					break
				}
			}
		}
		if len(targets) < 100 {
			return failed
		}
		after = targets[len(targets)-1].ID
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
