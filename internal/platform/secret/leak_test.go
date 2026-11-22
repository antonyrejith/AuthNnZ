package secret_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/antonyrejith/portun/internal/platform/secret"
)

type dbConfig struct {
	URL      string
	Password secret.String
	Key      secret.Bytes
}

type store struct {
	cfg dbConfig // unexported: fmt cannot call methods on values reached here
}

type client struct {
	name     string
	password secret.String
}

// TestNoLeakThroughNesting reproduces review finding R1: secrets reached
// through unexported fields, other verbs, errors and text logs.
func TestNoLeakThroughNesting(t *testing.T) {
	t.Parallel()
	const value = "hunter2"
	s := secret.NewString(value)
	b := secret.NewBytes([]byte(value))
	st := &store{cfg: dbConfig{URL: "postgres://db", Password: s, Key: b}}

	var text bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("x", slog.Any("client", client{name: "c", password: s}))
	var js bytes.Buffer
	slog.New(slog.NewJSONHandler(&js, nil)).Info("x", slog.Any("cfg", st.cfg))

	outputs := map[string]string{
		"%+v nested":    fmt.Sprintf("%+v", st),
		"%v deref":      fmt.Sprintf("%v", *st),
		"%#v nested":    fmt.Sprintf("%#v", *st),
		"errorf":        fmt.Errorf("store %v unavailable", *st).Error(),
		"%d string":     fmt.Sprintf("%d", s),
		"%t string":     fmt.Sprintf("%t", s),
		"%x string":     fmt.Sprintf("%x", s),
		"%d bytes":      fmt.Sprintf("%d", b),
		"%x bytes":      fmt.Sprintf("%x", b),
		"%s bytes":      fmt.Sprintf("%s", b),
		"slog text":     text.String(),
		"slog json":     js.String(),
		"sprint struct": fmt.Sprint(client{name: "c", password: s}),
	}
	hexValue := fmt.Sprintf("%x", value)
	for name, out := range outputs {
		if strings.Contains(out, value) || strings.Contains(out, hexValue) || strings.Contains(out, "104 117 110") {
			t.Errorf("%s leaked the secret: %s", name, out)
		}
	}
}

func TestEqualAndRoundTrip(t *testing.T) {
	t.Parallel()
	a, b, c := secret.NewString("x"), secret.NewString("x"), secret.NewString("y")
	if !a.Equal(b) || a.Equal(c) {
		t.Fatal("String.Equal is wrong")
	}
	if !secret.NewBytes([]byte("k")).Equal(secret.NewBytes([]byte("k"))) {
		t.Fatal("Bytes.Equal is wrong")
	}
	out, err := json.Marshal(struct {
		S secret.String `json:"s"`
	}{a})
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		S secret.String `json:"s"`
	}
	if err := json.Unmarshal(out, &in); err == nil {
		t.Fatal("unmarshaling the redaction marker must fail, not replace the secret")
	}
}
