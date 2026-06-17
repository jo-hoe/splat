package server

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jo-hoe/splat/internal/config"
	"github.com/jo-hoe/splat/internal/format"
	"github.com/jo-hoe/splat/internal/source"
	"github.com/jo-hoe/splat/internal/thumbs"
)

// Options configures a Server.
type Options struct {
	// Cfg is the loaded application configuration. Required.
	Cfg *config.Config
	// Source is the active image backend. Required.
	Source source.Source
	// Registry is the format-handler registry. Required.
	Registry *format.Registry
	// Thumbs is the on-disk thumbnail cache. Required.
	Thumbs *thumbs.Cache
	// Logger is used for access logs and panic reports. If nil, a default
	// JSON slog logger writing to os.Stdout is used.
	Logger *slog.Logger
}

// Server is the splat HTTP server. It owns the route mux, the HTML
// templates, the access-log middleware and the bundled static assets.
type Server struct {
	cfg       *config.Config
	source    source.Source
	registry  *format.Registry
	thumbs    *thumbs.Cache
	logger    *slog.Logger
	templates *template.Template
	handler   http.Handler
	srv       *http.Server
}

// New constructs a Server from the given options. It validates required
// fields, parses the embedded templates, and registers all routes. The
// returned Server is ready to serve via Start or via its Handler.
func New(opts Options) (*Server, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	tpl, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:       opts.Cfg,
		source:    opts.Source,
		registry:  opts.Registry,
		thumbs:    opts.Thumbs,
		logger:    logger,
		templates: tpl,
	}
	s.handler = s.buildHandler()
	return s, nil
}

// validateOptions enforces the non-nil contract on Options.
func validateOptions(opts Options) error {
	if opts.Cfg == nil {
		return errors.New("server: Options.Cfg is required")
	}
	if opts.Source == nil {
		return errors.New("server: Options.Source is required")
	}
	if opts.Registry == nil {
		return errors.New("server: Options.Registry is required")
	}
	if opts.Thumbs == nil {
		return errors.New("server: Options.Thumbs is required")
	}
	return nil
}

// buildHandler registers all routes on a fresh ServeMux and wraps the
// result in the request_id, access_log, and recover middleware.
func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /strip", s.handleStrip)
	mux.HandleFunc("GET /thumb/{key...}", s.handleThumb)
	mux.HandleFunc("GET /image/{key...}", s.handleImage)
	mux.HandleFunc("GET /editor/{key...}", s.handleEditor)
	mux.HandleFunc("POST /apply/{key...}", s.handleApply)
	mux.HandleFunc("DELETE /image/{key...}", s.handleDelete)

	staticSub, err := staticSubFS()
	if err == nil {
		mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticSub)))
	}

	return chain(mux,
		requestID,
		accessLog(s.logger),
		recoverPanic(s.logger),
	)
}

// Handler returns the wrapped HTTP handler for use in tests.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Start runs the HTTP server until ctx is cancelled. On cancellation it
// initiates a graceful shutdown bounded by Cfg.Server.ShutdownTimeout.
// It returns nil on clean shutdown and a non-nil error if the listener
// fails or the shutdown deadline elapses.
func (s *Server) Start(ctx context.Context) error {
	addr := net.JoinHostPort("", strconv.Itoa(s.cfg.Server.Port))
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("listening", "addr", addr)
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		return s.shutdown()
	case err := <-errCh:
		return err
	}
}

// shutdown stops the embedded http.Server, honoring the configured
// shutdown timeout.
func (s *Server) shutdown() error {
	timeout := s.cfg.Server.ShutdownTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("server: shutdown: %w", err)
	}
	return nil
}
