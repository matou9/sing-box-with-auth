//go:build with_postgres

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	connStr := os.Getenv("DBSETUP_DSN")
	if connStr == "" {
		fmt.Fprintln(os.Stderr, "DBSETUP_DSN not set (expected postgres://user:pass@host:port/db)")
		os.Exit(1)
	}
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err = ensureSchema(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, "ensure schema:", err)
		os.Exit(1)
	}

	cmd := "list"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "migrate":
		fmt.Println("Migration complete")

	case "init":
		_, err = pool.Exec(ctx,
			`INSERT INTO users(name, password) VALUES($1,$2) ON CONFLICT(name) DO UPDATE SET password=$2`,
			"user1", "pass1")
		if err != nil {
			fmt.Fprintln(os.Stderr, "insert user1:", err)
			os.Exit(1)
		}
		fmt.Println("Initialized: user1/pass1")

	case "add":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: add <name> <password>")
			os.Exit(1)
		}
		name, pass := os.Args[2], os.Args[3]
		_, err = pool.Exec(ctx,
			`INSERT INTO users(name, password) VALUES($1,$2) ON CONFLICT(name) DO UPDATE SET password=$2`,
			name, pass)
		if err != nil {
			fmt.Fprintln(os.Stderr, "insert:", err)
			os.Exit(1)
		}
		fmt.Printf("Added: %s/%s\n", name, pass)

	case "speed":
		if len(os.Args) < 5 {
			fmt.Fprintln(os.Stderr, "Usage: speed <name> <upload_mbps> <download_mbps>")
			os.Exit(1)
		}
		_, err = pool.Exec(ctx, `
			INSERT INTO users(name, upload_mbps, download_mbps)
			VALUES($1, $2, $3)
			ON CONFLICT(name) DO UPDATE SET
				upload_mbps = EXCLUDED.upload_mbps,
				download_mbps = EXCLUDED.download_mbps`,
			os.Args[2], os.Args[3], os.Args[4],
		)
		if err != nil {
			fmt.Fprintln(os.Stderr, "set speed:", err)
			os.Exit(1)
		}
		fmt.Printf("Updated speed: %s upload=%s download=%s\n", os.Args[2], os.Args[3], os.Args[4])

	case "quota":
		if len(os.Args) < 5 {
			fmt.Fprintln(os.Stderr, "Usage: quota <name> <quota_gb> <period>")
			os.Exit(1)
		}
		period := strings.ToLower(os.Args[4])
		switch period {
		case "daily", "weekly", "monthly":
		default:
			fmt.Fprintln(os.Stderr, "quota period must be one of: daily, weekly, monthly")
			os.Exit(1)
		}
		_, err = pool.Exec(ctx, `
			INSERT INTO users(name, quota_gb, quota_period)
			VALUES($1, $2, $3)
			ON CONFLICT(name) DO UPDATE SET
				quota_gb = EXCLUDED.quota_gb,
				quota_period = EXCLUDED.quota_period`,
			os.Args[2], os.Args[3], period,
		)
		if err != nil {
			fmt.Fprintln(os.Stderr, "set quota:", err)
			os.Exit(1)
		}
		fmt.Printf("Updated quota: %s quota_gb=%s period=%s\n", os.Args[2], os.Args[3], period)

	case "speed-clear":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: speed-clear <name>")
			os.Exit(1)
		}
		_, err = pool.Exec(ctx, `UPDATE users SET upload_mbps = 0, download_mbps = 0 WHERE name = $1`, os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "clear speed:", err)
			os.Exit(1)
		}
		fmt.Printf("Cleared speed: %s\n", os.Args[2])

	case "quota-clear":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: quota-clear <name>")
			os.Exit(1)
		}
		_, err = pool.Exec(ctx, `UPDATE users SET quota_gb = 0, quota_period = '', quota_period_start = '', quota_period_days = 0 WHERE name = $1`, os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "clear quota:", err)
			os.Exit(1)
		}
		fmt.Printf("Cleared quota: %s\n", os.Args[2])

	case "delete":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: delete <name>")
			os.Exit(1)
		}
		pool.Exec(ctx, `DELETE FROM users WHERE name=$1`, os.Args[2])
		fmt.Printf("Deleted: %s\n", os.Args[2])

	case "clear":
		pool.Exec(ctx, `DELETE FROM users`)
		fmt.Println("Cleared all users")

	case "list":
		rows, err := pool.Query(ctx, `SELECT name, password, upload_mbps, download_mbps, quota_gb, quota_period FROM users ORDER BY name`)
		if err != nil {
			fmt.Fprintln(os.Stderr, "query:", err)
			os.Exit(1)
		}
		defer rows.Close()
		fmt.Println("=== Users in PostgreSQL ===")
		count := 0
		for rows.Next() {
			var name, pass, period string
			var uploadMbps, downloadMbps int
			var quotaGB float64
			rows.Scan(&name, &pass, &uploadMbps, &downloadMbps, &quotaGB, &period)
			fmt.Printf("  %s / %s  speed=%d/%d quota=%.4f %s\n", name, pass, uploadMbps, downloadMbps, quotaGB, period)
			count++
		}
		fmt.Printf("Total: %d\n", count)
	}
}

func ensureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS users (
			name               TEXT PRIMARY KEY,
			password           TEXT,
			uuid               TEXT,
			alter_id           INT DEFAULT 0,
			flow               TEXT DEFAULT '',
			upload_mbps        INT DEFAULT 0,
			download_mbps      INT DEFAULT 0,
			quota_gb           DOUBLE PRECISION DEFAULT 0,
			quota_period       TEXT DEFAULT '',
			quota_period_start TEXT DEFAULT '',
			quota_period_days  INT DEFAULT 0
		)`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS upload_mbps INT DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS download_mbps INT DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_gb DOUBLE PRECISION DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_period TEXT DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_period_start TEXT DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_period_days INT DEFAULT 0`,
		`CREATE OR REPLACE FUNCTION notify_user_changes() RETURNS trigger AS $$
		BEGIN
			PERFORM pg_notify(
				'user_changes',
				CASE TG_OP
					WHEN 'DELETE' THEN 'del:' || OLD.name
					ELSE COALESCE(NEW.name, OLD.name)
				END
			);
			RETURN COALESCE(NEW, OLD);
		END;
		$$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS user_changes_trigger ON users`,
		`CREATE TRIGGER user_changes_trigger
		AFTER INSERT OR UPDATE OR DELETE ON users
		FOR EACH ROW EXECUTE FUNCTION notify_user_changes()`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
