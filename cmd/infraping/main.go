// Command infraping vigila webs, puertos, certificados y DNS, avisa cuando algo cae y sirve una
// página de estado.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/juanmmm21/infraping/internal/check"
	"github.com/juanmmm21/infraping/internal/config"
	"github.com/juanmmm21/infraping/internal/history"
	"github.com/juanmmm21/infraping/internal/monitor"
	"github.com/juanmmm21/infraping/internal/notify"
	"github.com/juanmmm21/infraping/internal/status"
)

// version se fija al compilar con -ldflags "-X main.version=...".
var version = "dev"

const usage = `InfraPing — uptime monitoring in a single binary.

Usage:
  infraping run         [-c config.yaml]   watch every check, send alerts, serve the status page
  infraping check       [-c config.yaml]   run every check once and exit non-zero if any fails
  infraping validate    [-c config.yaml]   validate the configuration file
  infraping test-notify [-c config.yaml] [-n name]   send a test message to the notifiers
  infraping version

The config path defaults to $INFRAPING_CONFIG or ./infraping.yaml.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "infraping", version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	defaultPath := os.Getenv("INFRAPING_CONFIG")
	if defaultPath == "" {
		defaultPath = "infraping.yaml"
	}
	path := fs.String("c", defaultPath, "path to the configuration file")
	only := fs.String("n", "", "test-notify: only this notifier")
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "infraping: %s\n", err)
		return 1
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	switch cmd {
	case "validate":
		fmt.Fprintf(stdout, "%s is valid: %d checks, %d notifiers\n", *path, len(cfg.Checks), len(cfg.Notifiers))
		return 0
	case "check":
		return checkOnce(ctx, cfg, stdout, stderr)
	case "test-notify":
		return testNotify(ctx, cfg, *only, stdout, stderr, logger)
	case "run":
		if err := serve(ctx, cfg, logger); err != nil {
			logger.Error("infraping stopped", "err", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "infraping: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

func buildTargets(cfg *config.Config) ([]monitor.Target, error) {
	targets := make([]monitor.Target, 0, len(cfg.Checks))
	for _, c := range cfg.Checks {
		probe, err := check.New(c)
		if err != nil {
			return nil, err
		}
		targets = append(targets, monitor.Target{Config: c, Checker: probe})
	}
	return targets, nil
}

func buildNotifiers(cfg *config.Config) ([]notify.Notifier, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	out := make([]notify.Notifier, 0, len(cfg.Notifiers))
	for _, n := range cfg.Notifiers {
		notifier, err := notify.New(n, client)
		if err != nil {
			return nil, err
		}
		out = append(out, notifier)
	}
	return out, nil
}

// checkOnce sirve para CI o cron: una pasada, una tabla y el código de salida dice si todo va bien.
func checkOnce(ctx context.Context, cfg *config.Config, stdout, stderr io.Writer) int {
	targets, err := buildTargets(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "infraping: %s\n", err)
		return 1
	}
	results := make([]check.Result, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = t.Checker.Check(ctx)
		}()
	}
	wg.Wait()

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tCHECK\tLATENCY\tDETAIL")
	failed := 0
	for i, t := range targets {
		res := results[i]
		mark := "UP"
		if !res.OK {
			mark = "DOWN"
			failed++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mark, t.Config.Name, res.Latency.Round(time.Millisecond), res.Message)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stderr, "infraping: %s\n", err)
		return 1
	}
	if failed > 0 {
		fmt.Fprintf(stdout, "\n%d of %d checks failing\n", failed, len(targets))
		return 1
	}
	return 0
}

func testNotify(ctx context.Context, cfg *config.Config, only string, stdout, stderr io.Writer, logger *slog.Logger) int {
	notifiers, err := buildNotifiers(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "infraping: %s\n", err)
		return 1
	}
	if len(notifiers) == 0 {
		fmt.Fprintln(stderr, "infraping: no notifiers configured")
		return 1
	}
	dispatcher := notify.NewDispatcher(notifiers, logger)
	var names []string
	if only != "" {
		names = []string{only}
		if len(dispatcher.Targets(names)) == 0 {
			fmt.Fprintf(stderr, "infraping: unknown notifier %q\n", only)
			return 1
		}
	}
	msg := notify.Message{Kind: notify.KindTest, Check: "test", Reason: "If you can read this, InfraPing can reach you.", At: time.Now()}
	failed := 0
	for _, n := range dispatcher.Targets(names) {
		if err := dispatcher.SendWithRetry(ctx, n, msg); err != nil {
			fmt.Fprintf(stdout, "FAIL  %s: %s\n", n.Name(), err)
			failed++
			continue
		}
		fmt.Fprintf(stdout, "OK    %s\n", n.Name())
	}
	if failed > 0 {
		return 1
	}
	return 0
}

const (
	persistEvery    = time.Minute
	shutdownTimeout = 10 * time.Second
)

func serve(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	targets, err := buildTargets(cfg)
	if err != nil {
		return err
	}
	notifiers, err := buildNotifiers(cfg)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(cfg.Checks))
	var public []status.Entry
	for _, c := range cfg.Checks {
		names = append(names, c.Name)
		if c.IsPublic() {
			public = append(public, status.Entry{Name: c.Name})
		}
	}
	store := history.New()
	if cfg.StateFile != "" {
		if err := store.Load(cfg.StateFile, names); err != nil {
			return err
		}
	}
	dispatcher := notify.NewDispatcher(notifiers, logger)
	runner := monitor.NewRunner(targets, store, dispatcher, logger)

	var srv *http.Server
	if cfg.StatusPage.Enabled {
		ln, err := net.Listen("tcp", cfg.StatusPage.Listen)
		if err != nil {
			return fmt.Errorf("status page: %w", err)
		}
		srv = &http.Server{
			Handler:           status.NewServer(cfg.StatusPage.Title, public, store, logger).Handler(),
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("status page stopped", "err", err)
			}
		}()
		logger.Info("status page listening", "addr", ln.Addr().String())
	}

	logger.Info("infraping started", "version", version, "checks", len(targets), "notifiers", strings.Join(notifierNames(notifiers), ","))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		monitor.Persist(ctx, store, cfg.StateFile, persistEvery, logger)
	}()
	runner.Run(ctx)
	wg.Wait()

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	var errs []error
	if srv != nil {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("status page shutdown: %w", err))
		}
	}
	// Los avisos ya decididos tienen hasta el plazo de apagado para salir.
	done := make(chan struct{})
	go func() {
		dispatcher.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		errs = append(errs, errors.New("gave up waiting for pending notifications"))
	}
	return errors.Join(errs...)
}

func notifierNames(ns []notify.Notifier) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Name())
	}
	return out
}
