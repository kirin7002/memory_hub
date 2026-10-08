package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"memoryhub/migrations"
	"sort"
	"time"
)

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10
	cfg.MinConns = 0
	cfg.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(74598101)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(name text PRIMARY KEY, checksum bytea NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		b, err := migrations.Files.ReadFile(name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		var existing []byte
		err = tx.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE name=$1`, name).Scan(&existing)
		if err == nil {
			if string(existing) != string(sum[:]) {
				return fmt.Errorf("migration checksum changed: %s", name)
			}
			continue
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if _, err = tx.Exec(ctx, string(b)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(name,checksum) VALUES($1,$2)`, name, sum[:]); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func Seed(ctx context.Context, pool *pgxpool.Pool) error {
	return EnsureDemoAccount(ctx, pool, "demo-user")
}

// EnsureDemoAccount 仅为显式本地 Demo 准备账户及三类设备。
// ON CONFLICT 不更新既有设备，避免连接时重新激活被撤销的设备。
func EnsureDemoAccount(ctx context.Context, pool *pgxpool.Pool, user string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO user_stream_heads(tenant_id,user_id) VALUES('demo',$1) ON CONFLICT DO NOTHING`, user); err != nil {
		return err
	}
	for _, d := range []string{"phone", "car", "web"} {
		if _, err = tx.Exec(ctx, `INSERT INTO devices(tenant_id,user_id,device_id,kind,scope_key) VALUES('demo',$1,$2,$2,'personal') ON CONFLICT DO NOTHING`, user, d); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
