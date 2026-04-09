//go:build with_redis

package dynamicconfig

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

// redisMessage is the JSON payload published to the Redis channel.
type redisMessage struct {
	User         string  `json:"user"`
	UploadMbps   int     `json:"upload_mbps,omitempty"`
	DownloadMbps int     `json:"download_mbps,omitempty"`
	PerClient    *bool   `json:"per_client,omitempty"`
	QuotaGB      float64 `json:"quota_gb,omitempty"`
	Period       string  `json:"period,omitempty"`
	PeriodStart  string  `json:"period_start,omitempty"`
	PeriodDays   int     `json:"period_days,omitempty"`
	Delete       bool    `json:"delete,omitempty"`
}

// RedisSource subscribes to a Redis channel and polls a hash key for runtime
// config updates. It mirrors userprovider/source_redis.go.
type RedisSource struct {
	logger         log.ContextLogger
	client         *redis.Client
	channel        string
	hashKey        string
	updateInterval time.Duration
	receiver       *Receiver
}

// NewRedisSource creates a RedisSource. Call Run to start it.
func NewRedisSource(logger log.ContextLogger, options *option.DynamicRedisOptions, receiver *Receiver) *RedisSource {
	channel := options.Channel
	if channel == "" {
		channel = "sing-box:config:updates"
	}
	updateInterval := time.Duration(options.UpdateInterval)
	if updateInterval == 0 {
		updateInterval = 30 * time.Second
	}
	client := redis.NewClient(&redis.Options{
		Addr:     options.Address,
		Password: options.Password,
		DB:       options.DB,
	})
	return &RedisSource{
		logger:         logger,
		client:         client,
		channel:        channel,
		hashKey:        options.HashKey,
		updateInterval: updateInterval,
		receiver:       receiver,
	}
}

// Run starts the Pub/Sub listener and polling ticker. Blocks until ctx is cancelled.
func (s *RedisSource) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go s.subscribe(ctx, &wg)
	if s.hashKey != "" {
		ticker := time.NewTicker(s.updateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				wg.Wait()
				_ = s.client.Close()
				return
			case <-ticker.C:
				if err := s.pollHash(ctx); err != nil {
					s.logger.Error("dynamic-config redis hash poll: ", err)
				}
			}
		}
	} else {
		<-ctx.Done()
		wg.Wait()
		_ = s.client.Close()
	}
}

// pollHash reads all entries from the config hash and applies them.
func (s *RedisSource) pollHash(ctx context.Context) error {
	result, err := s.client.HGetAll(ctx, s.hashKey).Result()
	if err != nil {
		return E.Cause(err, "HGETALL ", s.hashKey)
	}
	for _, value := range result {
		var msg redisMessage
		if err := json.Unmarshal([]byte(value), &msg); err != nil {
			s.logger.Error("dynamic-config redis hash parse: ", err)
			continue
		}
		if msg.User == "" {
			continue
		}
		if msg.Delete {
			if err := s.receiver.Remove(msg.User); err != nil {
				s.logger.Error("dynamic-config redis remove ", msg.User, ": ", err)
			}
			continue
		}
		if err := s.receiver.Apply(ConfigRow{
			User:         msg.User,
			UploadMbps:   msg.UploadMbps,
			DownloadMbps: msg.DownloadMbps,
			PerClient:    msg.PerClient,
			QuotaGB:      msg.QuotaGB,
			Period:       msg.Period,
			PeriodStart:  msg.PeriodStart,
			PeriodDays:   msg.PeriodDays,
		}); err != nil {
			s.logger.Error("dynamic-config redis apply ", msg.User, ": ", err)
		}
	}
	return nil
}

func (s *RedisSource) subscribe(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	backoff := time.Second
	for {
		err := s.doSubscribe(ctx)
		if ctx.Err() != nil {
			return
		}
		s.logger.Error("dynamic-config redis Pub/Sub disconnected: ", err, ", reconnecting in ", backoff)
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

func (s *RedisSource) doSubscribe(ctx context.Context) error {
	pubsub := s.client.Subscribe(ctx, s.channel)
	defer pubsub.Close()
	s.logger.Info("dynamic-config redis: subscribed to ", s.channel)
	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return E.New("Redis Pub/Sub channel closed")
			}
			s.logger.Debug("dynamic-config redis notification: ", msg.Payload)
			var parsed redisMessage
			if err := json.Unmarshal([]byte(msg.Payload), &parsed); err != nil {
				s.logger.Error("dynamic-config redis parse payload: ", err)
				continue
			}
			if parsed.User == "" {
				s.logger.Error("dynamic-config redis: missing user in payload")
				continue
			}
			if parsed.Delete {
				if err := s.receiver.Remove(parsed.User); err != nil {
					s.logger.Error("dynamic-config redis remove ", parsed.User, ": ", err)
				}
				continue
			}
			if err := s.receiver.Apply(ConfigRow{
				User:         parsed.User,
				UploadMbps:   parsed.UploadMbps,
				DownloadMbps: parsed.DownloadMbps,
				PerClient:    parsed.PerClient,
				QuotaGB:      parsed.QuotaGB,
				Period:       parsed.Period,
				PeriodStart:  parsed.PeriodStart,
				PeriodDays:   parsed.PeriodDays,
			}); err != nil {
				s.logger.Error("dynamic-config redis apply ", parsed.User, ": ", err)
			}
		}
	}
}
