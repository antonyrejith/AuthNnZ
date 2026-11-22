package bus

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

const (
	tenantA = "ten_01h455vb4pex5vsknk084sn02q"
	envA    = "env_01h455vb4pex5vsknk084sn02q"
)

func TestStreamForSubject(t *testing.T) {
	t.Parallel()
	tests := []struct {
		subject string
		want    string
	}{
		{"audit." + tenantA, StreamAudit},
		{AuditPlatformSubject, StreamAudit},
		{"evt.identity.user_created", StreamEvents},
		{"config." + tenantA + "." + envA, StreamConfig},
		{"audit", ""},
		{"audit.tenant", ""},
		{"audit." + tenantA + ".x", ""},
		{"audit.*", ""},
		{"audit.>", ""},
		{"evt.identity", ""},
		{"evt.identity.a.b", ""},
		{"evt..x", ""},
		{"evt.a b.c", ""},
		{"config." + tenantA, ""},
		{"config." + envA + "." + tenantA, ""},
		{"other.a.b", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.subject, func(t *testing.T) {
			t.Parallel()
			got, err := streamForSubject(tt.subject)
			if tt.want == "" {
				if !errs.Is(err, errs.CodeInvalidRequest) {
					t.Fatalf("streamForSubject(%q) = %q, %v; want invalid_request", tt.subject, got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("streamForSubject(%q) = %q, %v; want %q", tt.subject, got, err, tt.want)
			}
		})
	}
}

func TestValidateFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		stream, filter string
		ok             bool
	}{
		{StreamAudit, "audit.>", true},
		{StreamAudit, "audit.*", true},
		{StreamAudit, "audit." + tenantA, true},
		{StreamEvents, "evt.identity.>", true},
		{StreamEvents, "evt.*.user_created", true},
		{StreamAudit, "evt.>", false},
		{StreamAudit, "audit", false},
		{StreamAudit, ">", false},
		{StreamEvents, "evt.>.x", false},
		{StreamEvents, "evt.a*.x", false},
		{StreamEvents, "evt..x", false},
	}
	for _, tt := range tests {
		t.Run(tt.stream+"/"+tt.filter, func(t *testing.T) {
			t.Parallel()
			err := validateFilter(tt.stream, tt.filter)
			if (err == nil) != tt.ok {
				t.Fatalf("validateFilter(%q, %q) = %v; want ok=%v", tt.stream, tt.filter, err, tt.ok)
			}
		})
	}
}

func TestMessageValidate(t *testing.T) {
	t.Parallel()
	subject := "audit." + tenantA
	tests := []struct {
		name string
		msg  Message
		ok   bool
	}{
		{"valid", Message{Subject: subject, ID: "msg-1", Header: map[string]string{"Traceparent": "00-x"}}, true},
		{"missing id", Message{Subject: subject}, false},
		{"id with newline", Message{Subject: subject, ID: "a\r\nb"}, false},
		{"bad subject", Message{Subject: "audit.*", ID: "x"}, false},
		{"reserved header", Message{Subject: subject, ID: "x", Header: map[string]string{"Nats-Expected-Stream": "EVENTS"}}, false},
		{"reserved header lower case", Message{Subject: subject, ID: "x", Header: map[string]string{"nats-msg-id": "y"}}, false},
		{"header name with colon", Message{Subject: subject, ID: "x", Header: map[string]string{"a:b": "y"}}, false},
		{"empty header name", Message{Subject: subject, ID: "x", Header: map[string]string{"": "y"}}, false},
		{"header value injection", Message{Subject: subject, ID: "x", Header: map[string]string{"A": "y\r\nNats-Msg-Id: z"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stream, err := tt.msg.validate()
			if tt.ok {
				if err != nil || stream != StreamAudit {
					t.Fatalf("validate() = %q, %v; want AUDIT", stream, err)
				}
				return
			}
			if !errs.Is(err, errs.CodeInvalidRequest) {
				t.Fatalf("validate() error = %v; want invalid_request", err)
			}
		})
	}
}

func TestPublishErrorMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		want errs.Code
	}{
		{context.DeadlineExceeded, errs.CodeDeadlineExceeded},
		{nats.ErrTimeout, errs.CodeDeadlineExceeded},
		{context.Canceled, errs.CodeUnavailable},
		{jetstream.ErrNoStreamResponse, errs.CodeUnavailable},
		{nats.ErrConnectionClosed, errs.CodeUnavailable},
		{nats.ErrConnectionReconnecting, errs.CodeUnavailable},
		{nats.ErrReconnectBufExceeded, errs.CodeUnavailable},
		{nats.ErrMaxPayload, errs.CodePayloadTooLarge},
		{fmt.Errorf("nats: %w", &jetstream.APIError{Code: 503, ErrorCode: 10023}), errs.CodeUnavailable},
		{fmt.Errorf("nats: %w", &jetstream.APIError{Code: 400, ErrorCode: 10060}), errs.CodeInternal},
		{errors.New("boom"), errs.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.err.Error(), func(t *testing.T) {
			t.Parallel()
			got := publishError(tt.err)
			if errs.CodeOf(got) != tt.want {
				t.Fatalf("publishError(%v) code = %s; want %s", tt.err, errs.CodeOf(got), tt.want)
			}
			if !errors.Is(got, tt.err) {
				t.Fatalf("publishError(%v) lost the cause", tt.err)
			}
		})
	}
}

// TestClassify checks how handler results settle messages: only ErrPoison
// terminates at once.
func TestClassify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		err        error
		attempt    uint64
		maxDeliver int
		want       outcome
	}{
		{"success", nil, 1, 0, outcomeAck},
		{"success on last attempt", nil, 3, 3, outcomeAck},
		{"poison", ErrPoison, 1, 0, outcomeTerm},
		{"wrapped poison", fmt.Errorf("decode: %w", ErrPoison), 1, 0, outcomeTerm},
		{"poison in errs", errs.Wrap(errs.CodeInternal, ErrPoison, "bad payload"), 1, 0, outcomeTerm},
		{"invalid request is not poison", errs.New(errs.CodeInvalidRequest, "bad payload"), 1, 0, outcomeRetry},
		{"wrapped invalid request is not poison", fmt.Errorf("call: %w", errs.New(errs.CodeInvalidRequest, "bad")), 1, 0, outcomeRetry},
		{"transient unlimited", errs.New(errs.CodeUnavailable, "db down"), 100, 0, outcomeRetry},
		{"transient below max", errors.New("x"), 2, 3, outcomeRetry},
		{"transient at max", errors.New("x"), 3, 3, outcomeTerm},
		{"validation failed is not poison", errs.New(errs.CodeValidationFailed, "x"), 1, 0, outcomeRetry},
		{"canceled", context.Canceled, 1, 0, outcomeRetry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, reason := classify(tt.err, tt.attempt, tt.maxDeliver)
			if got != tt.want {
				t.Fatalf("classify() = %v; want %v", got, tt.want)
			}
			if (got == outcomeTerm) != (reason != "") {
				t.Fatalf("classify() reason = %q for outcome %v", reason, got)
			}
		})
	}
}

func TestBackoffDelay(t *testing.T) {
	t.Parallel()
	b := []time.Duration{time.Second, 5 * time.Second, time.Minute}
	tests := []struct {
		attempt uint64
		want    time.Duration
	}{{0, time.Second}, {1, time.Second}, {2, 5 * time.Second}, {3, time.Minute}, {4, time.Minute}, {1 << 40, time.Minute}}
	for _, tt := range tests {
		if got := backoffDelay(b, tt.attempt); got != tt.want {
			t.Errorf("backoffDelay(%d) = %v; want %v", tt.attempt, got, tt.want)
		}
	}
	if got := backoffDelay(nil, 3); got != 0 {
		t.Errorf("backoffDelay(nil) = %v; want 0", got)
	}
}

func TestConsumerSpecNormalize(t *testing.T) {
	t.Parallel()
	got, err := ConsumerSpec{Stream: StreamAudit, Durable: "audit-writer"}.normalize()
	if err != nil {
		t.Fatal(err)
	}
	want := ConsumerSpec{
		Stream: StreamAudit, Durable: "audit-writer", Concurrency: defaultConcurrency,
		MaxAckPending: defaultMaxAckPending, AckWait: defaultAckWait, Backoff: defaultBackoff(),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("normalize() mismatch (-want +got):\n%s", diff)
	}
	cfg := ConsumerSpec{Stream: StreamEvents, Durable: "d", FilterSubjects: []string{"evt.a.>", "evt.b.>"}, DeliverPolicy: DeliverNew}.consumerConfig()
	if cfg.DeliverPolicy != jetstream.DeliverNewPolicy || len(cfg.FilterSubjects) != 2 || cfg.MaxDeliver != -1 || cfg.AckPolicy != jetstream.AckExplicitPolicy {
		t.Fatalf("consumerConfig() = %+v", cfg)
	}

	invalid := []ConsumerSpec{
		{Stream: "OTHER", Durable: "d"},
		{Stream: StreamAudit},
		{Stream: StreamAudit, Durable: "a.b"},
		{Stream: StreamAudit, Durable: "a/b"},
		{Stream: StreamAudit, Durable: "d", FilterSubjects: []string{"evt.>"}},
		{Stream: StreamAudit, Durable: "d", DeliverPolicy: DeliverPolicy(9)},
		{Stream: StreamAudit, Durable: "d", Ordering: Ordering(9)},
		{Stream: StreamAudit, Durable: "d", Concurrency: -1},
		{Stream: StreamAudit, Durable: "d", AckWait: 10 * time.Millisecond},
		{Stream: StreamAudit, Durable: "d", Backoff: []time.Duration{-time.Second}},
	}
	for i, s := range invalid {
		if _, err := s.normalize(); !errs.Is(err, errs.CodeInvalidRequest) {
			t.Errorf("case %d: normalize(%+v) error = %v; want invalid_request", i, s, err)
		}
	}
}

// TestConfigValidate checks the configuration rules, including that
// external servers require TLS unless insecure connections are explicitly
// allowed.
//
// Covers: SEC-08
func TestConfigValidate(t *testing.T) {
	t.Parallel()
	good := []Config{
		{}, // embedded
		{URL: "tls://x:4222"},
		{URL: "TLS://a:4222, wss://b:443"},
		{URL: "nats://x:4222", TLSCAFile: "ca.pem"},
		{URL: "nats://x:4222", TLS: &tls.Config{MinVersion: tls.VersionTLS13}},
		{URL: "nats://x:4222", AllowInsecure: true},
	}
	for i, c := range good {
		if err := c.withDefaults().validate(); err != nil {
			t.Errorf("good case %d: validate() = %v", i, err)
		}
	}
	bad := []Config{
		{Replicas: 3}, // embedded with replicas
		{URL: "tls://x", Replicas: 9},
		{EventsMaxAge: -time.Second},
		{RequestTimeout: -time.Second},
		{URL: "nats://x:4222"},              // plaintext
		{URL: "x:4222"},                     // no scheme means nats://
		{URL: "tls://a:4222,nats://b:4222"}, // one plaintext server
		{URL: "ws://x:80"},
	}
	for i, c := range bad {
		if err := c.withDefaults().validate(); !errs.Is(err, errs.CodeInvalidRequest) {
			t.Errorf("case %d: validate() = %v; want invalid_request", i, err)
		}
	}
}

// TestEmbeddedRefusesNonLoopbackHost checks that the embedded server, which
// has no TLS, only listens on loopback addresses.
//
// Covers: SEC-08
func TestEmbeddedRefusesNonLoopbackHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		host     string
		loopback bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"::1", true},
		{"[::1]", true},
		{"localhost", true},
		{"0.0.0.0", false},
		{"::", false},
		{"10.0.0.5", false},
		{"bus.example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			t.Parallel()
			if got := isLoopback(tt.host); got != tt.loopback {
				t.Fatalf("isLoopback(%q) = %v, want %v", tt.host, got, tt.loopback)
			}
			if tt.loopback {
				return
			}
			_, err := StartEmbedded(t.Context(), EmbeddedConfig{StoreDir: t.TempDir(), Host: tt.host}, nil)
			if !errs.Is(err, errs.CodeInvalidRequest) {
				t.Fatalf("StartEmbedded on %q = %v; want invalid_request", tt.host, err)
			}
		})
	}
}

func TestAuthOptions(t *testing.T) {
	t.Parallel()
	if _, err := authOptions(Config{}); err != nil {
		t.Fatalf("no credentials: %v", err)
	}
	opts, err := authOptions(Config{TLSCAFile: "ca.pem"})
	if err != nil || len(opts) != 1 {
		t.Fatalf("CA only: %d options, %v", len(opts), err)
	}
	opts, err = authOptions(Config{TLS: &tls.Config{MinVersion: tls.VersionTLS13}})
	if err != nil || len(opts) != 1 {
		t.Fatalf("TLS config only: %d options, %v", len(opts), err)
	}
	broken := "-----BEGIN NATS USER JWT-----\nnot-a-jwt\n------END NATS USER JWT------\n"
	cfg := Config{}
	if err := cfg.Credentials.UnmarshalText([]byte(broken)); err != nil {
		t.Fatal(err)
	}
	if _, err := authOptions(cfg); !errs.Is(err, errs.CodeInvalidRequest) {
		t.Fatalf("broken creds error = %v; want invalid_request", err)
	}
}

func TestUserHeaders(t *testing.T) {
	t.Parallel()
	h := nats.Header{}
	h.Set("Nats-Msg-Id", "x")
	h.Set("Traceparent", "00-abc")
	h["Empty"] = nil
	got := userHeaders(h)
	if diff := cmp.Diff(map[string]string{"Traceparent": "00-abc"}, got); diff != "" {
		t.Fatalf("userHeaders mismatch (-want +got):\n%s", diff)
	}
}
