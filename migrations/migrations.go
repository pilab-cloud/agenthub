// Package migrations embeds the goose migrations of the AgentHub database.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

// FS holds the *.sql migrations in its root directory.
//
//go:embed *.sql
var FS embed.FS

// Dir is the directory inside FS that holds the migrations.
const Dir = "."

// Up applies every pending migration. It is idempotent.
func Up(ctx context.Context, db *sql.DB) error {
	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	return nil
}

// Status returns the migration status.
func Status(ctx context.Context, db *sql.DB) (string, error) {
	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		return "", fmt.Errorf("migrations: %w", err)
	}
	st, err := p.Status(ctx)
	if err != nil {
		return "", fmt.Errorf("migrations: %w", err)
	}
	out := ""
	for _, s := range st {
		out += fmt.Sprintf("%d  %s\n", s.Source.Version, s.State)
	}
	return out, nil
}

// Down rolls back the last migration.
func Down(ctx context.Context, db *sql.DB) error {
	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if _, err := p.Down(ctx); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	return nil
}
