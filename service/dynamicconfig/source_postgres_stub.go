//go:build !with_postgres

package dynamicconfig

import (
	"context"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

type PostgresSource struct{}

func NewPostgresSource(_ context.Context, _ log.ContextLogger, _ *option.DynamicPostgresOptions, _ *Receiver) (*PostgresSource, error) {
	return nil, E.New("sing-box not built with Postgres support (with_postgres build tag required)")
}

func (s *PostgresSource) Run(_ context.Context) {}
