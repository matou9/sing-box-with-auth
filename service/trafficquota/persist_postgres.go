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

// LoadMany batches WHERE user_name = ANY($2) queries so the flush hot
// path scales with the dirty-user count instead of the total user set.
// Each query covers at most loadManyBatchSize users to keep the
// parameter array bounded; pgx encodes []string as text[] automatically.
func (p *PostgresPersister) LoadMany(periodKey string, users []string) (map[string]int64, error) {
	if len(users) == 0 {
		return map[string]int64{}, nil
	}
	result := make(map[string]int64, len(users))
	query := "SELECT user_name, bytes_used FROM " + p.table + " WHERE period_key = $1 AND user_name = ANY($2)"
	for start := 0; start < len(users); start += loadManyBatchSize {
		end := start + loadManyBatchSize
		if end > len(users) {
			end = len(users)
		}
		batch := users[start:end]
		if err := p.loadManyBatch(query, periodKey, batch, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (p *PostgresPersister) loadManyBatch(query, periodKey string, batch []string, result map[string]int64) error {
	rows, err := p.pool.Query(p.ctx, query, periodKey, batch)
	if err != nil {
		return E.Cause(err, "query PostgreSQL traffic quota rows (batch)")
	}
	defer rows.Close()
	for rows.Next() {
		var user string
		var bytes int64
		if scanErr := rows.Scan(&user, &bytes); scanErr != nil {
			return E.Cause(scanErr, "scan PostgreSQL traffic quota row (batch)")
		}
		result[user] = bytes
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return E.Cause(rowsErr, "iterate PostgreSQL traffic quota rows (batch)")
	}
	return nil
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
