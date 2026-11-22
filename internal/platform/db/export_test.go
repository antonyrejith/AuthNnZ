package db

import "github.com/jackc/pgx/v5/pgxpool"

// AppPool exposes the application pool to the external tests, which use it
// to inspect pooled connections.
func AppPool(d *DB) *pgxpool.Pool { return d.app }
