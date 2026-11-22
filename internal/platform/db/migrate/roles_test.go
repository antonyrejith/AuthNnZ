package migrate

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// TestScramVerifier checks the verifier against a vector computed
// independently (Python hashlib/hmac, RFC 5802 formulas).
//
// Covers: SEC-09
func TestScramVerifier(t *testing.T) {
	t.Parallel()
	salt := make([]byte, 16)
	for i := range salt {
		salt[i] = byte(i)
	}
	got, err := scramVerifier("correct horse battery staple", salt, 4096)
	if err != nil {
		t.Fatal(err)
	}
	const want = "SCRAM-SHA-256$4096:AAECAwQFBgcICQoLDA0ODw==$ONYbSJBXtKl6bP6PVqw8pm9e7EiacprLnoUQPFS80Hw=:IPOtHuGJ2HifEQg74W2XXqqCrCyQG55GbPRHa6g6n9w="
	if got != want {
		t.Fatalf("verifier = %s\nwant %s", got, want)
	}
}

// TestRoleStatement checks validation and that the plaintext password never
// appears in the statement.
//
// Covers: SEC-09
func TestRoleStatement(t *testing.T) {
	t.Parallel()
	const pw = "a-very-long-password-123"
	tests := []struct {
		name     string
		spec     roleSpec
		exists   bool
		wantErr  errs.Code
		contains []string
	}{
		{
			"create app",
			roleSpec{name: "portun_app", password: secret.NewString(pw)},
			false, "",
			[]string{`CREATE ROLE "portun_app" WITH LOGIN NOSUPERUSER`, "NOBYPASSRLS", "PASSWORD 'SCRAM-SHA-256$4096:"},
		},
		{
			"alter platform",
			roleSpec{name: "portun_platform", bypassRLS: true, password: secret.NewString(pw)},
			true, "",
			[]string{`ALTER ROLE "portun_platform" WITH LOGIN NOSUPERUSER`, " BYPASSRLS "},
		},
		{"short password", roleSpec{name: "r", password: secret.NewString("short")}, false, errs.CodeInvalidRequest, nil},
		{"non-ascii password", roleSpec{name: "r", password: secret.NewString("pässwörd-pässwörd-x")}, false, errs.CodeInvalidRequest, nil},
		{"control character", roleSpec{name: "r", password: secret.NewString("password\npassword\n")}, false, errs.CodeInvalidRequest, nil},
		{"bad role name", roleSpec{name: `x"; DROP`, password: secret.NewString(pw)}, false, errs.CodeInternal, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			build, err := roleStatement(tt.spec)
			if tt.wantErr != "" {
				if !errs.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			stmt := build(tt.exists)
			for _, c := range tt.contains {
				if !strings.Contains(stmt, c) {
					t.Errorf("statement %q lacks %q", stmt, c)
				}
			}
			if strings.Contains(stmt, pw) {
				t.Fatal("statement contains the plaintext password")
			}
		})
	}
}

// TestUpValidation checks argument validation before any connection.
func TestUpValidation(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{}
	tests := []struct {
		name string
		url  string
		sets []Set
		want errs.Code
	}{
		{"no sets", "postgres://localhost/x", nil, errs.CodeInternal},
		{"bad schema", "postgres://localhost/x", []Set{{Schema: "Bad-Name", FS: fsys}}, errs.CodeInternal},
		{"system schema", "postgres://localhost/x", []Set{{Schema: "pg_mine", FS: fsys}}, errs.CodeInternal},
		{"nil fs", "postgres://localhost/x", []Set{{Schema: "a"}}, errs.CodeInternal},
		{"duplicate", "postgres://localhost/x", []Set{{Schema: "a", FS: fsys}, {Schema: "a", FS: fsys}}, errs.CodeInternal},
		{"bad url", "postgres://user:secretpw@host:notaport/x", []Set{{Schema: "a", FS: fsys}}, errs.CodeInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Up(context.Background(), secret.NewString(tt.url), tt.sets...)
			if !errs.Is(err, tt.want) {
				t.Fatalf("err = %v, want %s", err, tt.want)
			}
			if strings.Contains(err.Error(), "secretpw") {
				t.Fatal("error leaks the password")
			}
		})
	}
}
