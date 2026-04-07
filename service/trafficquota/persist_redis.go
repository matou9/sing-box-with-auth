//go:build with_redis

package trafficquota

import (
	"context"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/redis/go-redis/v9"
)

type RedisPersister struct {
	ctx       context.Context
	client    *redis.Client
	keyPrefix string
}

func NewRedisPersister(ctx context.Context, options *option.TrafficQuotaRedisOptions) (*RedisPersister, error) {
	if options == nil {
		return nil, E.New("missing redis options")
	}
	client := redis.NewClient(&redis.Options{
		Addr:     options.Address,
		Username: options.Username,
		Password: options.Password,
		DB:       options.DB,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, E.Cause(err, "ping redis")
	}
	return &RedisPersister{
		ctx:       ctx,
		client:    client,
		keyPrefix: normalizeRedisPrefix(options.KeyPrefix),
	}, nil
}

func (p *RedisPersister) Load(user, periodKey string) (int64, error) {
	value, err := p.client.Get(p.ctx, p.key(user, periodKey)).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, E.Cause(err, "redis GET ", p.key(user, periodKey))
	}
	parsed, parseErr := strconv.ParseInt(value, 10, 64)
	if parseErr != nil {
		return 0, E.Cause(parseErr, "parse redis quota value")
	}
	return parsed, nil
}

func (p *RedisPersister) LoadAll(periodKey string) (map[string]int64, error) {
	pattern := p.keyPrefix + ":*:" + periodKey
	result := make(map[string]int64)
	var cursor uint64
	for {
		keys, nextCursor, err := p.client.Scan(p.ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, E.Cause(err, "redis SCAN ", pattern)
		}
		for _, key := range keys {
			value, loadErr := p.client.Get(p.ctx, key).Result()
			if loadErr == redis.Nil {
				continue
			}
			if loadErr != nil {
				return nil, E.Cause(loadErr, "redis GET ", key)
			}
			parsed, parseErr := strconv.ParseInt(value, 10, 64)
			if parseErr != nil {
				return nil, E.Cause(parseErr, "parse redis quota value")
			}
			result[p.userFromKey(key, periodKey)] = parsed
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return result, nil
}

func (p *RedisPersister) Save(user, periodKey string, bytes int64) error {
	return p.client.Set(p.ctx, p.key(user, periodKey), bytes, 0).Err()
}

func (p *RedisPersister) IncrBy(user, periodKey string, delta int64) error {
	return p.client.IncrBy(p.ctx, p.key(user, periodKey), delta).Err()
}

func (p *RedisPersister) Delete(user, periodKey string) error {
	return p.client.Del(p.ctx, p.key(user, periodKey)).Err()
}

func (p *RedisPersister) Close() error {
	return p.client.Close()
}

func (p *RedisPersister) key(user, periodKey string) string {
	return p.keyPrefix + ":" + user + ":" + periodKey
}

func (p *RedisPersister) userFromKey(key, periodKey string) string {
	trimmed := strings.TrimPrefix(key, p.keyPrefix+":")
	return strings.TrimSuffix(trimmed, ":"+periodKey)
}

func normalizeRedisPrefix(prefix string) string {
	if prefix == "" {
		return "traffic-quota"
	}
	return strings.TrimSuffix(prefix, ":")
}
