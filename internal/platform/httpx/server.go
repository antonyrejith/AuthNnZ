package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Server defaults. They protect against slow clients (Slowloris), oversized
// headers and idle connection exhaustion; ShutdownTimeout matches the drain
// window of the runtime contract (plan 12 §1.3).
const (
	DefaultAddr              = ":8080"
	DefaultReadHeaderTimeout = 5 * time.Second
	DefaultReadTimeout       = 30 * time.Second
	DefaultWriteTimeout      = 30 * time.Second
	DefaultIdleTimeout       = 120 * time.Second
	DefaultMaxHeaderBytes    = 64 << 10
	DefaultShutdownTimeout   = 25 * time.Second
)

// ServerConfig configures a Server. It can be loaded with package config;
// zero or negative values get the secure defaults above.
type ServerConfig struct {
	Addr              string        `env:"ADDR"                default:":8080"`
	ReadHeaderTimeout time.Duration `env:"READ_HEADER_TIMEOUT" default:"5s"`
	ReadTimeout       time.Duration `env:"READ_TIMEOUT"        default:"30s"`
	WriteTimeout      time.Duration `env:"WRITE_TIMEOUT"       default:"30s"`
	IdleTimeout       time.Duration `env:"IDLE_TIMEOUT"        default:"120s"`
	MaxHeaderBytes    int           `env:"MAX_HEADER_BYTES"    default:"65536"`
	ShutdownTimeout   time.Duration `env:"SHUTDOWN_TIMEOUT"    default:"25s"`
	// TLSCertFile and TLSKeyFile enable TLS from PEM files.
	TLSCertFile string `env:"TLS_CERT_FILE"`
	TLSKeyFile  string `env:"TLS_KEY_FILE"`
	// TLSMinVersion is "1.2" (default) or "1.3". Go negotiates TLS 1.3
	// whenever the client supports it.
	TLSMinVersion string `env:"TLS_MIN_VERSION" default:"1.2"`
	// TLS, if set, enables TLS with this configuration (certificates or
	// GetCertificate). Its MinVersion is raised to TLSMinVersion.
	TLS *tls.Config
}

func (c ServerConfig) withDefaults() ServerConfig {
	if c.Addr == "" {
		c.Addr = DefaultAddr
	}
	setDuration(&c.ReadHeaderTimeout, DefaultReadHeaderTimeout)
	setDuration(&c.ReadTimeout, DefaultReadTimeout)
	setDuration(&c.WriteTimeout, DefaultWriteTimeout)
	setDuration(&c.IdleTimeout, DefaultIdleTimeout)
	setDuration(&c.ShutdownTimeout, DefaultShutdownTimeout)
	if c.MaxHeaderBytes <= 0 {
		c.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
	if c.TLSMinVersion == "" {
		c.TLSMinVersion = "1.2"
	}
	return c
}

func setDuration(d *time.Duration, def time.Duration) {
	if *d <= 0 {
		*d = def
	}
}

// tlsConfig returns the TLS configuration, or nil when TLS is off.
func (c ServerConfig) tlsConfig() (*tls.Config, error) {
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return nil, errs.New(errs.CodeInvalidRequest, "TLS needs both a certificate file and a key file")
	}
	var minVersion uint16
	switch c.TLSMinVersion {
	case "1.2":
		minVersion = tls.VersionTLS12
	case "1.3":
		minVersion = tls.VersionTLS13
	default:
		return nil, errs.New(errs.CodeInvalidRequest, "TLS minimum version must be 1.2 or 1.3")
	}
	if c.TLS == nil && c.TLSCertFile == "" {
		return nil, nil //nolint:nilnil // nil config is the documented "TLS off"
	}
	cfg := &tls.Config{MinVersion: minVersion}
	if c.TLS != nil {
		cfg = c.TLS.Clone()
		cfg.MinVersion = max(cfg.MinVersion, minVersion)
	}
	return cfg, nil
}

// Server is an HTTP server with secure timeouts and graceful shutdown.
type Server struct {
	cfg    ServerConfig
	srv    *http.Server
	logger *slog.Logger
}

// NewServer returns a server for handler. Call Run or Serve once.
func NewServer(cfg ServerConfig, handler http.Handler, logger *slog.Logger) *Server {
	cfg = cfg.withDefaults()
	return &Server{
		cfg:    cfg,
		logger: logger,
		srv: &http.Server{
			Addr:              cfg.Addr,
			Handler:           handler,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			ReadTimeout:       cfg.ReadTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       cfg.IdleTimeout,
			MaxHeaderBytes:    cfg.MaxHeaderBytes,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		},
	}
}

// Run listens on the configured address and serves until ctx is canceled.
func (s *Server) Run(ctx context.Context) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Addr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve serves on ln until ctx is canceled, then shuts down gracefully: it
// stops accepting connections, lets in-flight requests finish within
// ShutdownTimeout, then cancels their contexts and closes what remains.
// Request contexts are not canceled by ctx itself, so in-flight work is not
// cut short when shutdown begins. Serve closes ln.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	tlsCfg, err := s.cfg.tlsConfig()
	if err != nil {
		_ = ln.Close()
		return err
	}
	baseCtx, cancelBase := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelBase()
	s.srv.BaseContext = func(net.Listener) context.Context { return baseCtx }
	s.srv.TLSConfig = tlsCfg

	errCh := make(chan error, 1)
	go func() {
		if tlsCfg != nil {
			errCh <- s.srv.ServeTLS(ln, s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
			return
		}
		errCh <- s.srv.Serve(ln)
	}()
	s.logger.LogAttrs(ctx, slog.LevelInfo, "http server listening",
		slog.String("addr", ln.Addr().String()), slog.Bool("tls", tlsCfg != nil))

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
	}

	s.logger.LogAttrs(ctx, slog.LevelInfo, "http server shutting down",
		slog.Duration("shutdown_timeout", s.cfg.ShutdownTimeout))
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.ShutdownTimeout)
	defer cancel()
	shutdownErr := s.srv.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		cancelBase()
		_ = s.srv.Close()
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve http: %w", err)
	}
	if shutdownErr != nil {
		return fmt.Errorf("graceful shutdown: %w", shutdownErr)
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "http server stopped")
	return nil
}
