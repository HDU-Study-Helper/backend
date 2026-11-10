package contract

import "context"

// PasswordLogin is an optional adapter port. Credentials are transient inputs;
// only the returned opaque session is persisted by the encrypted host store.
type PasswordLogin interface {
	Login(context.Context, Scope, string, string) (AuthResult, error)
}

// AcademicTermReader requires an explicit academic starting year and term 1/2.
// The host separates checkpoints by resource, year and term.
type AcademicTermReader interface {
	SyncTerm(context.Context, Scope, Session, Resource, int, int, string) (Page, error)
}
