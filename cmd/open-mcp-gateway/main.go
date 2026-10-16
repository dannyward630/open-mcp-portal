// Command open-mcp-gateway runs Open MCP Gateway, a self-hosted gateway that
// gives a company's people and agents governed access to its MCP servers.
//
//	open-mcp-gateway serve     HTTP API, embedded UI and background loops
//	open-mcp-gateway migrate   apply database migrations and exit
//	open-mcp-gateway doctor    check the configuration, Postgres and migrations
//	open-mcp-gateway version   print the version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ncecere/open-mcp-gateway/internal/app"
	"github.com/ncecere/open-mcp-gateway/internal/buildinfo"
	"github.com/ncecere/open-mcp-gateway/internal/config"
	"github.com/ncecere/open-mcp-gateway/internal/doctor"
)

const usage = `usage: open-mcp-gateway <command>

commands:
  serve     HTTP API, embedded UI and background loops
  migrate   apply database migrations and exit
  doctor    check the configuration, Postgres and migrations (--json, --help)
  version   print the version

Configuration comes from environment variables and the optional YAML file
named by OMG_CONFIG_FILE. See .env.example.
`

// errChecksFailed makes `doctor` exit non-zero after printing its report.
var errChecksFailed = errors.New("some checks failed")

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "open-mcp-gateway:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := app.ModeServe
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "version", "--version":
		fmt.Println(buildinfo.String())
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "doctor":
		return runDoctor(args[1:], os.Stdout)
	case app.ModeServe, app.ModeMigrate:
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	log := newLogger(os.Stdout, cfg.LogLevel, cfg.LogFormat)
	// Packages that log through slog's package functions (httpx.Internal)
	// write in the same structured format as everything else.
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx, cmd, cfg, log)
}

func newLogger(w io.Writer, level, format string) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(level))
	opts := &slog.HandlerOptions{Level: lvl}
	if format == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

// runDoctor implements `open-mcp-gateway doctor [flags]`. It uses the same
// configuration as the other commands, so in a pod (the image has no shell):
// kubectl exec deploy/open-mcp-gateway -- /open-mcp-gateway doctor
func runDoctor(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	mode := fs.String("mode", "serve", "process mode whose configuration is validated: serve or migrate")
	timeout := fs.Duration("timeout", 10*time.Second, "time limit of each check")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: open-mcp-gateway doctor [--json] [--mode serve|migrate] [--timeout 10s]\n\n"+
			"Checks the configuration (including the production safety checks), then\n"+
			"PostgreSQL and the migration state. Exits 1 if any check fails.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if *mode != app.ModeServe && *mode != app.ModeMigrate {
		return fmt.Errorf("--mode must be serve or migrate")
	}
	var rep doctor.Report
	if cfg, err := config.Load(); err != nil {
		rep = doctor.ConfigLoadFailed(*mode, err)
	} else {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		rep = doctor.Run(ctx, cfg, doctor.Options{Mode: *mode, Timeout: *timeout})
	}
	write := rep.WriteText
	if *asJSON {
		write = rep.WriteJSON
	}
	if err := write(stdout); err != nil {
		return err
	}
	if !rep.OK {
		return errChecksFailed
	}
	return nil
}
