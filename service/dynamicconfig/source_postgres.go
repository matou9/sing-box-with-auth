//go:build with_postgres

package dynamicconfig

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var identifierRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func validateIdentifier(name string) error {
	if !identifierRe.MatchString(name) {
		return E.New("invalid identifier: ", name)
	}
	return nil
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// PostgresSource listens on a Postgres NOTIFY channel and fetches per-user
// config rows when notified. It mirrors userprovider/source_postgres.go.
type PostgresSource struct {
	logger         log.ContextLogger
	pool           *pgxpool.Pool
	quotedTable    string
	notifyChannel  string
	quotedChannel  string
	updateInterval time.Duration
	receiver       *Receiver
}

// NewPostgresSource creates a PostgresSource. Call Run to start it.
func NewPostgresSource(ctx context.Context, logger log.ContextLogger, options *option.DynamicPostgresOptions, receiver *Receiver) (*PostgresSource, error) {
	table := options.Table
	if table == "" {
		table = "users"
	}
	notifyChannel := options.NotifyChannel
	if notifyChannel == "" {
		notifyChannel = "user_changes"
	}
	if err := validateIdentifier(table); err != nil {
		return nil, E.Cause(err, "dynamic-config postgres: invalid table name")
	}
	if err := validateIdentifier(notifyChannel); err != nil {
		return nil, E.Cause(err, "dynamic-config postgres: invalid notify channel name")
	}
	updateInterval := time.Duration(options.UpdateInterval)
	if updateInterval == 0 {
		updateInterval = time.Minute
	}
	pool, err := pgxpool.New(ctx, options.ConnectionString)
	if err != nil {
		return nil, E.Cause(err, "dynamic-config postgres: connect")
	}
	return &PostgresSource{
		logger:         logger,
		pool:           pool,
		quotedTable:    quoteIdentifier(table),
		notifyChannel:  notifyChannel,
		quotedChannel:  quoteIdentifier(notifyChannel),
		updateInterval: updateInterval,
		receiver:       receiver,
	}, nil
}

// Run starts the LISTEN loop and polling ticker. Blocks until ctx is cancelled.
func (s *PostgresSource) Run(ctx context.Context) {
	if err := s.fetchAll(ctx); err != nil {
		s.logger.Error("dynamic-config postgres initial fetch: ", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go s.listen(ctx, &wg)
	ticker := time.NewTicker(s.updateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			s.pool.Close()
			return
		case <-ticker.C:
			if err := s.fetchAll(ctx); err != nil {
				s.logger.Error("dynamic-config postgres poll fetch: ", err)
			}
		}
	}
}

// fetchAll loads all rows that have at least one non-zero config column.
func (s *PostgresSource) fetchAll(ctx context.Context) error {
	query := `SELECT name, COALESCE(upload_mbps,0), COALESCE(download_mbps,0),
	                 COALESCE(quota_gb,0), COALESCE(quota_period,''),
	                 COALESCE(quota_period_start,''), COALESCE(quota_period_days,0)
	          FROM ` + s.quotedTable + `
	          WHERE upload_mbps > 0 OR download_mbps > 0 OR quota_gb > 0`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return E.Cause(err, "dynamic-config postgres fetchAll query")
	}
	defer rows.Close()
	for rows.Next() {
		var row ConfigRow
		if err := rows.Scan(&row.User, &row.UploadMbps, &row.DownloadMbps,
			&row.QuotaGB, &row.Period, &row.PeriodStart, &row.PeriodDays); err != nil {
			return E.Cause(err, "dynamic-config postgres scan row")
		}
		if err := s.receiver.Apply(row); err != nil {
			s.logger.Error("dynamic-config postgres apply ", row.User, ": ", err)
		}
	}
	return rows.Err()
}

// fetchUser loads a single user row and calls receiver.Apply.
// If the user no longer exists in the DB, it is removed from in-memory state.
func (s *PostgresSource) fetchUser(ctx context.Context, user string) error {
	query := `SELECT COALESCE(upload_mbps,0), COALESCE(download_mbps,0),
	                 COALESCE(quota_gb,0), COALESCE(quota_period,''),
	                 COALESCE(quota_period_start,''), COALESCE(quota_period_days,0)
	          FROM ` + s.quotedTable + ` WHERE name = $1`
	row := ConfigRow{User: user}
	err := s.pool.QueryRow(ctx, query, user).Scan(
		&row.UploadMbps, &row.DownloadMbps,
		&row.QuotaGB, &row.Period, &row.PeriodStart, &row.PeriodDays,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// User deleted from DB — remove from in-memory state
			return s.receiver.Remove(user)
		}
		return E.Cause(err, "dynamic-config postgres fetchUser ", user)
	}
	return s.receiver.Apply(row)
}

func (s *PostgresSource) listen(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	backoff := time.Second
	for {
		err := s.doListen(ctx)
		if ctx.Err() != nil {
			return
		}
		s.logger.Error("dynamic-config postgres LISTEN disconnected: ", err, ", reconnecting in ", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 60*time.Second {
			backoff *= 2
		}
	}
}

func (s *PostgresSource) doListen(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return E.Cause(err, "acquire LISTEN connection")
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+s.quotedChannel); err != nil {
		return E.Cause(err, "LISTEN ", s.notifyChannel)
	}
	s.logger.Info("dynamic-config postgres: listening on ", s.notifyChannel)
	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return E.Cause(err, "wait for notification")
		}
		payload := notification.Payload
		s.logger.Debug("dynamic-config postgres notification: ", payload)
		if strings.HasPrefix(payload, "del:") {
			user := strings.TrimPrefix(payload, "del:")
			if user == "" {
				s.logger.Warn("dynamic-config postgres: received del: with empty user, ignoring")
				continue
			}
			if err := s.receiver.Remove(user); err != nil {
				s.logger.Error("dynamic-config postgres remove ", user, ": ", err)
			}
			continue
		}
		if payload == "" {
			// Unknown user — reload all
			if err := s.fetchAll(ctx); err != nil {
				s.logger.Error("dynamic-config postgres fetchAll on empty payload: ", err)
			}
			continue
		}
		if err := s.fetchUser(ctx, payload); err != nil {
			s.logger.Error("dynamic-config postgres fetchUser ", payload, ": ", err)
		}
	}
}
