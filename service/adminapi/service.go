package adminapi

import (
	"net/http"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"

	"github.com/go-chi/chi/v5"
)

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
	}
	router := chi.NewRouter()
	router.Post("/auth/login", authenticator.LoginHandler)
	router.With(authenticator.Middleware).Route("/user", func(userRouter chi.Router) {
		userRouter.Post("/list", service.ListUsersHandler)
		userRouter.Post("/get", service.GetUserHandler)
		userRouter.Post("/create", service.CreateUserHandler)
		userRouter.Post("/update", service.UpdateUserHandler)
		userRouter.Post("/delete", service.DeleteUserHandler)
	})
	service.router = router
	return service, nil
}

func (s *Service) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.router.ServeHTTP(writer, request)
}
