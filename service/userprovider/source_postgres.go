//go:build with_postgres

package userprovider

import (
	"context"
	"sync"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresSource struct {
	logger           log.ContextLogger
	connectionString string
	table            string
	notifyChannel    string
	updateInterval   time.Duration
	pool             *pgxpool.Pool
	access           sync.RWMutex
	cachedUsers      []option.User
}

func NewPostgresSource(logger log.ContextLogger, options *option.UserProviderPostgresOptions) *PostgresSource {
	updateInterval := time.Duration(options.UpdateInterval)
	if updateInterval == 0 {
		updateInterval = time.Minute
	}
	table := options.Table
	if table == "" {
		table = "users"
	}
	notifyChannel := options.NotifyChannel
	if notifyChannel == "" {
		notifyChannel = "user_changes"
	}
	return &PostgresSource{
		logger:           logger,
		connectionString: options.ConnectionString,
		table:            table,
		notifyChannel:    notifyChannel,
		updateInterval:   updateInterval,
	}
}

func (s *PostgresSource) Start(ctx context.Context) error {
	pool, err := pgxpool.New(ctx, s.connectionString)
	if err != nil {
		return E.Cause(err, "connect to PostgreSQL")
	}
	s.pool = pool
	return nil
}

func (s *PostgresSource) CachedUsers() []option.User {
	s.access.RLock()
	defer s.access.RUnlock()
	return s.cachedUsers
}

func (s *PostgresSource) fetch(ctx context.Context) error {
	query := "SELECT name, COALESCE(password, ''), COALESCE(uuid, ''), COALESCE(alter_id, 0), COALESCE(flow, '') FROM " + s.table
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return E.Cause(err, "query users table")
	}
	defer rows.Close()
	var users []option.User
	for rows.Next() {
		var u option.User
		err = rows.Scan(&u.Name, &u.Password, &u.UUID, &u.AlterId, &u.Flow)
		if err != nil {
			return E.Cause(err, "scan user row")
		}
		users = append(users, u)
	}
	if err = rows.Err(); err != nil {
		return E.Cause(err, "iterate user rows")
	}
	s.access.Lock()
	s.cachedUsers = users
	s.access.Unlock()
	s.logger.Info("fetched ", len(users), " users from PostgreSQL")
	return nil
}

func (s *PostgresSource) Run(ctx context.Context, onUpdate func()) {
	// Initial fetch
	err := s.fetch(ctx)
	if err != nil {
		s.logger.Error("initial PostgreSQL fetch: ", err)
	} else {
		onUpdate()
	}
	// Start LISTEN in parallel
	go s.listen(ctx, onUpdate)
	// Polling fallback
	ticker := time.NewTicker(s.updateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := s.fetch(ctx)
			if err != nil {
				s.logger.Error("PostgreSQL poll fetch: ", err)
			} else {
				onUpdate()
			}
		}
	}
}

func (s *PostgresSource) listen(ctx context.Context, onUpdate func()) {
	for {
		err := s.doListen(ctx, onUpdate)
		if ctx.Err() != nil {
			return
		}
		s.logger.Error("PostgreSQL LISTEN disconnected: ", err, ", reconnecting in 5s")
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (s *PostgresSource) doListen(ctx context.Context, onUpdate func()) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return E.Cause(err, "acquire connection for LISTEN")
	}
	defer conn.Release()
	_, err = conn.Exec(ctx, "LISTEN "+s.notifyChannel)
	if err != nil {
		return E.Cause(err, "LISTEN ", s.notifyChannel)
	}
	s.logger.Info("listening on PostgreSQL channel: ", s.notifyChannel)
	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return E.Cause(err, "wait for notification")
		}
		s.logger.Debug("received PostgreSQL notification: ", notification.Payload)
		err = s.fetch(ctx)
		if err != nil {
			s.logger.Error("PostgreSQL fetch after notification: ", err)
		} else {
			onUpdate()
		}
	}
}

func (s *PostgresSource) Close() error {
	if s == nil {
		return nil
	}
	if s.pool != nil {
		s.pool.Close()
	}
	return nil
}
