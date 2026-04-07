package userprovider

import (
	"context"
	"sort"
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
	overlayUsers   map[string]option.User
	deletedUsers   map[string]struct{}
	fileSource     *FileSource
	httpSource     *HTTPSource
	redisSource    *RedisSource
	postgresSource *PostgresSource
	access         sync.Mutex
	allUsers       []adapter.User
}

type UserPatch struct {
	Password *string
	UUID     *string
	AlterId  *int
	Flow     *string
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
		overlayUsers:   make(map[string]option.User),
		deletedUsers:   make(map[string]struct{}),
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
	return s.loadAndPushLocked()
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

func (s *Service) ListUsers() []adapter.User {
	s.access.Lock()
	defer s.access.Unlock()

	users := make([]adapter.User, len(s.allUsers))
	copy(users, s.allUsers)
	return users
}

func (s *Service) GetUser(name string) (adapter.User, bool) {
	s.access.Lock()
	defer s.access.Unlock()

	for _, user := range s.allUsers {
		if user.Name == name {
			return user, true
		}
	}
	return adapter.User{}, false
}

func (s *Service) CreateUser(user option.User) error {
	if user.Name == "" {
		return E.New("missing name")
	}

	s.access.Lock()
	defer s.access.Unlock()

	users, err := s.mergedUsersLocked()
	if err != nil {
		return err
	}
	for _, existingUser := range users {
		if existingUser.Name == user.Name {
			return E.New("user ", user.Name, " already exists")
		}
	}
	s.ensureOverlayUsersLocked()
	s.ensureDeletedUsersLocked()
	delete(s.deletedUsers, user.Name)
	s.overlayUsers[user.Name] = user
	return s.loadAndPushLocked()
}

func (s *Service) UpdateUser(name string, patch UserPatch) error {
	s.access.Lock()
	defer s.access.Unlock()

	users, err := s.mergedUsersLocked()
	if err != nil {
		return err
	}
	var (
		currentUser adapter.User
		exists      bool
	)
	for _, user := range users {
		if user.Name == name {
			currentUser = user
			exists = true
			break
		}
	}
	if !exists {
		return E.New("user ", name, " not found")
	}

	updatedUser := adapterUserToOption(currentUser)
	if patch.Password != nil {
		updatedUser.Password = *patch.Password
	}
	if patch.UUID != nil {
		updatedUser.UUID = *patch.UUID
	}
	if patch.AlterId != nil {
		updatedUser.AlterId = *patch.AlterId
	}
	if patch.Flow != nil {
		updatedUser.Flow = *patch.Flow
	}

	s.ensureOverlayUsersLocked()
	s.ensureDeletedUsersLocked()
	delete(s.deletedUsers, name)
	s.overlayUsers[name] = updatedUser
	return s.loadAndPushLocked()
}

func (s *Service) DeleteUser(name string) error {
	s.access.Lock()
	defer s.access.Unlock()

	users, err := s.mergedUsersLocked()
	if err != nil {
		return err
	}
	var exists bool
	for _, user := range users {
		if user.Name == name {
			exists = true
			break
		}
	}
	if !exists {
		return E.New("user ", name, " not found")
	}
	sourceUsers, err := s.sourceUsersLocked()
	if err != nil {
		return err
	}
	var sourceExists bool
	for _, user := range sourceUsers {
		if user.Name == name {
			sourceExists = true
			break
		}
	}
	delete(s.overlayUsers, name)
	s.ensureDeletedUsersLocked()
	if sourceExists {
		s.deletedUsers[name] = struct{}{}
	} else {
		delete(s.deletedUsers, name)
	}
	return s.loadAndPushLocked()
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

func adapterUserToOption(user adapter.User) option.User {
	return option.User{
		Name:     user.Name,
		Password: user.Password,
		UUID:     user.UUID,
		AlterId:  user.AlterId,
		Flow:     user.Flow,
	}
}

func (s *Service) loadAndPushLocked() error {
	allUsers, err := s.mergedUsersLocked()
	if err != nil {
		return err
	}
	s.allUsers = allUsers
	return s.pushUsers(allUsers)
}

func (s *Service) mergedUsersLocked() ([]adapter.User, error) {
	sourceUsers, err := s.sourceUsersLocked()
	if err != nil {
		return nil, err
	}

	mergedUsers := make(map[string]adapter.User, len(sourceUsers)+len(s.overlayUsers))
	userNames := make([]string, 0, len(sourceUsers)+len(s.overlayUsers))
	for _, user := range sourceUsers {
		if s.isDeletedLocked(user.Name) {
			continue
		}
		if _, exists := mergedUsers[user.Name]; !exists {
			userNames = append(userNames, user.Name)
		}
		mergedUsers[user.Name] = user
	}

	overlayOnlyNames := make([]string, 0, len(s.overlayUsers))
	for name, user := range s.overlayUsers {
		if _, exists := mergedUsers[name]; !exists {
			overlayOnlyNames = append(overlayOnlyNames, name)
		}
		mergedUsers[name] = optionUserToAdapter(user)
	}
	sort.Strings(overlayOnlyNames)
	userNames = append(userNames, overlayOnlyNames...)

	allUsers := make([]adapter.User, 0, len(userNames))
	for _, name := range userNames {
		allUsers = append(allUsers, mergedUsers[name])
	}
	return allUsers, nil
}

func (s *Service) sourceUsersLocked() ([]adapter.User, error) {
	var allUsers []adapter.User

	for _, user := range s.inlineUsers {
		allUsers = append(allUsers, optionUserToAdapter(user))
	}
	if s.fileSource != nil {
		fileUsers, err := s.fileSource.Load()
		if err != nil {
			return nil, E.Cause(err, "load file source")
		}
		for _, user := range fileUsers {
			allUsers = append(allUsers, optionUserToAdapter(user))
		}
	}
	if s.httpSource != nil {
		for _, user := range s.httpSource.CachedUsers() {
			allUsers = append(allUsers, optionUserToAdapter(user))
		}
	}
	if s.redisSource != nil {
		for _, user := range s.redisSource.CachedUsers() {
			allUsers = append(allUsers, optionUserToAdapter(user))
		}
	}
	if s.postgresSource != nil {
		for _, user := range s.postgresSource.CachedUsers() {
			allUsers = append(allUsers, optionUserToAdapter(user))
		}
	}

	return allUsers, nil
}

func (s *Service) ensureOverlayUsersLocked() {
	if s.overlayUsers == nil {
		s.overlayUsers = make(map[string]option.User)
	}
}

func (s *Service) ensureDeletedUsersLocked() {
	if s.deletedUsers == nil {
		s.deletedUsers = make(map[string]struct{})
	}
}

func (s *Service) isDeletedLocked(name string) bool {
	_, deleted := s.deletedUsers[name]
	return deleted
}
