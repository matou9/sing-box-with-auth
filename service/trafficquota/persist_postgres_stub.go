//go:build !with_postgres

package trafficquota

import (
	"context"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

type PostgresPersister struct{}

func NewPostgresPersister(context.Context, *option.TrafficQuotaPostgresOptions) (*PostgresPersister, error) {
	return nil, E.New("postgres support requires with_postgres")
}

func (p *PostgresPersister) Load(string, string) (int64, error) {
	return 0, E.New("postgres support requires with_postgres")
}

func (p *PostgresPersister) LoadAll(string) (map[string]int64, error) {
	return nil, E.New("postgres support requires with_postgres")
}

func (p *PostgresPersister) Save(string, string, int64) error {
	return E.New("postgres support requires with_postgres")
}

func (p *PostgresPersister) IncrBy(string, string, int64) error {
	return E.New("postgres support requires with_postgres")
}

func (p *PostgresPersister) Delete(string, string) error {
	return E.New("postgres support requires with_postgres")
}

func (p *PostgresPersister) Close() error {
	return nil
}
