package reconcile

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/antonyrejith/portun/internal/platform/errs"
	"github.com/antonyrejith/portun/internal/platform/secret"
)

// MarkerPrefix is the logical decoding message prefix of reconciler markers.
const MarkerPrefix = "portun.reconcile"

const standbyInterval = 10 * time.Second

// replicationURL adds replication=database to a connection URL.
func replicationURL(u secret.String) (string, error) {
	parsed, err := url.Parse(u.Reveal())
	if err != nil {
		return "", errs.New(errs.CodeInvalidRequest, "reconcile: the replication URL is not a valid URL")
	}
	q := parsed.Query()
	q.Set("replication", "database")
	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}

// session is one streaming replication connection.
type session struct {
	conn *pgconn.PgConn
	dec  *decoder
	// confirmed is the end of the last fully processed transaction.
	confirmed pglogrepl.LSN
}

func connectReplication(ctx context.Context, u secret.String) (*pgconn.PgConn, error) {
	ru, err := replicationURL(u)
	if err != nil {
		return nil, err
	}
	conn, err := pgconn.Connect(ctx, ru)
	if err != nil {
		return nil, errs.Wrap(errs.CodeUnavailable, err, "reconcile: connect for replication")
	}
	return conn, nil
}

// start begins streaming slot from lsn (0: the slot's confirmed position).
func (s *session) start(ctx context.Context, slot, publication string, lsn pglogrepl.LSN) error {
	err := pglogrepl.StartReplication(ctx, s.conn, pgx.Identifier{slot}.Sanitize(), lsn, pglogrepl.StartReplicationOptions{
		PluginArgs: []string{"proto_version '1'", "publication_names '" + strings.ReplaceAll(publication, "'", "''") + "'", "messages 'true'"},
	})
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "reconcile: start replication")
	}
	return nil
}

// event is what the receive loop reports.
type event struct {
	unaudited []Unaudited
	commit    bool
	marker    string // content of a reconciler marker message
}

// next receives until a commit or marker (or ctx ends), answering
// keepalives and reporting progress on the way.
func (s *session) next(ctx context.Context) (event, error) {
	deadline := time.Now().Add(standbyInterval)
	for {
		rctx, cancel := context.WithDeadline(ctx, deadline)
		msg, err := s.conn.ReceiveMessage(rctx)
		cancel()
		if err != nil {
			if ctx.Err() == nil && pgconn.Timeout(err) {
				if err := s.status(ctx); err != nil {
					return event{}, err
				}
				deadline = time.Now().Add(standbyInterval)
				continue
			}
			return event{}, errs.Wrap(errs.CodeUnavailable, err, "reconcile: receive")
		}
		ev, done, err := s.handle(ctx, msg)
		if err != nil || done {
			return ev, err
		}
	}
}

func (s *session) handle(ctx context.Context, msg pgproto3.BackendMessage) (event, bool, error) {
	switch m := msg.(type) {
	case *pgproto3.ErrorResponse:
		return event{}, false, errs.Wrap(errs.CodeUnavailable, pgconn.ErrorResponseToPgError(m), "reconcile: replication error")
	case *pgproto3.CopyData:
		if len(m.Data) == 0 {
			return event{}, false, nil
		}
		switch m.Data[0] {
		case pglogrepl.PrimaryKeepaliveMessageByteID:
			ka, err := pglogrepl.ParsePrimaryKeepaliveMessage(m.Data[1:])
			if err != nil {
				return event{}, false, errs.Wrap(errs.CodeInternal, err, "reconcile: parse keepalive")
			}
			if ka.ReplyRequested {
				return event{}, false, s.status(ctx)
			}
		case pglogrepl.XLogDataByteID:
			xld, err := pglogrepl.ParseXLogData(m.Data[1:])
			if err != nil {
				return event{}, false, errs.Wrap(errs.CodeInternal, err, "reconcile: parse WAL data")
			}
			return s.wal(xld)
		}
	}
	return event{}, false, nil
}

func (s *session) wal(xld pglogrepl.XLogData) (event, bool, error) {
	lm, err := pglogrepl.Parse(xld.WALData)
	if err != nil {
		return event{}, false, errs.Wrap(errs.CodeInternal, err, "reconcile: parse a logical message")
	}
	if dm, ok := lm.(*pglogrepl.LogicalDecodingMessage); ok {
		if dm.Prefix == MarkerPrefix && !s.dec.inTx {
			return event{marker: string(dm.Content)}, true, nil
		}
		return event{}, false, nil
	}
	u, commit, err := s.dec.handle(lm)
	if err != nil {
		return event{}, false, err
	}
	if !commit {
		return event{}, false, nil
	}
	if cm, ok := lm.(*pglogrepl.CommitMessage); ok {
		s.confirmed = cm.TransactionEndLSN
	}
	return event{unaudited: u, commit: true}, true, nil
}

// status reports the confirmed position (so the slot can release WAL).
func (s *session) status(ctx context.Context) error {
	if s.confirmed == 0 {
		return nil
	}
	err := pglogrepl.SendStandbyStatusUpdate(ctx, s.conn, pglogrepl.StandbyStatusUpdate{
		WALWritePosition: s.confirmed,
	})
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "reconcile: send status")
	}
	return nil
}

// keyCache resolves primary keys through a normal connection.
type keyCache struct {
	mu    sync.Mutex
	conn  *pgx.Conn
	cache map[string][]string
}

func (k *keyCache) resolve(ctx context.Context) KeyResolver {
	return func(schema, table string) ([]string, error) {
		k.mu.Lock()
		defer k.mu.Unlock()
		name := schema + "." + table
		if cols, ok := k.cache[name]; ok {
			return cols, nil
		}
		rows, err := k.conn.Query(ctx, `
SELECT a.attname FROM pg_index i
CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
WHERE i.indrelid = (quote_ident($1) || '.' || quote_ident($2))::regclass AND i.indisprimary
ORDER BY k.ord`, schema, table)
		if err != nil {
			return nil, errs.Wrap(errs.CodeUnavailable, err, "reconcile: read primary key")
		}
		cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, errs.Wrap(errs.CodeUnavailable, err, "reconcile: read primary key")
		}
		k.cache[name] = cols
		return cols, nil
	}
}

// EnsurePublication creates or updates publication name to cover every
// table of schemas (partitioned tables through their root). It must run as
// the role that owns those tables (the migration owner).
func EnsurePublication(ctx context.Context, ownerURL secret.String, name string, schemas []string) error {
	conn, err := pgx.Connect(ctx, ownerURL.Reveal())
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "reconcile: connect as the owner")
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	rows, err := conn.Query(ctx, `
SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = ANY($1) AND c.relkind IN ('r', 'p') AND NOT c.relispartition
ORDER BY 1`, schemas)
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "reconcile: list tables")
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "reconcile: list tables")
	}
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = $1)`, name).Scan(&exists); err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "reconcile: look up the publication")
	}
	ident := pgx.Identifier{name}.Sanitize()
	var stmt string
	switch {
	case !exists && len(tables) == 0:
		stmt = "CREATE PUBLICATION " + ident + " WITH (publish_via_partition_root = true)"
	case !exists:
		stmt = "CREATE PUBLICATION " + ident + " FOR TABLE " + strings.Join(tables, ", ") + " WITH (publish_via_partition_root = true)"
	case len(tables) == 0:
		return nil
	default:
		stmt = "ALTER PUBLICATION " + ident + " SET TABLE " + strings.Join(tables, ", ")
	}
	// Table names come from the catalog, quoted with quote_ident.
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "reconcile: create or update the publication")
	}
	return nil
}

// emitMarker writes a non-transactional logical decoding message.
func emitMarker(ctx context.Context, conn *pgx.Conn, token string) error {
	if _, err := conn.Exec(ctx, `SELECT pg_logical_emit_message(false, $1, $2)`, MarkerPrefix, token); err != nil {
		return errs.Wrap(errs.CodeUnavailable, err, "reconcile: emit marker")
	}
	return nil
}
