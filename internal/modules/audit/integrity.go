package audit

// Integrity verification and export entry points for operators and the
// portunctl CLI (GOV-18). This file is owned by T0.8b (audit store and
// integrity); the rest of the facade is T0.8a's.

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/store"
	"github.com/antonyrejith/portun/internal/modules/audit/internal/verify"
	"github.com/antonyrejith/portun/internal/platform/db/migrate"
	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/ids"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// VerifyOptions selects the range and trust anchors of a verification.
type VerifyOptions = verify.Options

// VerifyReport is the result of a verification.
type VerifyReport = verify.Report

// VerifyFailure is one integrity finding.
type VerifyFailure = verify.Failure

// TrustedKeys are trusted public keys by key reference and version.
type TrustedKeys = verify.Keys

// StoreMigrations returns the audit store's schema migrations (schema
// "audit"), applied by the owner role with migrate.Up.
func StoreMigrations() migrate.Set { return store.Migrations() }

// ParseTrustedKeys reads trusted checkpoint signing keys (PEM "PUBLIC KEY"
// blocks with Key-Ref and Key-Version headers).
func ParseTrustedKeys(pemData []byte) (TrustedKeys, error) {
	return verify.ParseKeysPEM(pemData) //nolint:wrapcheck // errs-coded
}

// ParseTSARoots reads PEM certificates trusted to sign timestamp tokens.
func ParseTSARoots(pemData []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, errs.New(errs.CodeInvalidRequest, "no certificates in the TSA roots file")
	}
	return pool, nil
}

// Operator identifies the person running an integrity operation; the
// access is recorded in the platform trail and, for a tenant chain, in the
// tenant's trail (audit of audit, GOV-16) before any record is read.
type Operator struct {
	ID     string
	Reason string
}

// VerifyExport verifies an exported chain offline.
func VerifyExport(ctx context.Context, r io.Reader, opts VerifyOptions) (*VerifyReport, error) {
	src, err := verify.ReadExport(r)
	if err != nil {
		return nil, err //nolint:wrapcheck // errs-coded
	}
	return verify.Chain(ctx, src, opts) //nolint:wrapcheck // errs-coded
}

// VerifyDatabase verifies chain ("platform" or a tenant ID) in the store,
// connecting with platformURL as portun_platform.
func VerifyDatabase(ctx context.Context, platformURL secret.String, chain string, op Operator, opts VerifyOptions) (*VerifyReport, error) {
	var rep *VerifyReport
	err := integrityWithChain(ctx, platformURL, chain, op, "audit.verified", func(ctx context.Context, src verify.DBSource) error {
		var err error
		rep, err = verify.Chain(ctx, src, opts)
		return err //nolint:wrapcheck // errs-coded
	})
	return rep, err
}

// ExportDatabase writes chain's records from..to (0: open, widened to whole
// checkpoints) in the verifiable export format.
func ExportDatabase(ctx context.Context, platformURL secret.String, chain string, op Operator, from, to uint64, w io.Writer) error {
	return integrityWithChain(ctx, platformURL, chain, op, "audit.exported", func(ctx context.Context, src verify.DBSource) error {
		_, _, err := verify.Export(ctx, src, from, to, time.Now().UTC(), w)
		return err //nolint:wrapcheck // errs-coded
	})
}

// SigningKeysPEM returns the published checkpoint signing keys as PEM.
func SigningKeysPEM(ctx context.Context, platformURL secret.String) ([]byte, error) {
	conn, err := integrityConnect(ctx, platformURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	ks, err := store.SigningKeys(ctx, conn)
	if err != nil {
		return nil, err //nolint:wrapcheck // errs-coded
	}
	return verify.KeysPEM(ks), nil
}

func integrityConnect(ctx context.Context, u secret.String) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(u.Reveal())
	if err != nil {
		return nil, errs.New(errs.CodeInvalidRequest, "the database URL is not a valid PostgreSQL connection string")
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, errs.Wrap(errs.CodeUnavailable, err, "connect to the database")
	}
	return conn, nil
}

func integrityWithChain(ctx context.Context, u secret.String, chainName string, op Operator, eventType string,
	fn func(ctx context.Context, src verify.DBSource) error,
) error {
	if op.ID == "" {
		return errs.New(errs.CodeInvalidRequest, "the operator ID is required")
	}
	chain, err := store.ParseChain(chainName)
	if err != nil {
		return err //nolint:wrapcheck // errs-coded
	}
	conn, err := integrityConnect(ctx, u)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	// Record the access first (fail closed).
	tenants := []ids.TenantID{{}}
	if chain != uuid.Nil {
		tenants = append(tenants, ids.FromUUID[ids.TenantKind](chain))
	}
	appender := store.NewAppender(nil)
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		for _, t := range tenants {
			raw, err := integrityOperatorEvent(t, eventType, store.ChainName(chain), op)
			if err != nil {
				return err
			}
			if _, err := appender.Append(ctx, tx, raw); err != nil {
				return err //nolint:wrapcheck // errs-coded
			}
		}
		return nil
	})
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "record the audit access")
	}
	return pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error { //nolint:wrapcheck // errs-coded
		return fn(ctx, verify.DBSource{Q: tx, Chain: chain})
	})
}

func integrityOperatorEvent(tenant ids.TenantID, eventType, chain string, op Operator) ([]byte, error) {
	id, err := ids.New[ids.AuditEventKind]()
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "new audit event ID")
	}
	ev := Event{
		ID:       id,
		Time:     time.Now().UTC().Truncate(time.Millisecond),
		TenantID: tenant,
		Category: CategoryAuditAccess,
		Type:     eventType,
		Outcome:  OutcomeSuccess,
		Actor:    Actor{Type: "platform_operator", ID: op.ID},
		Channel:  Channel{Type: "cli"},
		Target:   &Target{Type: "audit_chain", ID: chain},
		Request:  Request{Operation: "portunctl " + eventType},
		Origin:   Origin{Service: "portunctl"},
	}
	if op.Reason != "" {
		ev.Reason = &Reason{Text: op.Reason}
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "encode the audit event")
	}
	return raw, nil
}
