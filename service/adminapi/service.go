package adminapi

import (
	"context"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	boxService "github.com/sagernet/sing-box/adapter/service"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/service/speedlimiter"
	"github.com/sagernet/sing-box/service/trafficquota"
	singservice "github.com/sagernet/sing/service"

	"github.com/go-chi/chi/v5"
)

const defaultBasePath = "/admin/v1"

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.AdminAPIServiceOptions](registry, C.TypeAdminAPI, NewService)
}

type UserProvider interface {
	ListUsers() []adapter.User
	GetUser(name string) (adapter.User, bool)
	CreateUser(user option.User) error
	UpdateUser(name string, patch UserPatch) error
	DeleteUser(name string) error
}

type QuotaController interface {
	ApplyConfig(user option.TrafficQuotaUser) error
	RemoveConfig(user string) error
	GetConfig(user string) (option.TrafficQuotaUser, bool)
	SnapshotState(user string) (trafficquota.RuntimeState, bool)
	RestoreState(state trafficquota.RuntimeState) error
	QuotaStatus(user string) (trafficquota.Status, bool)
}

type SpeedController interface {
	ApplyConfig(user option.SpeedLimiterUser) error
	RemoveConfig(user string) error
	GetConfig(user string) (option.SpeedLimiterUser, bool)
	ReplaceUserSchedules(user string, schedules []speedlimiter.UserSchedule) error
	GetUserSchedules(user string) ([]speedlimiter.UserSchedule, bool)
	RemoveUserSchedules(user string) error
}

type Service struct {
	boxService.Adapter
	ctx             context.Context
	logger          log.ContextLogger
	serviceManager  adapter.ServiceManager
	resolveOnce     sync.Once
	resolveErr      error
	authenticator   *Authenticator
	userProvider    UserProvider
	quotaController QuotaController
	speedController SpeedController
	basePath        string
	router          http.Handler
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.AdminAPIServiceOptions) (adapter.Service, error) {
	authenticator, err := NewAuthenticator(options)
	if err != nil {
		return nil, err
	}
	svc := &Service{
		Adapter:        boxService.NewAdapter(C.TypeAdminAPI, tag),
		ctx:            ctx,
		logger:         logger,
		serviceManager: singservice.FromContext[adapter.ServiceManager](ctx),
		authenticator:  authenticator,
		basePath:       normalizeBasePath(options.Path),
	}
	router := chi.NewRouter()
	router.Route(svc.basePath, func(baseRouter chi.Router) {
		baseRouter.Handle("/auth/login", postOnly(http.HandlerFunc(authenticator.LoginHandler)))
		baseRouter.Handle("/user/list", svc.authenticatedPost(http.HandlerFunc(svc.ListUsersHandler)))
		baseRouter.Handle("/user/get", svc.authenticatedPost(http.HandlerFunc(svc.GetUserHandler)))
		baseRouter.Handle("/user/create", svc.authenticatedPost(http.HandlerFunc(svc.CreateUserHandler)))
		baseRouter.Handle("/user/update", svc.authenticatedPost(http.HandlerFunc(svc.UpdateUserHandler)))
		baseRouter.Handle("/user/delete", svc.authenticatedPost(http.HandlerFunc(svc.DeleteUserHandler)))
	})
	svc.router = router
	return svc, nil
}

func (s *Service) Start(stage adapter.StartStage) error {
	if stage == adapter.StartStateInitialize {
		return s.ensureManagedServices()
	}
	return nil
}

func (s *Service) Close() error {
	return nil
}

func (s *Service) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.router.ServeHTTP(writer, request)
}

func (s *Service) ensureManagedServices() error {
	s.resolveOnce.Do(func() {
		s.userProvider, s.quotaController, s.speedController, s.resolveErr = s.resolveManagedServices()
	})
	return s.resolveErr
}

func (s *Service) resolveManagedServices() (UserProvider, QuotaController, SpeedController, error) {
	if s.serviceManager == nil {
		return nil, nil, nil, nil
	}
	var userProvider UserProvider
	var quotaController QuotaController
	var speedController SpeedController
	for _, managedService := range s.serviceManager.Services() {
		if managedService == s {
			continue
		}
		if userProvider == nil {
			if candidate, ok := managedService.(UserProvider); ok {
				userProvider = candidate
			}
		}
		if quotaController == nil {
			if candidate, ok := managedService.(QuotaController); ok {
				quotaController = candidate
			}
		}
		if speedController == nil {
			if candidate, ok := managedService.(SpeedController); ok {
				speedController = candidate
			}
		}
	}
	return userProvider, quotaController, speedController, nil
}

func normalizeBasePath(rawPath string) string {
	trimmedPath := strings.TrimSpace(rawPath)
	if trimmedPath == "" || trimmedPath == "/" {
		return defaultBasePath
	}
	if !strings.HasPrefix(trimmedPath, "/") {
		trimmedPath = "/" + trimmedPath
	}
	cleanedPath := path.Clean(trimmedPath)
	if cleanedPath == "." || cleanedPath == "/" {
		return defaultBasePath
	}
	return cleanedPath
}

func (s *Service) authenticatedPost(handler http.Handler) http.Handler {
	return postOnly(s.authenticator.Middleware(handler))
}

func postOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(writer, request)
	})
}
