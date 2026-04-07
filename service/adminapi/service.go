package adminapi

import (
	"net/http"
	"path"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"

	"github.com/go-chi/chi/v5"
)

const defaultBasePath = "/admin/v1"

type UserProvider interface {
	ListUsers() []adapter.User
	GetUser(name string) (adapter.User, bool)
	CreateUser(user option.User) error
	UpdateUser(name string, patch UserPatch) error
	DeleteUser(name string) error
}

type Service struct {
	authenticator *Authenticator
	userProvider  UserProvider
	basePath      string
	router        http.Handler
}

func NewService(options option.AdminAPIServiceOptions, userProvider UserProvider) (*Service, error) {
	authenticator, err := NewAuthenticator(options)
	if err != nil {
		return nil, err
	}
	service := &Service{
		authenticator: authenticator,
		userProvider:  userProvider,
		basePath:      normalizeBasePath(options.Path),
	}
	router := chi.NewRouter()
	router.Route(service.basePath, func(baseRouter chi.Router) {
		baseRouter.Handle("/auth/login", postOnly(http.HandlerFunc(authenticator.LoginHandler)))
		baseRouter.Handle("/user/list", service.authenticatedPost(http.HandlerFunc(service.ListUsersHandler)))
		baseRouter.Handle("/user/get", service.authenticatedPost(http.HandlerFunc(service.GetUserHandler)))
		baseRouter.Handle("/user/create", service.authenticatedPost(http.HandlerFunc(service.CreateUserHandler)))
		baseRouter.Handle("/user/update", service.authenticatedPost(http.HandlerFunc(service.UpdateUserHandler)))
		baseRouter.Handle("/user/delete", service.authenticatedPost(http.HandlerFunc(service.DeleteUserHandler)))
	})
	service.router = router
	return service, nil
}

func (s *Service) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.router.ServeHTTP(writer, request)
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
