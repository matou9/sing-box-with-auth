package userprovider

import (
	"context"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	boxService "github.com/sagernet/sing-box/adapter/service"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
)

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.UserProviderServiceOptions](registry, C.TypeUserProvider, NewService)
}

type Service struct {
	boxService.Adapter
	ctx            context.Context
	cancel         context.CancelFunc
	logger         log.ContextLogger
	inboundTags    []string
	inboundManager adapter.InboundManager
	servers        []adapter.ManagedUserServer
	inlineUsers    []option.User
	fileSource     *FileSource
	httpSource     *HTTPSource
	redisSource    *RedisSource
	postgresSource *PostgresSource
	access         sync.Mutex
	allUsers       []adapter.User
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.UserProviderServiceOptions) (adapter.Service, error) {
	if len(options.Inbounds) == 0 {
		return nil, E.New("missing inbounds")
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Service{
		Adapter:        boxService.NewAdapter(C.TypeUserProvider, tag),
		ctx:            ctx,
		cancel:         cancel,
		logger:         logger,
		inboundTags:    options.Inbounds,
		inboundManager: service.FromContext[adapter.InboundManager](ctx),
		inlineUsers:    options.Users,
	}
	if options.File != nil {
		s.fileSource = NewFileSource(logger, options.File)
	}
	if options.HTTP != nil {
		s.httpSource = NewHTTPSource(ctx, logger, options.HTTP)
	}
	if options.Redis != nil {
		s.redisSource = NewRedisSource(logger, options.Redis)
	}
	if options.Postgres != nil {
		s.postgresSource = NewPostgresSource(logger, options.Postgres)
	}
	return s, nil
}

func (s *Service) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	// Resolve inbound tags to ManagedUserServer interfaces
	for _, tag := range s.inboundTags {
		inbound, loaded := s.inboundManager.Get(tag)
		if !loaded {
			return E.New("inbound ", tag, " not found")
		}
		managedServer, isManaged := inbound.(adapter.ManagedUserServer)
		if !isManaged {
			return E.New("inbound/", inbound.Type(), "[", tag, "] does not support user-provider")
		}
		s.servers = append(s.servers, managedServer)
	}
	// Initial load from all sources and push
	err := s.loadAndPush()
	if err != nil {
		return E.Cause(err, "initial user load")
	}
	// Start PostgreSQL connection pool (must happen before loadAndPush for initial fetch)
	if s.postgresSource != nil {
		err = s.postgresSource.Start(s.ctx)
		if err != nil {
			return E.Cause(err, "start PostgreSQL source")
		}
	}
	// Start polling/subscription loops
	if s.fileSource != nil {
		go s.fileSource.Run(s.ctx, s.onSourceUpdate)
	}
	if s.httpSource != nil {
		go s.httpSource.Run(s.ctx, s.onSourceUpdate)
	}
	if s.redisSource != nil {
		go s.redisSource.Run(s.ctx, s.onSourceUpdate)
	}
	if s.postgresSource != nil {
		go s.postgresSource.Run(s.ctx, s.onSourceUpdate)
	}
	return nil
}

func (s *Service) onSourceUpdate() {
	err := s.loadAndPush()
	if err != nil {
		s.logger.Error("update users: ", err)
	}
}

func (s *Service) loadAndPush() error {
	s.access.Lock()
	defer s.access.Unlock()
	var allUsers []adapter.User
	// Inline users
	for _, u := range s.inlineUsers {
		allUsers = append(allUsers, optionUserToAdapter(u))
	}
	// File users
	if s.fileSource != nil {
		fileUsers, err := s.fileSource.Load()
		if err != nil {
			return E.Cause(err, "load file source")
		}
		for _, u := range fileUsers {
			allUsers = append(allUsers, optionUserToAdapter(u))
		}
	}
	// HTTP users
	if s.httpSource != nil {
		httpUsers := s.httpSource.CachedUsers()
		for _, u := range httpUsers {
			allUsers = append(allUsers, optionUserToAdapter(u))
		}
	}
	// Redis users
	if s.redisSource != nil {
		redisUsers := s.redisSource.CachedUsers()
		for _, u := range redisUsers {
			allUsers = append(allUsers, optionUserToAdapter(u))
		}
	}
	// PostgreSQL users
	if s.postgresSource != nil {
		pgUsers := s.postgresSource.CachedUsers()
		for _, u := range pgUsers {
			allUsers = append(allUsers, optionUserToAdapter(u))
		}
	}
	s.allUsers = allUsers
	return s.pushUsers(allUsers)
}

func (s *Service) pushUsers(users []adapter.User) error {
	var errs []error
	for _, server := range s.servers {
		err := server.ReplaceUsers(users)
		if err != nil {
			errs = append(errs, E.Cause(err, "push to inbound/", server.Type(), "[", server.Tag(), "]"))
		}
	}
	if len(errs) > 0 {
		return E.Errors(errs...)
	}
	s.logger.Info("pushed ", len(users), " users to ", len(s.servers), " inbound(s)")
	return nil
}

func (s *Service) Close() error {
	s.cancel()
	return common.Close(s.httpSource, s.redisSource, s.postgresSource)
}

func optionUserToAdapter(u option.User) adapter.User {
	return adapter.User{
		Name:     u.Name,
		Password: u.Password,
		UUID:     u.UUID,
		AlterId:  u.AlterId,
		Flow:     u.Flow,
	}
}
