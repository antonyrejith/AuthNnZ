package httpx

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/antonyrejith/portun/internal/platform/config"
)

// TestServerSecureDefaults checks that every timeout and the header limit are
// set, from the zero config and from configuration loading.
//
// Covers: SEC-09
func TestServerSecureDefaults(t *testing.T) {
	t.Parallel()
	var loaded ServerConfig
	if err := config.Load("HTTP_", &loaded, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]ServerConfig{"zero": {}, "loaded": loaded, "negative": {ReadTimeout: -1, MaxHeaderBytes: -5}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := NewServer(cfg, http.NotFoundHandler(), slog.New(slog.DiscardHandler))
			hs := s.srv
			if hs.ReadHeaderTimeout <= 0 || hs.ReadTimeout <= 0 || hs.WriteTimeout <= 0 || hs.IdleTimeout <= 0 || hs.MaxHeaderBytes <= 0 {
				t.Fatalf("unset limits: %+v", hs)
			}
			if hs.ReadHeaderTimeout != DefaultReadHeaderTimeout || hs.MaxHeaderBytes != DefaultMaxHeaderBytes || s.cfg.ShutdownTimeout != DefaultShutdownTimeout {
				t.Fatalf("defaults not applied: %+v", hs)
			}
			// TLS 1.3 is the minimum unless 1.2 is configured explicitly.
			s.cfg.TLS = &tls.Config{}
			tc, err := s.cfg.tlsConfig()
			if err != nil || tc.MinVersion != tls.VersionTLS13 {
				t.Fatalf("default TLS minimum = %x (err %v), want TLS 1.3", tc.MinVersion, err)
			}
		})
	}
}

// TestServerTLSConfig checks the TLS minimum version rules: TLS 1.3 by
// default, TLS 1.2 only when configured.
//
// Covers: SEC-08
func TestServerTLSConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     ServerConfig
		wantMin uint16
		wantNil bool
		wantErr bool
	}{
		{"off", ServerConfig{TLSMinVersion: "1.2"}, 0, true, false},
		{"default is tls13", ServerConfig{}.withDefaults(), 0, true, false},
		{"default raises tls12 config", ServerConfig{TLS: &tls.Config{MinVersion: tls.VersionTLS12}}.withDefaults(), tls.VersionTLS13, false, false},
		{"default cert files", ServerConfig{TLSCertFile: "c.pem", TLSKeyFile: "k.pem"}.withDefaults(), tls.VersionTLS13, false, false},
		{"raises weak min", ServerConfig{TLSMinVersion: "1.2", TLS: &tls.Config{MinVersion: tls.VersionTLS10}}, tls.VersionTLS12, false, false},
		{"tls13 only", ServerConfig{TLSMinVersion: "1.3", TLS: &tls.Config{}}, tls.VersionTLS13, false, false},
		{"cert files", ServerConfig{TLSMinVersion: "1.2", TLSCertFile: "c.pem", TLSKeyFile: "k.pem"}, tls.VersionTLS12, false, false},
		{"bad version", ServerConfig{TLSMinVersion: "1.0", TLS: &tls.Config{}}, 0, false, true},
		{"cert without key", ServerConfig{TLSMinVersion: "1.2", TLSCertFile: "c.pem"}, 0, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.cfg.tlsConfig()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr {
				return
			}
			if (got == nil) != tt.wantNil {
				t.Fatalf("config = %v", got)
			}
			if got != nil && got.MinVersion != tt.wantMin {
				t.Fatalf("min version = %x, want %x", got.MinVersion, tt.wantMin)
			}
		})
	}
}

// TestServerGracefulShutdown checks that an in-flight request completes after
// shutdown starts and that new connections are refused.
func TestServerGracefulShutdown(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "done")
	})
	s := NewServer(ServerConfig{ShutdownTimeout: 10 * time.Second}, h, slog.New(slog.DiscardHandler))
	shutdownStarted := make(chan struct{})
	s.srv.RegisterOnShutdown(func() { close(shutdownStarted) })
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ctx, cancel := context.WithCancel(t.Context())
	serveErr := make(chan error, 1)
	go func() { serveErr <- s.Serve(ctx, ln) }()

	type result struct {
		body string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		resCh <- result{body: string(b), err: err}
	}()
	<-started
	cancel()
	<-shutdownStarted // listeners are closed before shutdown hooks run

	if conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", addr); err == nil {
		_ = conn.Close()
		t.Fatal("new connection accepted after shutdown began")
	}
	close(release)
	if res := <-resCh; res.err != nil || res.body != "done" {
		t.Fatalf("in-flight request: %q %v", res.body, res.err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

// TestServerShutdownTimeout checks that a request outliving the shutdown
// timeout is cut off and reported.
func TestServerShutdownTimeout(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	h := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	s := NewServer(ServerConfig{ShutdownTimeout: 50 * time.Millisecond}, h, slog.New(slog.DiscardHandler))
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	serveErr := make(chan error, 1)
	go func() { serveErr <- s.Serve(ctx, ln) }()
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	cancel()
	if err := <-serveErr; err == nil {
		t.Fatal("expected a shutdown timeout error")
	}
}

func TestServerRunInvalidAddr(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{Addr: "256.0.0.1:99999"}, http.NotFoundHandler(), slog.New(slog.DiscardHandler))
	if err := s.Run(t.Context()); err == nil {
		t.Fatal("expected listen error")
	}
}
