package scope_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/scope"
)

// TestRequestLogValueOmitsPersonalData: logging a Request never writes the
// client IP or user agent (review finding R19).
func TestRequestLogValueOmitsPersonalData(t *testing.T) {
	t.Parallel()
	req := scope.Request{
		RequestID: "req_1", Channel: "api", ClientID: "cli_1", Operation: "users.create",
		ChangeTicket: "CHG-1", ChangeReason: "free text reason",
		IP: "203.0.113.7", UserAgent: "Mozilla/5.0 (secret-agent)",
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).InfoContext(context.Background(), "x", slog.Any("request", req))
	out := buf.String()
	for _, banned := range []string{"203.0.113.7", "secret-agent", "free text reason", `"ip"`, "user_agent"} {
		if strings.Contains(out, banned) {
			t.Errorf("log contains %q: %s", banned, out)
		}
	}
	for _, want := range []string{`"request_id":"req_1"`, `"channel":"api"`, `"client_id":"cli_1"`, `"operation":"users.create"`, `"change_ticket":"CHG-1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s: %s", want, out)
		}
	}
}
