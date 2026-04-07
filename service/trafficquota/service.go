package trafficquota

import (
	"context"
	"net"
	"time"

	"github.com/sagernet/sing-box/adapter"
	boxService "github.com/sagernet/sing-box/adapter/service"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

var (
	_ adapter.ConnectionTracker = (*Service)(nil)
	_ adapter.Service           = (*Service)(nil)
)

var (
	newRedisPersisterFunc = func(ctx context.Context, options *option.TrafficQuotaRedisOptions) (Persister, error) {
		return NewRedisPersister(ctx, options)
	}
	newPostgresPersisterFunc = func(ctx context.Context, options *option.TrafficQuotaPostgresOptions) (Persister, error) {
		return NewPostgresPersister(ctx, options)
	}
)

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.TrafficQuotaServiceOptions](registry, C.TypeTrafficQuota, NewService)
}

type Service struct {
	boxService.Adapter
	ctx           context.Context
	cancel        context.CancelFunc
	logger        log.ContextLogger
	manager       *QuotaManager
	persistence   *option.TrafficQuotaPersistence
	persister     Persister
	flushInterval time.Duration
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.TrafficQuotaServiceOptions) (adapter.Service, error) {
	manager, err := NewQuotaManager(options)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	flushInterval := time.Duration(options.FlushInterval)
	if flushInterval == 0 {
		flushInterval = 30 * time.Second
	}
	return &Service{
		Adapter:       boxService.NewAdapter(C.TypeTrafficQuota, tag),
		ctx:           ctx,
		cancel:        cancel,
		logger:        logger,
		manager:       manager,
		persistence:   options.Persistence,
		flushInterval: flushInterval,
	}, nil
}

func (s *Service) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	router := service.FromContext[adapter.Router](s.ctx)
	if router == nil {
		return E.New("missing router in context")
	}
	router.AppendTracker(s)
	if err := s.initPersister(); err != nil {
		return err
	}
	s.restoreUsage()
	go s.runFlushLoop()
	go s.runPeriodResetLoop()
	return nil
}

func (s *Service) RoutedConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) net.Conn {
	if metadata.User == "" || !s.manager.HasQuota(metadata.User) {
		return conn
	}
	if s.manager.IsExceeded(metadata.User) {
		quotaConn := NewQuotaConn(conn, nil, nil)
		quotaConn.markQuotaExceeded()
		return quotaConn
	}
	quotaConn := NewQuotaConn(conn, nil, nil)
	onBytes, onClose := s.manager.RegisterConn(metadata.User, quotaConn)
	quotaConn.onBytes = onBytes
	quotaConn.onClose = onClose
	return quotaConn
}

func (s *Service) RoutedPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) N.PacketConn {
	if metadata.User == "" || !s.manager.HasQuota(metadata.User) {
		return conn
	}
	if s.manager.IsExceeded(metadata.User) {
		quotaConn := NewQuotaPacketConn(conn, nil, nil)
		quotaConn.markQuotaExceeded()
		return quotaConn
	}
	quotaConn := NewQuotaPacketConn(conn, nil, nil)
	onBytes, onClose := s.manager.RegisterConn(metadata.User, quotaConn)
	quotaConn.onBytes = onBytes
	quotaConn.onClose = onClose
	return quotaConn
}

func (s *Service) Close() error {
	s.cancel()
	var errs []error
	if flushErr := s.flushPending(); flushErr != nil {
		errs = append(errs, flushErr)
	}
	if s.persister != nil {
		if closeErr := s.persister.Close(); closeErr != nil {
			errs = append(errs, closeErr)
		}
	}
	return E.Errors(errs...)
}

func (s *Service) initPersister() error {
	if s.persister != nil {
		return nil
	}
	if s.persistence == nil {
		s.persister = NewNoopPersister()
		return nil
	}
	if s.persistence.Redis != nil {
		persister, err := newRedisPersisterFunc(s.ctx, s.persistence.Redis)
		if err != nil {
			s.logger.Warn("traffic-quota redis unavailable, falling back to memory mode: ", err)
			s.persister = NewNoopPersister()
			return nil
		}
		s.persister = persister
		return nil
	}
	if s.persistence.Postgres != nil {
		persister, err := newPostgresPersisterFunc(s.ctx, s.persistence.Postgres)
		if err != nil {
			s.logger.Warn("traffic-quota postgres unavailable, falling back to memory mode: ", err)
			s.persister = NewNoopPersister()
			return nil
		}
		s.persister = persister
		return nil
	}
	s.persister = NewNoopPersister()
	return nil
}

func (s *Service) restoreUsage() {
	if s.persister == nil {
		return
	}
	for _, user := range s.manager.Users() {
		periodKey := s.manager.CurrentPeriodKey(user)
		usage, err := s.persister.Load(user, periodKey)
		if err != nil {
			s.logger.Warn("restore traffic quota usage for ", user, ": ", err)
			continue
		}
		s.manager.LoadUsage(user, usage)
	}
}

func (s *Service) runFlushLoop() {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if err := s.flushPending(); err != nil {
				s.logger.Error("flush traffic quota usage: ", err)
			}
		}
	}
}

func (s *Service) runPeriodResetLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if err := s.handlePeriodResets(s.manager.now()); err != nil {
				s.logger.Error("reset traffic quota period: ", err)
			}
		}
	}
}

func (s *Service) flushPending() error {
	if s.persister == nil {
		return nil
	}
	pending := s.manager.ConsumePendingDeltas()
	if len(pending) == 0 {
		return nil
	}
	var errs []error
	periodUsers := make(map[string][]string)
	for user, delta := range pending {
		periodKey := s.manager.CurrentPeriodKey(user)
		if err := s.persister.IncrBy(user, periodKey, delta); err != nil {
			s.manager.RestorePendingDelta(user, delta)
			errs = append(errs, E.Cause(err, "persist traffic quota delta for ", user))
			continue
		}
		periodUsers[periodKey] = append(periodUsers[periodKey], user)
	}
	for periodKey, users := range periodUsers {
		loaded, err := s.persister.LoadAll(periodKey)
		if err != nil {
			errs = append(errs, E.Cause(err, "reload traffic quota totals for period ", periodKey))
			continue
		}
		for _, user := range users {
			if value, ok := loaded[user]; ok {
				s.manager.LoadUsage(user, value)
			}
		}
	}
	return E.Errors(errs...)
}

func (s *Service) handlePeriodResets(now time.Time) error {
	if s.persister == nil {
		return nil
	}
	resets := s.manager.CheckPeriodReset(now)
	var errs []error
	for _, reset := range resets {
		if err := s.persister.Delete(reset.User, reset.PreviousKey); err != nil {
			errs = append(errs, E.Cause(err, "delete previous traffic quota period for ", reset.User))
		}
	}
	return E.Errors(errs...)
}
