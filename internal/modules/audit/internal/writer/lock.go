package writer

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// sessionLock is the writer lock held by a dedicated session.
type sessionLock struct {
	conn *pgx.Conn
	pid  uint32
}

// acquire connects and waits until the session holds LockID.
func acquire(ctx context.Context, url secret.String, retry time.Duration, logger *slog.Logger) (*sessionLock, error) {
	cfg, err := pgx.ParseConfig(url.Reveal())
	if err != nil {
		return nil, errs.New(errs.CodeInvalidRequest, "audit writer: the lock URL is not a valid PostgreSQL connection string")
	}
	cfg.RuntimeParams["application_name"] = "portun-audit-writer-lock"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, errs.Wrap(errs.CodeUnavailable, err, "audit writer: connect the lock session")
	}
	l := &sessionLock{conn: conn, pid: conn.PgConn().PID()}
	for {
		var ok bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, LockID).Scan(&ok); err != nil {
			l.release(ctx, logger)
			return nil, errs.Wrap(errs.CodeUnavailable, err, "audit writer: take the lock")
		}
		if ok {
			return l, nil
		}
		if !sleep(ctx, retry) {
			l.release(ctx, logger)
			return nil, errs.Wrap(errs.CodeUnavailable, ctx.Err(), "audit writer: stopped while waiting for the lock")
		}
	}
}

// watch returns when ctx ends (nil) or the lock session fails (error).
func (l *sessionLock) watch(ctx context.Context, interval time.Duration) error {
	for sleep(ctx, interval) {
		pctx, cancel := context.WithTimeout(ctx, interval)
		err := l.conn.Ping(pctx)
		cancel()
		if err != nil && ctx.Err() == nil {
			return errs.Wrap(errs.CodeUnavailable, err, "audit writer: lock session lost")
		}
	}
	return nil
}

// release closes the session, which releases the lock.
func (l *sessionLock) release(ctx context.Context, logger *slog.Logger) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := l.conn.Close(cctx); err != nil {
		logger.WarnContext(ctx, "audit writer: close the lock session", slog.String("error", err.Error()))
	}
}
