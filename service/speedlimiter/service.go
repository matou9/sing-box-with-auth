package speedlimiter

import (
	"context"
	"net"

	"github.com/sagernet/sing-box/adapter"
	boxService "github.com/sagernet/sing-box/adapter/service"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

var _ adapter.ConnectionTracker = (*Service)(nil)

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.SpeedLimiterServiceOptions](registry, C.TypeSpeedLimiter, NewService)
}

type Service struct {
	boxService.Adapter
	ctx     context.Context
	cancel  context.CancelFunc
	logger  log.ContextLogger
	manager *LimiterManager
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.SpeedLimiterServiceOptions) (adapter.Service, error) {
	manager, err := NewLimiterManager(options)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Service{
		Adapter: boxService.NewAdapter(C.TypeSpeedLimiter, tag),
		ctx:     ctx,
		cancel:  cancel,
		logger:  logger,
		manager: manager,
	}, nil
}

func (s *Service) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	// Register as ConnectionTracker with the router
	router := service.FromContext[adapter.Router](s.ctx)
	router.AppendTracker(s)
	s.logger.Info("speed-limiter registered as connection tracker")

	// Start schedule loop
	s.manager.StartScheduleLoop(s.ctx)
	return nil
}

func (s *Service) Close() error {
	s.cancel()
	return nil
}

func (s *Service) RoutedConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) net.Conn {
	user := metadata.User
	if user == "" {
		return conn
	}
	ul := s.manager.GetOrCreateLimiter(user)
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
	ul := s.manager.GetOrCreateLimiter(user)
	if ul == nil {
		return conn
	}
	return NewThrottledPacketConn(ctx, conn, ul.Upload, ul.Download)
}
