//go:build !with_redis

package trafficquota

import (
	"context"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

type RedisPersister struct{}

func NewRedisPersister(context.Context, *option.TrafficQuotaRedisOptions) (*RedisPersister, error) {
	return nil, E.New("redis support requires with_redis")
}

func (p *RedisPersister) Load(string, string) (int64, error) {
	return 0, E.New("redis support requires with_redis")
}

func (p *RedisPersister) LoadAll(string) (map[string]int64, error) {
	return nil, E.New("redis support requires with_redis")
}

func (p *RedisPersister) Save(string, string, int64) error {
	return E.New("redis support requires with_redis")
}

func (p *RedisPersister) IncrBy(string, string, int64) error {
	return E.New("redis support requires with_redis")
}

func (p *RedisPersister) Delete(string, string) error {
	return E.New("redis support requires with_redis")
}

func (p *RedisPersister) Close() error {
	return nil
}
