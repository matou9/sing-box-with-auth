package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

func TestAdminAuthLoginReturnsSignedToken(t *testing.T) {
	auth, err := NewAuthenticator(option.AdminAPIServiceOptions{
		Listen:      "127.0.0.1:8080",
		Path:        "/admin",
		TokenSecret: "super-secret",
		TokenTTL:    badoption.Duration(5 * time.Minute),
		Admins: []option.AdminCredential{
			{Username: "alice", Password: "wonderland"},
		},
	})
	if err != nil {
		t.Fatalf("NewAuthenticator returned error: %v", err)
	}
	auth.now = func() time.Time {
		return time.Unix(1_700_000_000, 0)
	}

	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"alice","password":"wonderland"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	auth.LoginHandler(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Token == "" {
		t.Fatal("expected token in login response")
	}
	if !strings.Contains(response.Token, ".") {
		t.Fatalf("expected signed token format, got %q", response.Token)
	}

	subject, err := auth.validateBearerToken(response.Token)
	if err != nil {
		t.Fatalf("validateBearerToken returned error: %v", err)
	}
	if subject.Username != "alice" {
		t.Fatalf("unexpected username: %q", subject.Username)
	}
}

func TestAdminAuthMiddlewareAcceptsStaticBearerToken(t *testing.T) {
	auth, err := NewAuthenticator(option.AdminAPIServiceOptions{
		Tokens: []string{"static-token"},
	})
	if err != nil {
		t.Fatalf("NewAuthenticator returned error: %v", err)
	}

	var called bool
	protected := auth.Middleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		called = true
		writer.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer static-token")
	recorder := httptest.NewRecorder()

	protected.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !called {
		t.Fatal("expected protected handler to be called")
	}
}

func TestAdminAuthMiddlewareRejectsExpiredLoginToken(t *testing.T) {
	auth, err := NewAuthenticator(option.AdminAPIServiceOptions{
		TokenSecret: "super-secret",
		TokenTTL:    badoption.Duration(time.Second),
		Admins: []option.AdminCredential{
			{Username: "alice", Password: "wonderland"},
		},
	})
	if err != nil {
		t.Fatalf("NewAuthenticator returned error: %v", err)
	}
	baseTime := time.Unix(1_700_000_000, 0)
	auth.now = func() time.Time {
		return baseTime
	}
	token, err := auth.issueLoginToken("alice")
	if err != nil {
		t.Fatalf("issueLoginToken returned error: %v", err)
	}
	auth.now = func() time.Time {
		return baseTime.Add(2 * time.Second)
	}

	protected := auth.Middleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	protected.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}
}
