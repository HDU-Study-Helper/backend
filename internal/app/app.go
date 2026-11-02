package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"academic/backend/internal/bridge"
	"academic/backend/internal/chaoxing"
	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/dbbridge"
	"academic/backend/internal/dws"
	"academic/backend/internal/mooc"
	"academic/backend/internal/platform"
	"academic/backend/internal/preview"
	"academic/backend/internal/trigger"
	"academic/backend/internal/ucampus"
	"academic/backend/internal/users"
)

// App owns process resources. Only this composition layer knows concrete repositories.
type App struct {
	config    Config
	db        *dbbridge.DB
	users     *users.Service
	dws       *dws.Service
	platforms *platform.Service
	handler   http.Handler
}

func New(ctx context.Context, cfg Config) (*App, error) {
	return NewWithTrigger(ctx, cfg, trigger.Noop{})
}

// NewWithTrigger is the composition port for a future Agent change handler.
func NewWithTrigger(ctx context.Context, cfg Config, handler trigger.Handler) (*App, error) {
	db, err := dbbridge.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	if err = db.Migrate(ctx); err != nil {
		return nil, errors.New("database migration failed")
	}
	db.SetTriggerHandler(handler)
	us, err := users.New(db.Users(), users.Config{SecureCookies: cfg.SecureCookies, Origin: cfg.PublicOrigin})
	if err != nil {
		return nil, errors.New("invalid user service configuration")
	}
	if cfg.BootstrapUsername != "" {
		if err = us.Bootstrap(ctx, cfg.BootstrapUsername, cfg.BootstrapPassword); err != nil {
			return nil, errors.New("initial administrator setup failed")
		}
	}
	var ds *dws.Service
	if cfg.DWSBinary != "" {
		vault, e := credential.New(cfg.CredentialKey)
		if e != nil {
			return nil, errors.New("invalid credential encryption key")
		}
		ds, e = dws.New(db.DWS(), vault, dws.Config{BinaryPath: cfg.DWSBinary, BinarySHA256: cfg.DWSBinarySHA256, WorkspaceRoot: cfg.DWSWorkspaceRoot, PollInterval: cfg.DWSPollInterval, CommandTimeout: cfg.DWSCommandTimeout})
		if e != nil {
			return nil, errors.New("DWS configuration or binary verification failed")
		}
	}
	var ps *platform.Service
	if len(cfg.CredentialKey) > 0 {
		vault, e := credential.New(cfg.CredentialKey)
		if e != nil {
			return nil, errors.New("invalid credential encryption key")
		}
		deps := contract.Dependencies{HTTPClient: &http.Client{Timeout: 40 * time.Second}}
		cx, e := chaoxing.New(deps)
		if e != nil {
			return nil, e
		}
		mc, e := mooc.New(deps)
		if e != nil {
			return nil, e
		}
		uc, e := ucampus.New(deps)
		if e != nil {
			return nil, e
		}
		ps, e = platform.New(db.Platforms(), vault, []contract.Adapter{cx, mc, uc})
		if e != nil {
			return nil, e
		}
	}
	app := &App{config: cfg, db: db, users: us, dws: ds, platforms: ps}
	var descriptors []contract.Descriptor
	if ps != nil {
		descriptors = ps.Sources()
	}
	inspect := preview.New(db.Preview(), descriptors, int(cfg.DWSPollInterval/time.Second))
	app.handler = routerWithPreview(us, ds, bridge.Demo{}, inspect, ps)
	ok = true
	return app, nil
}

func (a *App) Close() { a.db.Close() }

func (a *App) ImportUCampus(ctx context.Context, owner string, raw []byte) error {
	if a.platforms == nil {
		return errors.New("platform encryption is not configured")
	}
	_, e := a.platforms.ImportUCampus(ctx, owner, raw)
	return e
}

func (a *App) ImportDWS(ctx context.Context, owner, connection, root string) error {
	if a.dws == nil {
		return errors.New("DWS is not configured")
	}
	return a.dws.ImportWorkspace(ctx, owner, connection, root)
}

// Run starts and joins explicitly managed HTTP and polling lifecycles.
func (a *App) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", a.config.HTTPAddr)
	if err != nil {
		return errors.New("HTTP server failed; check listen address")
	}
	server := &http.Server{Handler: a.handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers chan error
	workerCount := 0
	if a.dws != nil {
		workerCount++
	}
	if a.platforms != nil {
		workerCount++
	}
	if workerCount > 0 {
		workers = make(chan error, workerCount)
	}
	if a.dws != nil {
		go func() { workers <- a.dws.Run(runCtx) }()
	}
	if a.platforms != nil {
		go func() { workers <- a.platforms.Run(runCtx) }()
	}
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(listener) }()
	slog.Info("API started", "addr", listener.Addr().String(), "dws_enabled", a.dws != nil)
	var runErr error
	select {
	case <-ctx.Done():
	case e := <-workers:
		workerCount--
		if e != nil && !errors.Is(e, context.Canceled) {
			runErr = errors.New("DWS worker stopped unexpectedly")
		} else if ctx.Err() == nil {
			runErr = errors.New("DWS worker stopped unexpectedly")
		}
	case e := <-errs:
		if !errors.Is(e, http.ErrServerClosed) {
			runErr = errors.New("HTTP server failed")
		}
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if e := server.Shutdown(shutdownCtx); e != nil {
		_ = server.Close()
		runErr = errors.New("HTTP graceful shutdown failed")
	}
	for workerCount > 0 {
		select {
		case e := <-workers:
			workerCount--
			if e != nil && !errors.Is(e, context.Canceled) {
				runErr = errors.New("DWS worker stopped unexpectedly")
			}
		case <-shutdownCtx.Done():
			workerCount = 0
			runErr = errors.New("worker shutdown timed out")
		}
	}
	slog.Info("API stopped")
	return runErr
}
