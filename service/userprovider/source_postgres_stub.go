//go:build !with_postgres

package userprovider

import (
	"context"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

type PostgresSource struct{}

func NewPostgresSource(_ log.ContextLogger, _ *option.UserProviderPostgresOptions) *PostgresSource {
	return nil
}

func (s *PostgresSource) Start(_ context.Context) error {
	return nil
}

func (s *PostgresSource) CachedUsers() []option.User {
	return nil
}

func (s *PostgresSource) Run(_ context.Context, _ func()) {}

func (s *PostgresSource) Close() error {
	return nil
}
