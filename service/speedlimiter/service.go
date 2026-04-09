package speedlimiter

import (
	"context"
	"net"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	boxService "github.com/sagernet/sing-box/adapter/service"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/service/dynamicconfig"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

var _ adapter.ConnectionTracker = (*Service)(nil)

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.SpeedLimiterServiceOptions](registry, C.TypeSpeedLimiter, NewService)
}

type Service struct {
	boxService.Adapter
	ctx            context.Context
	cancel         context.CancelFunc
	logger         log.ContextLogger
	manager        *LimiterManager
	dynamicOptions *option.DynamicConfigOptions
	applyMu        sync.Mutex
	wg             sync.WaitGroup
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.SpeedLimiterServiceOptions) (adapter.Service, error) {
	manager, err := NewLimiterManager(options)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Service{
		Adapter:        boxService.NewAdapter(C.TypeSpeedLimiter, tag),
		ctx:            ctx,
		cancel:         cancel,
		logger:         logger,
		manager:        manager,
		dynamicOptions: options.Dynamic,
	}, nil
}

func (s *Service) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	// Register as ConnectionTracker with the router
	router := service.FromContext[adapter.Router](s.ctx)
	if router == nil {
		return E.New("missing router in context")
	}
	router.AppendTracker(s)
	s.logger.Info("speed-limiter registered as connection tracker")

	// Start schedule loop
	s.manager.StartScheduleLoop(s.ctx)
	s.startDynamicSources()
	return nil
}

func (s *Service) startDynamicSources() {
	if s.dynamicOptions == nil {
		return
	}
	receiver := &dynamicconfig.Receiver{
		Apply:  s.applyDynamic,
		Remove: s.removeDynamic,
	}
	if s.dynamicOptions.Postgres != nil {
		source, err := dynamicconfig.NewPostgresSource(s.ctx, s.logger, s.dynamicOptions.Postgres, receiver)
		if err != nil {
			s.logger.Warn("speed-limiter dynamic postgres unavailable: ", err)
		} else {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				source.Run(s.ctx)
			}()
		}
	}
	if s.dynamicOptions.Redis != nil {
		source := dynamicconfig.NewRedisSource(s.logger, s.dynamicOptions.Redis, receiver)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			source.Run(s.ctx)
		}()
	}
}

func (s *Service) applyDynamic(row dynamicconfig.ConfigRow) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	return s.manager.ApplyConfig(option.SpeedLimiterUser{
		Name:         row.User,
		UploadMbps:   row.UploadMbps,
		DownloadMbps: row.DownloadMbps,
		PerClient:    row.PerClient,
	})
}

func (s *Service) removeDynamic(user string) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	return s.manager.RemoveConfig(user)
}

func (s *Service) Close() error {
	s.cancel()
	s.wg.Wait()
	return nil
}

func (s *Service) ApplyConfig(user option.SpeedLimiterUser) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	return s.manager.ApplyConfig(user)
}

func (s *Service) RemoveConfig(user string) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	return s.manager.RemoveConfig(user)
}

func (s *Service) GetConfig(user string) (option.SpeedLimiterUser, bool) {
	return s.manager.GetConfig(user)
}

func (s *Service) CurrentSpeed(user string) (int, int, bool) {
	return s.manager.CurrentSpeed(user)
}

func (s *Service) ReplaceUserSchedules(user string, schedules []UserSchedule) error {
	return s.manager.ReplaceUserSchedules(user, schedules)
}

func (s *Service) RemoveUserSchedules(user string) error {
	return s.manager.RemoveUserSchedules(user)
}

func (s *Service) GetUserSchedules(user string) ([]UserSchedule, bool) {
	return s.manager.GetUserSchedules(user)
}

func (s *Service) RoutedConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) net.Conn {
	user := metadata.User
	if user == "" {
		return conn
	}
	ul := s.manager.GetOrCreateLimiterForClient(user, metadata.Source.Addr)
	if ul == nil {
		return conn
	}
	return NewThrottledConn(ctx, conn, ul.Upload, ul.Download)
}

func (s *Service) RoutedPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) N.PacketConn {
	user := metadata.User
	if user == "" {
		return conn
	}
	ul := s.manager.GetOrCreateLimiterForClient(user, metadata.Source.Addr)
	if ul == nil {
		return conn
	}
	return NewThrottledPacketConn(ctx, conn, ul.Upload, ul.Download)
}
