package platform

import (
	"academic/backend/internal/contract"
	"academic/backend/internal/model"
	"context"
	"errors"
)

// refreshSessionLocked uses only an existing login, under the connection fencing
// lease. It never changes collection cursors or revives a disconnected account.
func (s *Service) refreshSessionLocked(ctx context.Context, l model.PlatformLease, state model.PlatformState, a contract.Adapter, raw contract.Session) (contract.Session, error) {
	refresher, ok := a.(contract.SessionRefresher)
	if !ok {
		return nil, contract.ErrUnsupported
	}
	if !state.Connection.Enabled || (state.Connection.State != "active" && state.Connection.State != "expired") || len(raw) == 0 {
		return nil, contract.ErrAuthRequired
	}
	updated, err := refresher.RefreshSession(ctx, adapterScope(l.Scope), raw)
	if err != nil {
		return nil, err
	}
	if len(updated) == 0 || len(updated) > 1<<20 {
		clear(updated)
		return nil, contract.ErrParse
	}
	sealed, err := s.vault.Seal(l.Scope.UserID, l.Scope.ConnectionID, updated)
	if err != nil {
		clear(updated)
		return nil, err
	}
	if err = s.repo.SaveAuth(ctx, l, sealed, "active", state.AuthExpiresAt); err != nil {
		clear(updated)
		return nil, err
	}
	return updated, nil
}

// RefreshSession explicitly verifies and refreshes the saved login. No cookie or
// opaque session state is returned to the caller; business checkpoints are intact.
func (s *Service) RefreshSession(ctx context.Context, scope model.DWSScope) (model.PlatformConnection, error) {
	err := s.operate(ctx, scope, "", func(ctx context.Context, l model.PlatformLease, state model.PlatformState, a contract.Adapter) error {
		raw, err := s.vault.Open(scope.UserID, scope.ConnectionID, state.Session)
		if err != nil {
			return err
		}
		defer clear(raw)
		updated, err := s.refreshSessionLocked(ctx, l, state, a, raw)
		defer clear(updated)
		return err
	})
	if err != nil {
		return model.PlatformConnection{}, err
	}
	return s.repo.Get(ctx, scope)
}

// readWithSessionRecovery retries exactly once only after an explicit auth error.
// Rate limits, parse errors and upstream outages never initiate cookie exchange.
func (s *Service) readWithSessionRecovery(ctx context.Context, l model.PlatformLease, state model.PlatformState, a contract.Adapter, raw contract.Session, read func(contract.Session) (contract.Page, error)) (contract.Page, error) {
	page, err := read(raw)
	if !errors.Is(err, contract.ErrAuthRequired) {
		return page, err
	}
	if _, ok := a.(contract.SessionRefresher); !ok {
		return page, err
	}
	updated, err := s.refreshSessionLocked(ctx, l, state, a, raw)
	if err != nil {
		return contract.Page{}, err
	}
	defer clear(updated)
	page, err = read(updated)
	if err == nil && page.Session == nil {
		page.Session = append(contract.Session(nil), updated...)
	}
	return page, err
}
