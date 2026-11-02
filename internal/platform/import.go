package platform

import (
	"context"
	"time"

	"academic/backend/internal/contract"
	"academic/backend/internal/model"
	"academic/backend/internal/ucampus"
)

// ImportUCampus is operator-only. Never expose raw tokens as ordinary API metadata.
// It creates a distinct connection so importing another account cannot mix its history.
func (s *Service) ImportUCampus(ctx context.Context, owner string, raw []byte) (model.PlatformConnection, error) {
	c, e := s.repo.Create(ctx, owner, "ucampus")
	if e != nil {
		return c, e
	}
	scope := scopeOf(c)
	e = s.operate(ctx, scope, "", func(ctx context.Context, l model.PlatformLease, state model.PlatformState, _ contract.Adapter) error {
		session, e := ucampus.ImportSession(ctx, adapterScope(scope), raw)
		if e != nil {
			return e
		}
		defer clear(session)
		return s.saveAuth(ctx, l, contract.AuthResult{Status: "authorized", Session: session, ExpiresAt: time.Now().Add(time.Hour)})
	})
	return c, e
}
