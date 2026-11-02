// Package dbbridge owns all PostgreSQL access. Callers use model repositories.
package dbbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"academic/backend/internal/trigger"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	pool           *pgxpool.Pool
	triggerMu      sync.RWMutex
	triggerHandler trigger.Handler
}

// SetTriggerHandler installs the best-effort after-commit port. It is safe to
// call concurrently; nil restores the default no-op handler.
func (d *DB) SetTriggerHandler(handler trigger.Handler) {
	d.triggerMu.Lock()
	defer d.triggerMu.Unlock()
	d.triggerHandler = handler
}

func (d *DB) emit(ctx context.Context, changes []trigger.Change) {
	d.triggerMu.RLock()
	handler := d.triggerHandler
	d.triggerMu.RUnlock()
	if handler == nil {
		handler = trigger.Noop{}
	}
	for _, change := range changes {
		if !trigger.Eligible(change) {
			continue
		}
		// A committed change remains eligible even if its request was cancelled.
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		err := handler.Handle(callCtx, change)
		cancel()
		if err != nil {
			slog.Warn("business change handler failed", "resource", change.Resource)
		}
	}
}

func Open(ctx context.Context, url string) (*DB, error) {
	if strings.TrimSpace(url) == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	cfg.MaxConns = 10
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, err := pgxpool.NewWithConfig(startup, cfg)
	if err != nil {
		return nil, model.ErrInternal
	}
	if err = p.Ping(startup); err != nil {
		p.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, model.ErrInternal
	}
	return &DB{pool: p}, nil
}
func (d *DB) Close()                      { d.pool.Close() }
func (d *DB) Users() model.UserRepository { return &users{db: d} }
func (d *DB) DWS() model.DWSRepository    { return &dws{db: d} }
func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("cryptographic randomness unavailable")
	}
	return hex.EncodeToString(b[:])
}
func storageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(err, &p) {
		switch p.Code {
		case "23505":
			return model.ErrConflict
		case "23503":
			return model.ErrNotFound
		case "23514", "22001":
			return model.ErrInvalidArgument
		}
	}
	return model.ErrInternal
}
func (d *DB) transaction(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, e := d.pool.Begin(ctx)
	if e != nil {
		return storageError(e)
	}
	defer tx.Rollback(context.Background())
	if e = fn(tx); e != nil {
		return e
	}
	return storageError(tx.Commit(ctx))
}
