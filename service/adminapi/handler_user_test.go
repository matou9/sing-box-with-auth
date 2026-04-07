package adminapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
)

type stubUserProvider struct {
	created []option.User
}

func (s *stubUserProvider) ListUsers() []adapter.User {
	return nil
}

func (s *stubUserProvider) GetUser(name string) (adapter.User, bool) {
	return adapter.User{}, false
}

func (s *stubUserProvider) CreateUser(user option.User) error {
	s.created = append(s.created, user)
	return nil
}

func (s *stubUserProvider) UpdateUser(name string, patch UserPatch) error {
	return nil
}

func (s *stubUserProvider) DeleteUser(name string) error {
	return nil
}

func TestAdminAPIUserCreateRequiresAuthentication(t *testing.T) {
	service, err := NewService(option.AdminAPIServiceOptions{
		TokenSecret: "super-secret",
		Admins: []option.AdminCredential{
			{Username: "alice", Password: "wonderland"},
		},
	}, &stubUserProvider{})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/user/create", strings.NewReader(`{"name":"sekai","password":"password"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	service.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminAPIUserCreateUsesBearerToken(t *testing.T) {
	provider := &stubUserProvider{}
	service, err := NewService(option.AdminAPIServiceOptions{
		TokenSecret: "super-secret",
		Admins: []option.AdminCredential{
			{Username: "alice", Password: "wonderland"},
		},
	}, provider)
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}
	service.authenticator.now = func() time.Time {
		return time.Unix(1_700_000_000, 0)
	}

	token, err := service.authenticator.issueLoginToken("alice")
	if err != nil {
		t.Fatalf("issueLoginToken returned error: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/user/create", strings.NewReader(`{"name":"sekai","password":"password"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	service.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(provider.created) != 1 {
		t.Fatalf("unexpected create count: %d", len(provider.created))
	}
	createdUser := provider.created[0]
	if createdUser.Name != "sekai" {
		t.Fatalf("unexpected created name: %q", createdUser.Name)
	}
	if createdUser.Password != "password" {
		t.Fatalf("unexpected created password: %q", createdUser.Password)
	}
}
