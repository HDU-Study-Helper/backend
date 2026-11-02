// Command api assembles and starts the modular backend.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"academic/backend/internal/app"
)

func main() {
	if err := run(); err != nil {
		slog.Error("API stopped", "reason", err.Error())
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := app.LoadConfig()
	if err != nil {
		return err
	}
	application, err := app.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer application.Close()
	if len(os.Args) > 1 && os.Args[1] == "dws-import" {
		args := flag.NewFlagSet("dws-import", flag.ContinueOnError)
		owner := args.String("owner", "", "application user ID")
		connection := args.String("connection", "", "DWS connection ID")
		workspace := args.String("workspace", "", "operator-authorized CLI workspace root")
		if err := args.Parse(os.Args[2:]); err != nil {
			return errors.New("invalid dws-import arguments")
		}
		if *owner == "" || *connection == "" || *workspace == "" || args.NArg() != 0 {
			return errors.New("dws-import requires --owner, --connection and --workspace")
		}
		if err := application.ImportDWS(ctx, *owner, *connection, *workspace); err != nil {
			return errors.New("DWS workspace import failed; verify profile, owner and CLI authorization")
		}
		slog.Info("DWS workspace imported")
		return nil
	}
	if len(os.Args) > 1 && os.Args[1] == "ucampus-import" {
		args := flag.NewFlagSet("ucampus-import", flag.ContinueOnError)
		owner := args.String("owner", "", "application user ID")
		if args.Parse(os.Args[2:]) != nil || *owner == "" || args.NArg() != 0 {
			return errors.New("ucampus-import requires --owner; authorized session JSON is read from stdin")
		}
		raw, e := io.ReadAll(io.LimitReader(os.Stdin, (128<<10)+1))
		if e != nil || len(raw) > 128<<10 {
			return errors.New("invalid authorized session input")
		}
		defer clear(raw)
		if e = application.ImportUCampus(ctx, *owner, raw); e != nil {
			return errors.New("U校园 session import failed; check authorized account and session")
		}
		slog.Info("U校园 session imported")
		return nil
	}
	return application.Run(ctx)
}

// Keep the existing public liveness contract independently testable.
func newRouter() http.Handler { return app.Router(nil, nil, nil) }
