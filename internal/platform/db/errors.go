package db

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/puddle/v2"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// SQLSTATE codes the mapping and Retryable treat specially
// (https://www.postgresql.org/docs/current/errcodes-appendix.html).
const (
	sqlstateUniqueViolation        = "23505"
	sqlstateExclusionViolation     = "23P01"
	sqlstateForeignKeyViolation    = "23503"
	sqlstateSerializationFailure   = "40001"
	sqlstateDeadlockDetected       = "40P01"
	sqlstateCompletionUnknown      = "40003"
	sqlstateQueryCanceled          = "57014"
	sqlstateLockNotAvailable       = "55P03"
	sqlstateIdleInTxSessionTimeout = "25P03"
	sqlstateTransactionTimeout     = "25P04"
)

// Client-safe messages. They never contain SQL text, parameters or row
// values: those stay in the cause, which is for logs only.
const (
	msgDatabaseError   = "database error"
	msgUnavailable     = "the database is unavailable; retry later"
	msgTimedOut        = "the database operation timed out"
	msgCanceled        = "the database operation was canceled"
	msgNotFound        = "not found"
	msgAlreadyExists   = "the resource already exists"
	msgConflictRelated = "the change conflicts with related data"
	msgConcurrent      = "the change conflicted with a concurrent change; retry the request"
	msgBusy            = "the resource is busy; retry later"
)

// MapError converts an error from pgx (or from code running inside a
// transaction) into an *errs.Error with a stable code and a client-safe
// message; the original error stays the cause. Errors that already carry an
// errs code pass through unchanged, and nil stays nil.
//
// Mapping:
//
//   - context deadline, statement timeout, transaction timeout: CodeDeadlineExceeded
//   - context cancellation: CodeUnavailable
//   - pgx.ErrNoRows: CodeNotFound
//   - unique, exclusion and foreign-key violations: CodeConflict
//   - serialization failure, deadlock: CodeConflict (see Retryable)
//   - lock timeout, connection failures, server shutdown, resource
//     exhaustion, closed pool: CodeUnavailable
//   - anything else, including privilege and row-level security violations:
//     CodeInternal
func MapError(err error) error {
	if err == nil {
		return nil
	}
	var e *errs.Error
	if errors.As(err, &e) {
		return err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), pgconn.Timeout(err):
		return errs.Wrap(errs.CodeDeadlineExceeded, err, msgTimedOut)
	case errors.Is(err, context.Canceled):
		return errs.Wrap(errs.CodeUnavailable, err, msgCanceled)
	case errors.Is(err, pgx.ErrNoRows):
		return errs.Wrap(errs.CodeNotFound, err, msgNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		code, msg := mapSQLState(pgErr.Code)
		return errs.Wrap(code, err, msg)
	}
	if isConnectionError(err) {
		return errs.Wrap(errs.CodeUnavailable, err, msgUnavailable)
	}
	return errs.Wrap(errs.CodeInternal, err, msgDatabaseError)
}

// mapSQLState maps a server error code to an errs code and message.
func mapSQLState(state string) (errs.Code, string) {
	switch state {
	case sqlstateUniqueViolation, sqlstateExclusionViolation:
		return errs.CodeConflict, msgAlreadyExists
	case sqlstateForeignKeyViolation:
		return errs.CodeConflict, msgConflictRelated
	case sqlstateSerializationFailure, sqlstateDeadlockDetected:
		return errs.CodeConflict, msgConcurrent
	case sqlstateQueryCanceled, sqlstateIdleInTxSessionTimeout, sqlstateTransactionTimeout:
		return errs.CodeDeadlineExceeded, msgTimedOut
	case sqlstateLockNotAvailable:
		return errs.CodeUnavailable, msgBusy
	case sqlstateCompletionUnknown:
		return errs.CodeUnavailable, msgUnavailable
	}
	switch sqlstateClass(state) {
	case "08", // connection exception
		"53", // insufficient resources (too many connections, disk full, out of memory)
		"57": // operator intervention (shutdown, cannot connect now); 57014 is handled above
		return errs.CodeUnavailable, msgUnavailable
	default:
		return errs.CodeInternal, msgDatabaseError
	}
}

func sqlstateClass(state string) string {
	if len(state) < 2 {
		return ""
	}
	return state[:2]
}

// isConnectionError reports failures to reach or keep a server connection.
func isConnectionError(err error) bool {
	var connectErr *pgconn.ConnectError
	var netErr net.Error
	return errors.As(err, &connectErr) ||
		errors.As(err, &netErr) ||
		errors.Is(err, pgconn.ErrConnClosed) ||
		errors.Is(err, puddle.ErrClosedPool) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed)
}

// Retryable reports whether err is a transient transaction conflict
// (serialization failure or deadlock) after which the whole transaction can
// be retried unchanged. Callers that run Serializable transactions use it to
// retry; InTenantTx and InPlatformTx never retry on their own, because fn may
// have effects outside the database.
func Retryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == sqlstateSerializationFailure || pgErr.Code == sqlstateDeadlockDetected
}
