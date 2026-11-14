package contract

import "context"

// SessionRefresher recovers an existing authorized session using official read-only
// cookie exchange endpoints. It must verify the resulting login, preserve Scope,
// never request new credentials or silently select another account, and return
// ErrAuthRequired when existing login material cannot recover authorization.
// Transport, rate-limit and parse errors retain their original meanings.
// The host encrypts and saves a successful result under the current fencing lease.
type SessionRefresher interface {
	RefreshSession(context.Context, Scope, Session) (Session, error)
}
