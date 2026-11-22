package migrate

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/platform/db"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// MinPasswordLength is the shortest role password EnsureRoles accepts.
const MinPasswordLength = 16

// rolesLockID serializes EnsureRoles callers on one database
// ("portunrl" as a big-endian int64).
const rolesLockID int64 = 0x706f7274756e726c

// SCRAM-SHA-256 parameters, as PostgreSQL uses them by default.
const (
	scramIterations = 4096
	scramSaltLen    = 16
)

// EnsureRoles creates the application role (portun_app: LOGIN, NOSUPERUSER,
// NOBYPASSRLS) and the platform role (portun_platform: LOGIN, NOSUPERUSER,
// BYPASSRLS) if they are missing. Roles that exist are reset to exactly these
// attributes and the given passwords; their role-level settings are removed
// (ALTER ROLE ... RESET ALL, also for every database-specific setting, so no
// role or database default such as app.tenant_id survives); and every
// membership they hold in another role is revoked (so they cannot inherit or
// SET ROLE to a more privileged role). The call is idempotent and always
// leaves the roles in the secure state. It is for development and test
// bootstrap; production roles are provisioned by the deployment.
//
// ownerURL must connect as a role allowed to create roles with BYPASSRLS (a
// superuser, or on PostgreSQL 16+ a CREATEROLE role that has BYPASSRLS).
// Passwords must be at least MinPasswordLength printable ASCII characters.
// They are sent to the server only as SCRAM-SHA-256 verifiers, so the
// plaintext never appears in statements or server logs.
func EnsureRoles(ctx context.Context, ownerURL, appPassword, platformPassword secret.String) error {
	return ensureRoles(ctx, ownerURL, []roleSpec{
		{name: db.AppRole, bypassRLS: false, password: appPassword},
		{name: db.PlatformRole, bypassRLS: true, password: platformPassword},
	})
}

// roleSpec describes one login role.
type roleSpec struct {
	name      string
	bypassRLS bool
	password  secret.String
}

func ensureRoles(ctx context.Context, ownerURL secret.String, specs []roleSpec) error {
	statements := make([]func(exists bool) string, 0, len(specs))
	for _, s := range specs {
		stmt, err := roleStatement(s)
		if err != nil {
			return err
		}
		statements = append(statements, stmt)
	}
	cfg, err := pgx.ParseConfig(ownerURL.Reveal())
	if err != nil {
		return errs.New(errs.CodeInvalidRequest, "migrate: the owner database URL is not a valid PostgreSQL connection string")
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "migrate: connect to the database")
	}
	defer closeConn(ctx, conn)
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", rolesLockID); err != nil {
			return wrap(err, "migrate: take the role lock")
		}
		for i, s := range specs {
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", s.name).Scan(&exists); err != nil {
				return wrap(err, "migrate: look up role "+quote(s.name))
			}
			if _, err := tx.Exec(ctx, statements[i](exists)); err != nil {
				return wrap(err, "migrate: create or alter role "+quote(s.name))
			}
			if err := resetRole(ctx, tx, s.name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return wrap(err, "migrate: ensure roles")
	}
	return nil
}

// resetRoleQuery lists the statements that remove the role-level settings
// and the memberships of role $1. Identifiers are quoted with quote_ident.
const resetRoleQuery = `
SELECT 'ALTER ROLE ' || quote_ident($1) || ' IN DATABASE ' || quote_ident(d.datname) || ' RESET ALL'
FROM pg_db_role_setting s
JOIN pg_database d ON d.oid = s.setdatabase
WHERE s.setrole = (SELECT oid FROM pg_roles WHERE rolname = $1)
UNION ALL
SELECT 'REVOKE ' || quote_ident(g.rolname) || ' FROM ' || quote_ident($1) || ' GRANTED BY ' || quote_ident(gr.rolname)
FROM pg_auth_members m
JOIN pg_roles g ON g.oid = m.roleid
JOIN pg_roles gr ON gr.oid = m.grantor
WHERE m.member = (SELECT oid FROM pg_roles WHERE rolname = $1)`

// resetRole removes every setting of the role (for all databases and for
// each database) and revokes every membership it holds.
func resetRole(ctx context.Context, tx pgx.Tx, name string) error {
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := tx.Exec(ctx, "ALTER ROLE "+ident+" RESET ALL"); err != nil {
		return wrap(err, "migrate: reset the settings of role "+quote(name))
	}
	rows, err := tx.Query(ctx, resetRoleQuery, name)
	if err != nil {
		return wrap(err, "migrate: list the settings and memberships of role "+quote(name))
	}
	stmts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return wrap(err, "migrate: list the settings and memberships of role "+quote(name))
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return wrap(err, "migrate: reset role "+quote(name))
		}
	}
	return nil
}

// roleStatement validates s and returns a function that builds its CREATE
// ROLE (or, if it exists, ALTER ROLE) statement.
func roleStatement(s roleSpec) (func(exists bool) string, error) {
	if !identPattern.MatchString(s.name) {
		return nil, errs.New(errs.CodeInternal, "migrate: invalid role name "+quote(s.name))
	}
	if err := validatePassword(s.password.Reveal()); err != nil {
		return nil, errs.Wrap(errs.CodeInvalidRequest, err, "migrate: unacceptable password for role "+quote(s.name))
	}
	salt := make([]byte, scramSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "migrate: generate a password salt")
	}
	verifier, err := scramVerifier(s.password.Reveal(), salt, scramIterations)
	if err != nil {
		return nil, err
	}
	bypass := "NOBYPASSRLS"
	if s.bypassRLS {
		bypass = "BYPASSRLS"
	}
	// The verifier consists of base64, digits, '$' and ':' only, so it
	// cannot end the literal; utility statements take no parameters.
	attrs := " WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION INHERIT " + bypass +
		" PASSWORD '" + verifier + "'"
	ident := pgx.Identifier{s.name}.Sanitize()
	return func(exists bool) string {
		if exists {
			return "ALTER ROLE " + ident + attrs
		}
		return "CREATE ROLE " + ident + attrs
	}, nil
}

// validatePassword accepts printable ASCII of at least MinPasswordLength
// characters. For such passwords SASLprep is the identity, so the verifier
// computed here matches what every client sends.
func validatePassword(p string) error {
	if len(p) < MinPasswordLength {
		return errs.New(errs.CodeInvalidRequest, "the password must have at least "+strconv.Itoa(MinPasswordLength)+" characters")
	}
	if strings.IndexFunc(p, func(r rune) bool { return r < 0x20 || r > 0x7e }) >= 0 {
		return errs.New(errs.CodeInvalidRequest, "the password must consist of printable ASCII characters")
	}
	return nil
}

// scramVerifier returns the SCRAM-SHA-256 verifier PostgreSQL stores for a
// password (RFC 5802, RFC 7677):
// SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>.
func scramVerifier(password string, salt []byte, iterations int) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", errs.Wrap(errs.CodeInternal, err, "migrate: derive the password verifier")
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")
	b64 := base64.StdEncoding.EncodeToString
	return "SCRAM-SHA-256$" + strconv.Itoa(iterations) + ":" + b64(salt) + "$" + b64(storedKey[:]) + ":" + b64(serverKey), nil
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}
