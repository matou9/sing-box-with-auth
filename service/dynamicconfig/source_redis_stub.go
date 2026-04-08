//go:build !with_redis

package dynamicconfig

import (
	"context"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

type RedisSource struct{}

func NewRedisSource(_ log.ContextLogger, _ *option.DynamicRedisOptions, _ *Receiver) *RedisSource {
	return &RedisSource{}
}

func (s *RedisSource) Run(_ context.Context) {}
