package db

import (
	"strconv"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// Config configures the two connection pools. It is loaded with package
// config, for example config.FromEnv("PORTUN_DB_", &cfg). Zero values of the
// optional fields take the defaults in the env tags, so a Config built in code
// only needs the two URLs.
type Config struct {
	// AppURL connects as the application role (portun_app): no superuser, no
	// BYPASSRLS, owns nothing. Every tenant query runs through it.
	AppURL secret.String `env:"APP_URL" required:"true"`
	// PlatformURL connects as the platform role (portun_platform), which has
	// BYPASSRLS, for platform-level work only (ADR-0002).
	PlatformURL secret.String `env:"PLATFORM_URL" required:"true"`
	// MaxConns is the application pool size.
	MaxConns int32 `env:"MAX_CONNS" default:"20"`
	// PlatformMaxConns is the platform pool size.
	PlatformMaxConns int32 `env:"PLATFORM_MAX_CONNS" default:"5"`
	// ConnectTimeout bounds establishing one connection.
	ConnectTimeout time.Duration `env:"CONNECT_TIMEOUT" default:"5s"`
	// StatementTimeout is the server-side statement_timeout of every
	// connection. It always applies: a statement can never run unbounded.
	StatementTimeout time.Duration `env:"STATEMENT_TIMEOUT" default:"30s"`
	// IdleInTransactionTimeout is the server-side
	// idle_in_transaction_session_timeout of every connection.
	IdleInTransactionTimeout time.Duration `env:"IDLE_IN_TRANSACTION_TIMEOUT" default:"60s"`
	// MaxConnLifetime recycles connections after this age.
	MaxConnLifetime time.Duration `env:"MAX_CONN_LIFETIME" default:"1h"`
	// MaxConnIdleTime closes connections idle for this long.
	MaxConnIdleTime time.Duration `env:"MAX_CONN_IDLE_TIME" default:"10m"`
	// ApplicationName is reported to the server (pg_stat_activity) unless the
	// URL sets application_name itself.
	ApplicationName string `env:"APPLICATION_NAME" default:"portun"`
}

// Defaults of the optional Config fields. They repeat the env tags; a test
// keeps both in step.
const (
	defaultMaxConns                 int32 = 20
	defaultPlatformMaxConns         int32 = 5
	defaultConnectTimeout                 = 5 * time.Second
	defaultStatementTimeout               = 30 * time.Second
	defaultIdleInTransactionTimeout       = 60 * time.Second
	defaultMaxConnLifetime                = time.Hour
	defaultMaxConnIdleTime                = 10 * time.Minute
	defaultApplicationName                = "portun"
)

// withDefaults returns c with every zero optional field set to its default.
func (c Config) withDefaults() Config {
	setDefault(&c.MaxConns, defaultMaxConns)
	setDefault(&c.PlatformMaxConns, defaultPlatformMaxConns)
	setDefault(&c.ConnectTimeout, defaultConnectTimeout)
	setDefault(&c.StatementTimeout, defaultStatementTimeout)
	setDefault(&c.IdleInTransactionTimeout, defaultIdleInTransactionTimeout)
	setDefault(&c.MaxConnLifetime, defaultMaxConnLifetime)
	setDefault(&c.MaxConnIdleTime, defaultMaxConnIdleTime)
	setDefault(&c.ApplicationName, defaultApplicationName)
	return c
}

func setDefault[T comparable](field *T, def T) {
	var zero T
	if *field == zero {
		*field = def
	}
}

// Validate reports every invalid field at once. Zero optional fields are
// valid: they take their defaults.
func (c Config) Validate() error {
	var fields []errs.FieldError
	if c.AppURL.IsEmpty() {
		fields = append(fields, fieldError("app_url", "required", "the application database URL is required"))
	}
	if c.PlatformURL.IsEmpty() {
		fields = append(fields, fieldError("platform_url", "required", "the platform database URL is required"))
	}
	if c.MaxConns < 0 {
		fields = append(fields, fieldError("max_conns", "out_of_range", "must not be negative"))
	}
	if c.PlatformMaxConns < 0 {
		fields = append(fields, fieldError("platform_max_conns", "out_of_range", "must not be negative"))
	}
	for _, d := range []struct {
		name  string
		value time.Duration
	}{
		{"connect_timeout", c.ConnectTimeout},
		{"statement_timeout", c.StatementTimeout},
		{"idle_in_transaction_timeout", c.IdleInTransactionTimeout},
		{"max_conn_lifetime", c.MaxConnLifetime},
		{"max_conn_idle_time", c.MaxConnIdleTime},
	} {
		if d.value < 0 {
			fields = append(fields, fieldError(d.name, "out_of_range", "must not be negative"))
		}
	}
	if len(fields) > 0 {
		return errs.New(errs.CodeInvalidRequest, "invalid database configuration").WithFields(fields...) //nolint:wrapcheck // errs constructor
	}
	return nil
}

func fieldError(name, code, detail string) errs.FieldError {
	return errs.FieldError{Pointer: "/" + name, Code: code, Detail: detail}
}

// milliseconds formats d for a PostgreSQL time setting given in milliseconds.
func milliseconds(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10)
}
