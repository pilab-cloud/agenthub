// Package db opens the PostgreSQL connection used by AgentHub.
package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	"go.pilab.hu/agenthub/internal/config"
)

// Open opens a pooled bun connection and verifies it with a ping.
func Open(ctx context.Context, cfg config.DatabaseConfig) (*bun.DB, error) {
	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(cfg.DSN)))
	if cfg.MaxOpenConns > 0 {
		sqldb.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		sqldb.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		sqldb.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}
	bunDB := bun.NewDB(sqldb, pgdialect.New())
	if err := bunDB.PingContext(ctx); err != nil {
		_ = bunDB.Close()
		return nil, err
	}
	return bunDB, nil
}

// Close closes the bun database.
func Close(db *bun.DB) error {
	if db == nil {
		return nil
	}
	return db.Close()
}

// Ping checks database reachability.
func Ping(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return sql.ErrConnDone
	}
	ctx2, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return db.PingContext(ctx2)
}
