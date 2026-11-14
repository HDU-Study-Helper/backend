package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"academic/backend/internal/academic"
	"academic/backend/internal/agentadmin"
	"academic/backend/internal/bridge"
	"academic/backend/internal/chaoxing"
	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/dbbridge"
	"academic/backend/internal/dws"
	"academic/backend/internal/jwxt"
	"academic/backend/internal/model"
	"academic/backend/internal/mooc"
	"academic/backend/internal/platform"
	"academic/backend/internal/preview"
	"academic/backend/internal/runcontrol"
	"academic/backend/internal/schedule"
	"academic/backend/internal/scheduler"
	"academic/backend/internal/trigger"
	"academic/backend/internal/ucampus"
	"academic/backend/internal/users"
)

// App owns process resources. Only this composition layer knows concrete repositories.
type App struct {
	academic  *academic.Service
	control   *runcontrol.Service
	schedules *schedule.Service
	refresh   *scheduler.Task
	agents    *agentadmin.Console
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
		jw, e := jwxt.New(deps)
		if e != nil {
			return nil, e
		}
		ps, e = platform.New(db.Platforms(), vault, []contract.Adapter{cx, mc, uc, jw})
		if e != nil {
			return nil, e
		}
	}
	var agentVault *credential.Vault
	if len(cfg.CredentialKey) > 0 {
		agentVault, err = credential.New(cfg.CredentialKey)
		if err != nil {
			return nil, errors.New("invalid Agent credential key")
		}
	}
	schedules := schedule.New(db.Schedules(), bridge.Demo{})
	console, err := agentadmin.New(ctx, db.Agents(), db.AgentAdmin(), db.Users(), agentVault, cfg.AgentDemoEnabled, db.AgentData(), schedules)
	if err != nil {
		return nil, err
	}
	control := runcontrol.New(db.Control(), console.Service, db.AgentData())
	app := &App{academic: academic.New(db.Academic()), control: control, config: cfg, db: db, users: us, dws: ds, platforms: ps, agents: console}
	var descriptors []contract.Descriptor
	if ps != nil {
		descriptors = ps.Sources()
	}
	inspect := preview.New(db.Preview(), descriptors, int(cfg.DWSPollInterval/time.Second))
	if !cfg.APIOnly && (ds != nil || ps != nil) {
		app.refresh = scheduler.New("information-refresh", "信息刷新 · 学习平台与 DWS", cfg.DWSPollInterval)
		if cfg.RefreshPaused {
			app.refresh.Update(false, int(cfg.DWSPollInterval/time.Second))
		}
	}
	inspect.Intervals = func() (int, int) {
		if app.refresh != nil {
			seconds := app.refresh.Snapshot().IntervalSeconds
			return seconds, seconds
		}
		return 600, int(cfg.DWSPollInterval / time.Second)
	}
	app.schedules = schedules
	app.handler = routerWithAcademic(us, ds, app.schedules, inspect, console, app.refresh, app.schedules, control, app.academic, ps)
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
	server := &http.Server{Handler: a.handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 0, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers chan error
	workerCount := 0
	if a.control != nil && !a.config.APIOnly {
		workerCount++
	}
	if a.dws != nil {
		workerCount++
	}
	if a.platforms != nil {
		workerCount++
	}
	if a.refresh != nil {
		workerCount++
	}
	if workerCount > 0 {
		workers = make(chan error, workerCount)
	}
	if a.control != nil && !a.config.APIOnly {
		go func() { workers <- a.control.Run(runCtx) }()
	}
	if a.dws != nil {
		go func() { workers <- a.dws.Run(runCtx) }()
	}
	if a.platforms != nil {
		go func() { workers <- a.platforms.Run(runCtx) }()
	}
	if a.refresh != nil {
		go func() {
			workers <- a.refresh.Run(runCtx, func(ctx context.Context) bool {
				failed := false
				if a.control != nil {
					round, err := a.control.StartRefresh(ctx)
					if err != nil {
						slog.Error("refresh round persistence failed")
						return true
					}
					ctx = model.WithControlRound(ctx, round)
					defer func() {
						c, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
						defer stop()
						if err := a.control.Store.FinishRound(c, round, model.RefreshSnapshot(ctx), failed || ctx.Err() != nil); err != nil {
							slog.Error("refresh round finalization failed")
						}
						_ = a.control.Store.Seal(c)
					}()
				}
				for _, source := range []string{"chaoxing", "mooc", "ucampus"} {
					model.InitRefreshSource(ctx, source, a.platforms != nil)
				}
				model.InitRefreshSource(ctx, "dws", a.dws != nil)
				if a.platforms != nil {
					failed = a.platforms.Poll(ctx) || failed
				}
				if a.dws != nil && ctx.Err() == nil {
					failed = a.dws.Poll(ctx) || failed
				}
				return failed || ctx.Err() != nil
			})
		}()
	}
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(listener) }()
	slog.Info("API started", "addr", listener.Addr().String(), "dws_enabled", a.dws != nil, "api_only", a.config.APIOnly)
	var runErr error
	select {
	case <-ctx.Done():
	case e := <-workers:
		workerCount--
		if e != nil && !errors.Is(e, context.Canceled) {
			runErr = errors.New("background worker stopped unexpectedly")
		} else if ctx.Err() == nil {
			runErr = errors.New("background worker stopped unexpectedly")
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
				runErr = errors.New("background worker stopped unexpectedly")
			}
		case <-shutdownCtx.Done():
			workerCount = 0
			runErr = errors.New("worker shutdown timed out")
		}
	}
	slog.Info("API stopped")
	return runErr
}
