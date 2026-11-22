package bus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats-server/v2/server"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// EmbeddedConfig configures the in-process NATS server used by the
// all-in-one binary and by tests.
type EmbeddedConfig struct {
	// StoreDir holds JetStream's file storage. Required: the data must
	// survive restarts, so there is no temporary default.
	StoreDir string `env:"STORE_DIR"`
	// Host is the listen address. Empty means 127.0.0.1. It must be a
	// loopback address or "localhost": the embedded server does not support
	// TLS, so it never listens where other hosts could reach it in clear text.
	Host string `env:"HOST" default:"127.0.0.1"`
	// Port is the client port; -1 (and 0) pick a random free port.
	Port int `env:"PORT" default:"-1"`
	// ServerName names the server in logs and JetStream metadata.
	ServerName string `env:"SERVER_NAME" default:"portun-embedded"`
	// Token is the client authentication token. Every client, including the
	// Bus's own in-process connection, must present it. Empty generates a
	// random token per start.
	Token secret.String `env:"TOKEN"`
	// MaxStore limits JetStream file storage in bytes; 0 uses the server default.
	MaxStore int64 `env:"MAX_STORE"`
	// ReadyTimeout bounds the start, including JetStream recovery of the store.
	ReadyTimeout time.Duration `env:"READY_TIMEOUT" default:"30s"`
}

const (
	defaultEmbeddedHost  = "127.0.0.1"
	defaultEmbeddedName  = "portun-embedded"
	defaultReadyTimeout  = 30 * time.Second
	embeddedTokenEntropy = 32
)

func (c EmbeddedConfig) withDefaults() EmbeddedConfig {
	if c.Host == "" {
		c.Host = defaultEmbeddedHost
	}
	if c.Port == 0 {
		c.Port = server.RANDOM_PORT
	}
	if c.ServerName == "" {
		c.ServerName = defaultEmbeddedName
	}
	if c.ReadyTimeout <= 0 {
		c.ReadyTimeout = defaultReadyTimeout
	}
	return c
}

// EmbeddedServer is a NATS server with JetStream running in this process.
type EmbeddedServer struct {
	ns       *server.Server
	token    secret.String
	logger   *slog.Logger
	shutdown sync.Once
}

// StartEmbedded starts a NATS server with JetStream enabled and file storage
// in cfg.StoreDir, listening on the loopback address cfg.Host, and returns
// once it accepts connections. Clients authenticate with the server's token
// (EmbeddedServer.Token). A non-loopback Host is refused.
func StartEmbedded(ctx context.Context, cfg EmbeddedConfig, logger *slog.Logger) (*EmbeddedServer, error) {
	return startEmbedded(ctx, cfg, logger, false)
}

// isLoopback reports whether host is "localhost" or a loopback IP address.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && a.IsLoopback()
}

// startEmbedded starts the server; with dontListen it opens no TCP listener
// and accepts in-process connections only.
func startEmbedded(ctx context.Context, cfg EmbeddedConfig, logger *slog.Logger, dontListen bool) (*EmbeddedServer, error) {
	cfg = cfg.withDefaults()
	if !isLoopback(cfg.Host) {
		return nil, errs.New(errs.CodeInvalidRequest,
			"embedded bus: host must be a loopback address; the embedded server has no TLS and must not be reachable from other hosts")
	}
	logger = orDiscard(logger).With(slog.String("component", "nats_server"))
	if cfg.StoreDir == "" {
		return nil, errs.New(errs.CodeInvalidRequest, "embedded bus: store directory is required")
	}
	if cfg.MaxStore < 0 {
		return nil, errs.New(errs.CodeInvalidRequest, "embedded bus: max store must not be negative")
	}
	token := cfg.Token
	if token.IsEmpty() {
		b := make([]byte, embeddedTokenEntropy)
		if _, err := rand.Read(b); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "embedded bus: generate token")
		}
		token = secret.NewString(hex.EncodeToString(b))
	}
	ns, err := server.NewServer(&server.Options{
		ServerName:             cfg.ServerName,
		Host:                   cfg.Host,
		Port:                   cfg.Port,
		JetStream:              true,
		StoreDir:               cfg.StoreDir,
		JetStreamMaxStore:      cfg.MaxStore,
		Authorization:          token.Reveal(),
		NoSigs:                 true,
		DisableJetStreamBanner: true,
		DontListen:             dontListen,
	})
	if err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "embedded bus: invalid server options")
	}
	ns.SetLoggerV2(serverLogger{l: logger}, false, false, false)
	ns.Start()

	wait := cfg.ReadyTimeout
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
		wait = time.Until(dl)
	}
	if !ns.ReadyForConnections(wait) || !ns.JetStreamEnabled() {
		ns.Shutdown()
		ns.WaitForShutdown()
		return nil, errs.New(errs.CodeUnavailable, "embedded bus: server did not become ready")
	}
	clientURL := ns.ClientURL()
	if dontListen {
		clientURL = "in-process"
	}
	logger.InfoContext(ctx, "embedded nats server started", slog.String("client_url", clientURL), slog.String("store_dir", cfg.StoreDir))
	return &EmbeddedServer{ns: ns, token: token, logger: logger}, nil
}

// ClientURL returns the URL clients connect to.
func (e *EmbeddedServer) ClientURL() string { return e.ns.ClientURL() }

// Token returns the token clients must present (Config.Credentials).
func (e *EmbeddedServer) Token() secret.String { return e.token }

// Shutdown stops the server and waits until it has stopped. It is safe to
// call more than once.
func (e *EmbeddedServer) Shutdown() {
	e.shutdown.Do(func() {
		e.ns.Shutdown()
		e.ns.WaitForShutdown()
		e.logger.Info("embedded nats server stopped")
	})
}

// serverLogger routes the server's log output to slog. Fatal messages are
// logged as errors; they never exit the process.
type serverLogger struct{ l *slog.Logger }

func (s serverLogger) log(level slog.Level, format string, v ...any) {
	s.l.LogAttrs(context.Background(), level, fmt.Sprintf(format, v...))
}

func (s serverLogger) Noticef(format string, v ...any) { s.log(slog.LevelInfo, format, v...) }
func (s serverLogger) Warnf(format string, v ...any)   { s.log(slog.LevelWarn, format, v...) }
func (s serverLogger) Fatalf(format string, v ...any)  { s.log(slog.LevelError, format, v...) }
func (s serverLogger) Errorf(format string, v ...any)  { s.log(slog.LevelError, format, v...) }
func (s serverLogger) Debugf(format string, v ...any)  { s.log(slog.LevelDebug, format, v...) }
func (s serverLogger) Tracef(format string, v ...any)  { s.log(slog.LevelDebug, format, v...) }

func orDiscard(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.New(slog.DiscardHandler)
	}
	return l
}
