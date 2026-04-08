//go:build with_redis

package userprovider

import (
	"context"
	"sync"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"

	"github.com/redis/go-redis/v9"
)

type RedisSource struct {
	logger         log.ContextLogger
	client         *redis.Client
	key            string
	channel        string
	updateInterval time.Duration
	access         sync.RWMutex
	cachedUsers    []option.User
}

func NewRedisSource(logger log.ContextLogger, options *option.UserProviderRedisOptions) *RedisSource {
	updateInterval := time.Duration(options.UpdateInterval)
	if updateInterval == 0 {
		updateInterval = 30 * time.Second
	}
	channel := options.Channel
	if channel == "" {
		channel = options.Key + ":notify"
	}
	client := redis.NewClient(&redis.Options{
		Addr:     options.Address,
		Username: options.Username,
		Password: options.Password,
		DB:       options.DB,
	})
	return &RedisSource{
		logger:         logger,
		client:         client,
		key:            options.Key,
		channel:        channel,
		updateInterval: updateInterval,
	}
}

func (s *RedisSource) CachedUsers() []option.User {
	s.access.RLock()
	defer s.access.RUnlock()
	return s.cachedUsers
}

func (s *RedisSource) fetch(ctx context.Context) error {
	result, err := s.client.Get(ctx, s.key).Result()
	if err != nil {
		return E.Cause(err, "redis GET ", s.key)
	}
	var users []option.User
	err = json.Unmarshal([]byte(result), &users)
	if err != nil {
		return E.Cause(err, "parse redis users")
	}
	s.access.Lock()
	s.cachedUsers = users
	s.access.Unlock()
	s.logger.Info("fetched ", len(users), " users from Redis")
	return nil
}

func (s *RedisSource) Run(ctx context.Context, onUpdate func()) {
	// Initial fetch
	err := s.fetch(ctx)
	if err != nil {
		s.logger.Error("initial Redis fetch: ", err)
	} else {
		onUpdate()
	}
	// Start Pub/Sub listener and polling in parallel
	go s.subscribe(ctx, onUpdate)
	ticker := time.NewTicker(s.updateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := s.fetch(ctx)
			if err != nil {
				s.logger.Error("Redis poll fetch: ", err)
			} else {
				onUpdate()
			}
		}
	}
}

func (s *RedisSource) subscribe(ctx context.Context, onUpdate func()) {
	for {
		err := s.doSubscribe(ctx, onUpdate)
		if ctx.Err() != nil {
			return
		}
		s.logger.Error("Redis Pub/Sub disconnected: ", err, ", reconnecting in 5s")
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (s *RedisSource) doSubscribe(ctx context.Context, onUpdate func()) error {
	pubsub := s.client.Subscribe(ctx, s.channel)
	defer pubsub.Close()
	s.logger.Info("subscribed to Redis channel: ", s.channel)
	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return E.New("Redis Pub/Sub channel closed")
			}
			s.logger.Debug("received Redis notification: ", msg.Payload)
			err := s.fetch(ctx)
			if err != nil {
				s.logger.Error("Redis fetch after notification: ", err)
			} else {
				onUpdate()
			}
		}
	}
}

func (s *RedisSource) Close() error {
	if s == nil {
		return nil
	}
	return s.client.Close()
}
