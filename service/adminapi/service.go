package adminapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	boxService "github.com/sagernet/sing-box/adapter/service"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/service/speedlimiter"
	"github.com/sagernet/sing-box/service/trafficquota"
	E "github.com/sagernet/sing/common/exceptions"
	singservice "github.com/sagernet/sing/service"

	"github.com/go-chi/chi/v5"
)

const defaultBasePath = "/admin/v1"

// Resource bounds for the admin-api HTTP server. Unit 8 of the
// 2026-05-14 memory-leak plan adds these to keep the management plane
// from being held open indefinitely by slow clients or held large by a
// single oversized request. Values are intentionally conservative — they
// cover normal management traffic (login, single user CRUD, schedule
// updates) with comfortable headroom but reject pathological cases.
const (
	adminAPIReadHeaderTimeout = 10 * time.Second
	adminAPIReadTimeout       = 30 * time.Second
	adminAPIIdleTimeout       = 60 * time.Second

	// adminAPIMaxRequestBytes caps the size of a single decoded request
	// body. 1 MiB is well above any realistic admin operation (a single
	// user record is well under 1 KiB; a bulk schedule update is rarely
	// larger than 10 KiB) while keeping a hostile payload from forcing
	// RSS to spike. Exposed as a variable so tests can shrink it.
	defaultAdminAPIMaxRequestBytes int64 = 1 << 20
)

// adminAPIMaxRequestBytes is the active body-size cap. Mutable so tests
// can scope down to a few hundred bytes without fabricating MiBs of
// JSON payload. Production callers never change it.
var adminAPIMaxRequestBytes = defaultAdminAPIMaxRequestBytes

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
	authenticator   *Authenticator
	userProvider    UserProvider
	quotaController QuotaController
	speedController SpeedController
	basePath        string
	listenAddr      string
	httpServer      *http.Server
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
		listenAddr:     options.Listen,
	}
	router := chi.NewRouter()
	// bodyLimitMiddleware sits in front of every handler and wraps the
	// request body so any read past adminAPIMaxRequestBytes fails with
	// "http: request body too large". The handlers already map decode
	// errors to 400 BadRequest, which keeps the response shape unchanged
	// for legitimate small requests while bounding worst-case allocation.
	router.Use(bodyLimitMiddleware)
	router.Route(svc.basePath, func(baseRouter chi.Router) {
		baseRouter.Handle("/auth/login", postOnly(http.HandlerFunc(authenticator.LoginHandler)))
		baseRouter.Handle("/user/list", svc.authenticatedPost(http.HandlerFunc(svc.ListUsersHandler)))
		baseRouter.Handle("/user/get", svc.authenticatedPost(http.HandlerFunc(svc.GetUserHandler)))
		baseRouter.Handle("/user/create", svc.authenticatedPost(http.HandlerFunc(svc.CreateUserHandler)))
		baseRouter.Handle("/user/update", svc.authenticatedPost(http.HandlerFunc(svc.UpdateUserHandler)))
		baseRouter.Handle("/user/delete", svc.authenticatedPost(http.HandlerFunc(svc.DeleteUserHandler)))
		baseRouter.Route("/quota", func(r chi.Router) {
			r.Use(authenticator.Middleware)
			r.Get("/{user}", http.HandlerFunc(svc.GetQuotaStatusHandler))
			r.Put("/{user}", http.HandlerFunc(svc.PutQuotaHandler))
			r.Delete("/{user}", http.HandlerFunc(svc.DeleteQuotaHandler))
		})
		baseRouter.Route("/speed", func(r chi.Router) {
			r.Use(authenticator.Middleware)
			r.Get("/{user}", http.HandlerFunc(svc.GetSpeedHandler))
			r.Put("/{user}", http.HandlerFunc(svc.PutSpeedHandler))
			r.Delete("/{user}", http.HandlerFunc(svc.DeleteSpeedHandler))
			r.Get("/{user}/schedules", http.HandlerFunc(svc.GetSpeedSchedulesHandler))
			r.Put("/{user}/schedules", http.HandlerFunc(svc.PutSpeedSchedulesHandler))
			r.Delete("/{user}/schedules", http.HandlerFunc(svc.DeleteSpeedSchedulesHandler))
		})
	})
	svc.router = router
	return svc, nil
}

func (s *Service) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	s.userProvider, s.quotaController, s.speedController = s.resolveManagedServices()
	if s.listenAddr != "" {
		ln, err := net.Listen("tcp", s.listenAddr)
		if err != nil {
			return E.Cause(err, "admin-api listen")
		}
		s.httpServer = &http.Server{
			Handler:           s.router,
			ReadHeaderTimeout: adminAPIReadHeaderTimeout,
			ReadTimeout:       adminAPIReadTimeout,
			IdleTimeout:       adminAPIIdleTimeout,
			// WriteTimeout deliberately unset: streaming or large list
			// responses must not be killed mid-flight by a server-side
			// deadline. The body-limit middleware handles inbound
			// resource bounds; outbound bounds are left to per-handler
			// logic.
		}
		s.logger.Info("admin-api listening on ", s.listenAddr)
		go func() {
			if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.logger.Error("admin-api serve: ", err)
			}
		}()
	}
	return nil
}

func (s *Service) Close() error {
	if s.httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(shutdownCtx)
	}
	return nil
}

func (s *Service) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.router.ServeHTTP(writer, request)
}

// ensureManagedServices is a no-op kept for call-site compatibility.
// Fields are set once in Start(StartStateStart) before the HTTP server
// begins accepting connections, so handlers only read them — no lock needed.
func (s *Service) ensureManagedServices() {}

func (s *Service) resolveManagedServices() (UserProvider, QuotaController, SpeedController) {
	if s.serviceManager == nil {
		return nil, nil, nil
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
	return userProvider, quotaController, speedController
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

// bodyLimitMiddleware wraps r.Body with http.MaxBytesReader so every
// handler downstream sees the same enforced ceiling. The wrapper closes
// the underlying connection on overflow (a hint to misbehaving clients)
// and surfaces the standard "http: request body too large" error to
// callers, which our decoders already map to 400.
func bodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Body != nil && request.Body != http.NoBody {
			request.Body = http.MaxBytesReader(writer, request.Body, adminAPIMaxRequestBytes)
		}
		next.ServeHTTP(writer, request)
	})
}
