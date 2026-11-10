package platform

import (
	"context"

	"academic/backend/internal/contract"
	"academic/backend/internal/model"
)

// LoginPassword binds login to an application-owned connection before passing
// transient credentials to its adapter. Sessions use the existing scoped vault.
func (s *Service) LoginPassword(ctx context.Context, owner, source, username, password string) (model.PlatformConnection, error) {
	a := s.adapters[source]
	if owner == "" || username == "" || len(username) > 128 || password == "" || len(password) > 1024 {
		return model.PlatformConnection{}, contract.ErrInvalid
	}
	if a == nil || !a.Describe().PasswordLogin {
		return model.PlatformConnection{}, contract.ErrUnsupported
	}
	if _, ok := a.(contract.PasswordLogin); !ok {
		return model.PlatformConnection{}, contract.ErrUnsupported
	}
	c, e := s.repo.Create(ctx, owner, source)
	if e != nil {
		return c, e
	}
	e = s.operate(ctx, scopeOf(c), "", func(ctx context.Context, l model.PlatformLease, state model.PlatformState, adapter contract.Adapter) error {
		login, ok := adapter.(contract.PasswordLogin)
		if !ok {
			return contract.ErrUnsupported
		}
		result, err := login.Login(ctx, adapterScope(l.Scope), username, password)
		if err != nil {
			return err
		}
		defer clear(result.Session)
		if result.Status != "authorized" {
			return contract.ErrAuthRequired
		}
		return s.saveAuth(ctx, l, result)
	})
	if e != nil {
		return c, e
	}
	return s.repo.Get(ctx, scopeOf(c))
}

func (s *Service) SyncTerm(ctx context.Context, scope model.DWSScope, r contract.Resource, year, term int) error {
	if year < 2000 || year > 2100 || (term != 1 && term != 2) || (r != contract.Course && r != contract.Schedule) {
		return contract.ErrInvalid
	}
	if r == contract.Schedule {
		return contract.ErrInvalid
	} // concrete schedules require a calendar
	return s.syncPageTerm(ctx, scope, r, "", "", year, term)
}
