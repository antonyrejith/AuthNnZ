// Package store is the audit store of record (ADR-0008): the audit schema
// (migrations), the canonical hash of a record (RFC 8785 JCS), and the
// gapless, hash-chained append used by the writer.
//
// The package works on the record's JSON form, not on the facade's Go type,
// so that the facade can depend on it without an import cycle and so that
// fields added to the record later are hashed and kept even by an older
// writer. Header decodes the few fields the store indexes.
package store

import (
	"embed"
	"io/fs"

	"github.com/google/uuid"

	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/ids"
)

// Schema is the PostgreSQL schema of the audit store.
const Schema = "audit"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the audit schema's migration set, to be applied by the
// owner role with migrate.Up.
func Migrations() migrate.Set {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		// The embedded directory is fixed at compile time.
		panic("audit store: migrations directory missing: " + err.Error())
	}
	return migrate.Set{Schema: Schema, FS: sub}
}

// ChainOf returns the chain ID of a tenant (the zero tenant is the platform chain).
func ChainOf(tenant ids.TenantID) uuid.UUID { return tenant.UUID() }

// ChainName renders a chain ID for people: the tenant TypeID, or "platform".
func ChainName(chain uuid.UUID) string {
	if chain == uuid.Nil {
		return "platform"
	}
	return ids.FromUUID[ids.TenantKind](chain).String()
}

// ParseChain parses "platform", a tenant TypeID or a UUID.
func ParseChain(s string) (uuid.UUID, error) {
	if s == "platform" {
		return uuid.Nil, nil
	}
	if t, err := ids.Parse[ids.TenantKind](s); err == nil {
		return t.UUID(), nil
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, invalid("chain must be \"platform\", a tenant ID or a UUID")
	}
	return u, nil
}
