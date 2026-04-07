//go:build with_postgres

package trafficquota

import (
	"context"
	"errors"
	"regexp"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var postgresTablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type PostgresPersister struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	table string
}

func NewPostgresPersister(ctx context.Context, options *option.TrafficQuotaPostgresOptions) (*PostgresPersister, error) {
	if options == nil {
		return nil, E.New("missing postgres options")
	}
	table := options.Table
	if table == "" {
		table = "traffic_quota"
	}
	if !postgresTablePattern.MatchString(table) {
		return nil, E.New("invalid postgres table name: ", table)
	}
	pool, err := pgxpool.New(ctx, options.ConnectionString)
	if err != nil {
		return nil, E.Cause(err, "connect to PostgreSQL")
	}
	persister := &PostgresPersister{
		ctx:   ctx,
		pool:  pool,
		table: table,
	}
	if err := persister.ensureTable(); err != nil {
		pool.Close()
		return nil, err
	}
	return persister, nil
}

func (p *PostgresPersister) Load(user, periodKey string) (int64, error) {
	query := "SELECT bytes_used FROM " + p.table + " WHERE user_name = $1 AND period_key = $2"
	var bytes int64
	err := p.pool.QueryRow(p.ctx, query, user, periodKey).Scan(&bytes)
	if err != nil {
		if isPostgresNoRows(err) {
			return 0, nil
		}
		return 0, E.Cause(err, "query PostgreSQL traffic quota row")
	}
	return bytes, nil
}

func (p *PostgresPersister) LoadAll(periodKey string) (map[string]int64, error) {
	query := "SELECT user_name, bytes_used FROM " + p.table + " WHERE period_key = $1"
	rows, err := p.pool.Query(p.ctx, query, periodKey)
	if err != nil {
		return nil, E.Cause(err, "query PostgreSQL traffic quota rows")
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var user string
		var bytes int64
		if scanErr := rows.Scan(&user, &bytes); scanErr != nil {
			return nil, E.Cause(scanErr, "scan PostgreSQL traffic quota row")
		}
		result[user] = bytes
	}
	if err := rows.Err(); err != nil {
		return nil, E.Cause(err, "iterate PostgreSQL traffic quota rows")
	}
	return result, nil
}

func (p *PostgresPersister) Save(user, periodKey string, bytes int64) error {
	query := "INSERT INTO " + p.table + " (user_name, period_key, bytes_used, updated_at) VALUES ($1, $2, $3, NOW()) " +
		"ON CONFLICT (user_name, period_key) DO UPDATE SET bytes_used = EXCLUDED.bytes_used, updated_at = NOW()"
	_, err := p.pool.Exec(p.ctx, query, user, periodKey, bytes)
	if err != nil {
		return E.Cause(err, "save PostgreSQL traffic quota row")
	}
	return nil
}

func (p *PostgresPersister) IncrBy(user, periodKey string, delta int64) error {
	query := "INSERT INTO " + p.table + " (user_name, period_key, bytes_used, updated_at) VALUES ($1, $2, $3, NOW()) " +
		"ON CONFLICT (user_name, period_key) DO UPDATE SET bytes_used = " + p.table + ".bytes_used + EXCLUDED.bytes_used, updated_at = NOW()"
	_, err := p.pool.Exec(p.ctx, query, user, periodKey, delta)
	if err != nil {
		return E.Cause(err, "increment PostgreSQL traffic quota row")
	}
	return nil
}

func (p *PostgresPersister) Delete(user, periodKey string) error {
	query := "DELETE FROM " + p.table + " WHERE user_name = $1 AND period_key = $2"
	_, err := p.pool.Exec(p.ctx, query, user, periodKey)
	if err != nil {
		return E.Cause(err, "delete PostgreSQL traffic quota row")
	}
	return nil
}

func (p *PostgresPersister) Close() error {
	p.pool.Close()
	return nil
}

func (p *PostgresPersister) ensureTable() error {
	query := "CREATE TABLE IF NOT EXISTS " + p.table + " (" +
		"user_name TEXT NOT NULL," +
		"period_key TEXT NOT NULL," +
		"bytes_used BIGINT NOT NULL," +
		"updated_at TIMESTAMPTZ NOT NULL," +
		"PRIMARY KEY (user_name, period_key))"
	_, err := p.pool.Exec(p.ctx, query)
	if err != nil {
		return E.Cause(err, "ensure PostgreSQL traffic quota table")
	}
	return nil
}

func isPostgresNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
