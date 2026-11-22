package bus_test

import (
	"strings"
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/platform/bus"
	"github.com/antonyrejith/portun/internal/platform/config"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

func mustTenant(t *testing.T) ids.TenantID {
	t.Helper()
	id, err := ids.New[ids.TenantKind]()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSubjectHelpers(t *testing.T) {
	t.Parallel()
	tenant := mustTenant(t)
	env, err := ids.New[ids.EnvironmentKind]()
	if err != nil {
		t.Fatal(err)
	}

	got, err := bus.AuditSubject(tenant)
	if err != nil || got != "audit."+tenant.String() {
		t.Fatalf("AuditSubject = %q, %v", got, err)
	}
	got, err = bus.ConfigSubject(tenant, env)
	if err != nil || got != "config."+tenant.String()+"."+env.String() {
		t.Fatalf("ConfigSubject = %q, %v", got, err)
	}
	got, err = bus.EventSubject("identity", "user_created")
	if err != nil || got != "evt.identity.user_created" {
		t.Fatalf("EventSubject = %q, %v", got, err)
	}
	if strings.Contains(bus.AuditPlatformSubject, "_") {
		t.Fatal("platform subject must not look like a tenant TypeID")
	}

	if _, err := bus.AuditSubject(ids.TenantID{}); !errs.Is(err, errs.CodeInvalidRequest) {
		t.Fatalf("AuditSubject(zero) error = %v", err)
	}
	if _, err := bus.ConfigSubject(tenant, ids.EnvironmentID{}); !errs.Is(err, errs.CodeInvalidRequest) {
		t.Fatalf("ConfigSubject(zero env) error = %v", err)
	}
	if _, err := bus.ConfigSubject(ids.TenantID{}, env); !errs.Is(err, errs.CodeInvalidRequest) {
		t.Fatalf("ConfigSubject(zero tenant) error = %v", err)
	}
}

func TestValidateToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		token string
		ok    bool
	}{
		{"identity", true},
		{"user_created", true},
		{"v1-beta", true},
		{"", false},
		{"a.b", false},
		{"*", false},
		{"a>", false},
		{"a b", false},
		{"a\tb", false},
		{"a\nb", false},
		{"ünicode", false},
		{"a\x00b", false},
		{strings.Repeat("a", 129), false},
	}
	for _, tt := range tests {
		t.Run(tt.token, func(t *testing.T) {
			t.Parallel()
			err := bus.ValidateToken(tt.token)
			if tt.ok != (err == nil) {
				t.Fatalf("ValidateToken(%q) = %v; want ok=%v", tt.token, err, tt.ok)
			}
			if err != nil && !errs.Is(err, errs.CodeInvalidRequest) {
				t.Fatalf("ValidateToken(%q) code = %s", tt.token, errs.CodeOf(err))
			}
			_, evtErr := bus.EventSubject("module", tt.token)
			if tt.ok != (evtErr == nil) {
				t.Fatalf("EventSubject(module, %q) = %v; want ok=%v", tt.token, evtErr, tt.ok)
			}
		})
	}
}

func TestConfigFromEnvironment(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"PORTUN_BUS_URL":                "nats://a:4222,nats://b:4222",
		"PORTUN_BUS_CREDS":              "secret-token",
		"PORTUN_BUS_REPLICAS":           "3",
		"PORTUN_BUS_EMBEDDED_STORE_DIR": "/var/lib/portun/nats",
		"PORTUN_BUS_AUDIT_MAX_AGE":      "720h",
	}
	var cfg bus.Config
	err := config.Load("PORTUN_BUS_", &cfg, func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		ok   bool
	}{
		{"url", cfg.URL == "nats://a:4222,nats://b:4222"},
		{"creds", cfg.Credentials.Reveal() == "secret-token" && cfg.Credentials.String() == "[REDACTED]"},
		{"replicas", cfg.Replicas == 3},
		{"name default", cfg.Name == "portun"},
		{"duplicate window default", cfg.DuplicateWindow == 5*time.Minute},
		{"request timeout default", cfg.RequestTimeout == 5*time.Second},
		{"audit max age", cfg.AuditMaxAge == 720*time.Hour},
		{"events max age default", cfg.EventsMaxAge == 168*time.Hour},
		{"config max age default", cfg.ConfigMaxAge == 24*time.Hour},
		{"embedded store dir", cfg.Embedded.StoreDir == "/var/lib/portun/nats"},
		{"embedded host default", cfg.Embedded.Host == "127.0.0.1"},
		{"embedded port default", cfg.Embedded.Port == -1},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s: unexpected config %+v", c.name, cfg)
		}
	}
}
