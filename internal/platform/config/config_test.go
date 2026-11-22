package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/antonyrejith/portun/internal/platform/config"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

type dbConfig struct {
	URL      string        `env:"URL" required:"true"`
	MaxConns int32         `env:"MAX_CONNS" default:"10"`
	Password secret.String `env:"PASSWORD"`
}

type appConfig struct {
	HTTPAddr  string        `env:"HTTP_ADDR" default:":8080"`
	Debug     bool          `env:"DEBUG"`
	Timeout   time.Duration `env:"TIMEOUT" default:"5s"`
	Ratio     float64       `env:"RATIO" default:"0.5"`
	Port      uint16        `env:"PORT" default:"9090"`
	Hosts     []string      `env:"HOSTS"`
	Tenant    ids.TenantID  `env:"TENANT"`
	DB        dbConfig      `envPrefix:"DB_"`
	untouched string
	NoTag     string
}

func lookupFrom(m map[string]string) config.LookupFunc {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"PORTUN_DEBUG":       "true",
		"PORTUN_TIMEOUT":     "250ms",
		"PORTUN_HOSTS":       " a.example , b.example ,,",
		"PORTUN_TENANT":      "ten_01h455vb4pex5vsknk084sn02q",
		"PORTUN_DB_URL":      "postgres://db/portun",
		"PORTUN_DB_PASSWORD": "pw",
		"PORTUN_HTTP_ADDR":   "", // empty means unset: the default applies
	}
	got := appConfig{untouched: "kept"}
	if err := config.Load("PORTUN_", &got, lookupFrom(env)); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.untouched != "kept" {
		t.Fatalf("unexported field changed to %q", got.untouched)
	}
	tenant, err := ids.Parse[ids.TenantKind]("ten_01h455vb4pex5vsknk084sn02q")
	if err != nil {
		t.Fatal(err)
	}
	want := appConfig{
		HTTPAddr: ":8080",
		Debug:    true,
		Timeout:  250 * time.Millisecond,
		Ratio:    0.5,
		Port:     9090,
		Hosts:    []string{"a.example", "b.example"},
		Tenant:   tenant,
		DB:       dbConfig{URL: "postgres://db/portun", MaxConns: 10, Password: secret.NewString("pw")},
	}
	opts := cmp.Options{
		cmpopts.IgnoreUnexported(appConfig{}),
		cmp.Comparer(func(a, b secret.String) bool { return a.Reveal() == b.Reveal() }),
		cmp.Comparer(func(a, b ids.TenantID) bool { return a == b }),
	}
	if diff := cmp.Diff(want, got, opts); diff != "" {
		t.Fatalf("Load() mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadReportsAllProblems(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"P_DEBUG":        "maybe",
		"P_TIMEOUT":      "soon",
		"P_PORT":         "70000",
		"P_TENANT":       "usr_01h455vb4pex5vsknk084sn02q",
		"P_DB_MAX_CONNS": "many",
	}
	var cfg appConfig
	err := config.Load("P_", &cfg, lookupFrom(env))
	if !errs.Is(err, errs.CodeInvalidRequest) {
		t.Fatalf("Load error = %v, want invalid_request", err)
	}
	for _, key := range []string{"P_DEBUG", "P_TIMEOUT", "P_PORT", "P_TENANT", "P_DB_URL is required", "P_DB_MAX_CONNS"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}

func TestLoadRejectsBadTargets(t *testing.T) {
	t.Parallel()
	var notStruct int
	var nilPtr *appConfig
	type badPrefix struct {
		X string `envPrefix:"X_"`
	}
	type badType struct {
		C chan int `env:"C" default:"1"`
	}
	type badSlice struct {
		N []int `env:"N" default:"1"`
	}
	tests := []struct {
		name   string
		target any
	}{
		{"not a pointer", appConfig{}},
		{"pointer to non-struct", &notStruct},
		{"nil pointer", nilPtr},
		{"envPrefix on non-struct", &badPrefix{}},
		{"unsupported type", &badType{}},
		{"unsupported slice", &badSlice{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := config.Load("", tt.target, lookupFrom(nil)); err == nil {
				t.Fatal("Load succeeded")
			}
		})
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("PORTUN_TEST_DB_URL", "postgres://env")
	var cfg appConfig
	if err := config.FromEnv("PORTUN_TEST_", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.DB.URL != "postgres://env" {
		t.Fatalf("DB.URL = %q", cfg.DB.URL)
	}
}
