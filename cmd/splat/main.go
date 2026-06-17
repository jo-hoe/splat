// Package main is the splat entry point.
//
// It dispatches between the default "serve" mode (run the HTTP server) and
// the "healthcheck" subcommand (issue an HTTP GET against /healthz on the
// configured port). The healthcheck subcommand exists so a distroless
// runtime image can probe the running server without curl/wget.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jo-hoe/splat/internal/config"
	"github.com/jo-hoe/splat/internal/format"
	"github.com/jo-hoe/splat/internal/server"
	"github.com/jo-hoe/splat/internal/source"
	"github.com/jo-hoe/splat/internal/thumbs"
)

// defaultConfigPath is the fallback location for the config file when neither
// the --config flag nor the SPLAT_CONFIG env var is set.
const defaultConfigPath = "./config.yaml"

// configEnvVar is the env var consulted when --config is not provided.
const configEnvVar = "SPLAT_CONFIG"

// healthcheckTimeout bounds the local HTTP probe issued by `splat healthcheck`.
const healthcheckTimeout = 2 * time.Second

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "splat: %v\n", err)
		os.Exit(1)
	}
}

// run dispatches subcommands. It must not call os.Exit directly; main exits
// on the returned error.
func run(args []string) error {
	if len(args) > 0 && args[0] == "healthcheck" {
		return runHealthcheck(args[1:])
	}
	return runServe(args)
}

// runServe parses serve-mode flags, builds every dependency, installs a
// signal-driven shutdown context, and runs the HTTP server until shutdown.
func runServe(args []string) error {
	flagPath, err := parseConfigFlag("splat", args)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfgPath := resolveConfigPath(flagPath)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Error("load config", "path", cfgPath, "err", err)
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := buildServer(ctx, cfg, logger)
	if err != nil {
		logger.Error("build server", "err", err)
		return err
	}

	if err := srv.Start(ctx); err != nil {
		logger.Error("server stopped", "err", err)
		return err
	}
	logger.Info("server stopped cleanly")
	return nil
}

// buildServer wires every dependency for serve mode: source, format
// registry, thumbnail cache, HTTP server. Split out of runServe so each
// function stays well below the cyclomatic complexity ceiling.
func buildServer(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*server.Server, error) {
	src, err := buildSource(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build source: %w", err)
	}

	registry, err := format.NewRegistry(cfg.Editing.JPEGQuality)
	if err != nil {
		return nil, fmt.Errorf("build format registry: %w", err)
	}

	cache, err := thumbs.New(thumbs.Options{
		Dir:      cfg.Thumbnails.CacheDir,
		HeightPx: cfg.Thumbnails.HeightPx,
		MaxBytes: cfg.Thumbnails.CacheMaxBytes,
		SourceID: sourceID(cfg),
	})
	if err != nil {
		return nil, fmt.Errorf("build thumbnail cache: %w", err)
	}

	srv, err := server.New(server.Options{
		Cfg:      cfg,
		Source:   src,
		Registry: registry,
		Thumbs:   cache,
		Logger:   logger,
	})
	if err != nil {
		return nil, fmt.Errorf("build server: %w", err)
	}
	return srv, nil
}

// runHealthcheck issues a 2-second-bounded HTTP GET against
// http://127.0.0.1:<port>/healthz and returns nil on 200, non-nil otherwise.
// It does not start the server; it is purely an HTTP client.
func runHealthcheck(args []string) error {
	flagPath, err := parseConfigFlag("splat healthcheck", args)
	if err != nil {
		return err
	}
	cfgPath := resolveConfigPath(flagPath)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
	defer cancel()

	url := "http://127.0.0.1:" + strconv.Itoa(cfg.Server.Port) + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	client := &http.Client{Timeout: healthcheckTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	// Drain body so the connection can be reused (immaterial for a
	// one-shot probe but cheap and idiomatic).
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d from %s", resp.StatusCode, url)
	}
	return nil
}

// parseConfigFlag parses a single --config flag from args using a fresh
// FlagSet so subcommands can call it independently. Errors during parse
// are returned (the FlagSet is configured to ContinueOnError and to
// suppress its auto-printed usage; main owns stderr formatting).
func parseConfigFlag(name string, args []string) (string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var path string
	fs.StringVar(&path, "config", "", "path to YAML config file")
	if err := fs.Parse(args); err != nil {
		return "", fmt.Errorf("parse flags: %w", err)
	}
	return path, nil
}

// resolveConfigPath implements the --config → $SPLAT_CONFIG → ./config.yaml
// fallback chain. The flag wins; an empty flag falls through to the env var;
// an unset env var falls through to the default path.
func resolveConfigPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv(configEnvVar); env != "" {
		return env
	}
	return defaultConfigPath
}

// buildSource constructs the appropriate Source implementation for cfg.
// It type-dispatches on cfg.Source.Type; config.Validate has already ensured
// the matching sub-struct is populated.
func buildSource(ctx context.Context, cfg *config.Config) (source.Source, error) {
	switch cfg.Source.Type {
	case config.SourceLocal:
		return source.NewLocalSource(cfg.Source.Local.Root)
	case config.SourceS3:
		s := cfg.Source.S3
		return source.NewS3Source(ctx, source.S3Options{
			Bucket:          s.Bucket,
			Prefix:          s.Prefix,
			Region:          s.Region,
			AccessKey:       s.AccessKey,
			SecretAccessKey: s.SecretAccessKey,
		})
	default:
		return nil, fmt.Errorf("unsupported source type %q", cfg.Source.Type)
	}
}

// sourceID returns a stable identifier for the configured source, used by
// the thumbnail cache to namespace on-disk entries across deployments that
// happen to share a cache_dir.
func sourceID(cfg *config.Config) string {
	switch cfg.Source.Type {
	case config.SourceLocal:
		return "local:" + cfg.Source.Local.Root
	case config.SourceS3:
		return "s3:" + cfg.Source.S3.Bucket + "/" + cfg.Source.S3.Prefix
	default:
		return string(cfg.Source.Type)
	}
}
