// Package app wires dependencies together and runs one process mode.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-gateway/internal/auth"
	"github.com/ncecere/open-mcp-gateway/internal/buildinfo"
	"github.com/ncecere/open-mcp-gateway/internal/config"
	"github.com/ncecere/open-mcp-gateway/internal/httpapi"
	"github.com/ncecere/open-mcp-gateway/internal/platform"
	"github.com/ncecere/open-mcp-gateway/internal/registry"
	"github.com/ncecere/open-mcp-gateway/internal/secrets"
	"github.com/ncecere/open-mcp-gateway/internal/store"
	"github.com/ncecere/open-mcp-gateway/internal/teams"
	"github.com/ncecere/open-mcp-gateway/internal/upstream"
	"github.com/ncecere/open-mcp-gateway/web"
)

// Modes.
const (
	ModeServe   = "serve"   // HTTP API, embedded UI and background loops
	ModeMigrate = "migrate" // apply migrations and exit
)

// dbConnectWait is how long a process waits for Postgres at start-up.
const dbConnectWait = 3 * time.Minute

// cleanupInterval is how often expired sessions and abandoned sign-ins are
// deleted.
const cleanupInterval = 15 * time.Minute

// Run starts the given mode and blocks until ctx is cancelled (or the mode
// completes, for migrate). Shutdown is bounded by SHUTDOWN_TIMEOUT.
func Run(ctx context.Context, mode string, cfg config.Config, log *slog.Logger) error {
	if err := cfg.Validate(mode); err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	log = log.With("mode", mode)
	log.Info("starting", "version", buildinfo.Version, "commit", buildinfo.Commit)

	pool, err := store.OpenWait(ctx, cfg.DatabaseURL, dbConnectWait, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	if mode == ModeMigrate || cfg.MigrateOnStart {
		if err := store.Migrate(ctx, pool, log); err != nil {
			return err
		}
		if mode == ModeMigrate {
			log.Info("migrations complete")
			return nil
		}
	}
	if st, err := store.Migrations(ctx, pool); err != nil {
		return fmt.Errorf("migration state: %w", err)
	} else if st.Pending {
		return fmt.Errorf("the database is at migration %d but this binary needs %d: run `open-mcp-gateway migrate` (or set MIGRATE_ON_START=true)", st.Current, st.Latest)
	}

	health := httpapi.NewHealth(map[string]func(context.Context) error{"postgres": pool.Ping})
	deps, err := NewDeps(cfg, pool, log)
	if err != nil {
		return err
	}
	deps.Health = health
	deps.Web, deps.WebBuilt = web.Assets()
	if !deps.WebBuilt {
		log.Warn("web UI not built; run `make web` before building")
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           httpapi.NewAPIHandler(deps),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: MCP responses will stream (later phases).
		// Handlers bound their own work with contexts.
		BaseContext: func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	loopCtx, stopLoops := context.WithCancel(ctx)
	defer stopLoops()
	go cleanupLoop(loopCtx, deps.Auth, log)
	go deps.Registry.HealthLoop(loopCtx, cfg.HealthCheckInterval, log)
	if err := serve(ctx, srv, log); err != nil {
		return err
	}
	stopLoops()
	return shutdown(cfg, log, health, srv)
}

// NewDeps builds the services the HTTP API needs (Health and Web are left
// to the caller). The configuration must be valid for serve.
func NewDeps(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) (httpapi.Deps, error) {
	box, err := secretBox(cfg)
	if err != nil {
		return httpapi.Deps{}, err
	}
	// The development allowance for MCP server addresses (DEV_AUTH on a
	// loopback APP_URL); there is no setting for it in production.
	client := &upstream.Client{AllowPrivate: cfg.AllowPrivateUpstreams(), Log: log}
	return httpapi.Deps{
		Config:   cfg,
		Pool:     pool,
		Log:      log,
		Auth:     auth.NewService(cfg, pool, log),
		Teams:    teams.NewService(pool),
		Platform: platform.NewService(pool),
		Registry: registry.New(pool, box, client, log),
	}, nil
}

// secretBox opens ENCRYPTION_KEY (and ENCRYPTION_KEY_PREVIOUS).
func secretBox(cfg config.Config) (*secrets.Box, error) {
	key, err := secrets.ParseKey(cfg.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("ENCRYPTION_KEY: %w", err)
	}
	var previous [][]byte
	if cfg.EncryptionKeyPrevious != "" {
		prev, err := secrets.ParseKey(cfg.EncryptionKeyPrevious)
		if err != nil {
			return nil, fmt.Errorf("ENCRYPTION_KEY_PREVIOUS: %w", err)
		}
		previous = append(previous, prev)
	}
	return secrets.New(key, previous...)
}

// cleanupLoop deletes expired sessions and sign-in states. Deletes are
// idempotent, so every replica may run it; no leader election is needed.
func cleanupLoop(ctx context.Context, a *auth.Service, log *slog.Logger) {
	t := time.NewTicker(cleanupInterval)
	defer t.Stop()
	for {
		sessions, logins, err := a.Cleanup(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("session cleanup failed", "err", err)
		case sessions+logins > 0:
			log.Debug("expired sessions deleted", "sessions", sessions, "loginStates", logins)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// serve runs srv until ctx is cancelled or the server fails.
func serve(ctx context.Context, srv *http.Server, log *slog.Logger) error {
	serveErr := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()
	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
	case <-ctx.Done():
	}
	return nil
}

// shutdown drains traffic before stopping: readiness fails first so the load
// balancer removes the pod, then in-flight requests get SHUTDOWN_TIMEOUT to
// finish.
func shutdown(cfg config.Config, log *slog.Logger, health *httpapi.Health, servers ...*http.Server) error {
	log.Info("shutting down", "drain_delay", cfg.ShutdownDelay, "timeout", cfg.ShutdownTimeout)
	health.SetDraining()
	time.Sleep(cfg.ShutdownDelay)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	var errs []error
	for _, srv := range servers {
		if err := srv.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("http shutdown (%s): %w", srv.Addr, err))
		}
	}
	if len(errs) == 0 {
		log.Info("shutdown complete")
	}
	return errors.Join(errs...)
}
