//go:build !with_redis

package userprovider

import (
	"context"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

type RedisSource struct{}

func NewRedisSource(_ log.ContextLogger, _ *option.UserProviderRedisOptions) *RedisSource {
	return nil
}

func (s *RedisSource) CachedUsers() []option.User {
	return nil
}

func (s *RedisSource) Run(_ context.Context, _ func()) {}

func (s *RedisSource) Close() error {
	return nil
}
