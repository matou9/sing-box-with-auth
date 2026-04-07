package adminapi

import (
	"encoding/base64"
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
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Token == "" {
		t.Fatal("expected token in login response")
	}
	if response.ExpiresAt != "2023-11-14T22:18:20Z" {
		t.Fatalf("unexpected expires_at: %q", response.ExpiresAt)
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

func TestAdminAuthLoginUsesDefaultTokenTTLWhenUnspecified(t *testing.T) {
	auth, err := NewAuthenticator(option.AdminAPIServiceOptions{
		TokenSecret: "super-secret",
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

	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"alice","password":"wonderland"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	auth.LoginHandler(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	expectedExpiry := baseTime.Add(12 * time.Hour).UTC().Format(time.RFC3339)
	if response.ExpiresAt != expectedExpiry {
		t.Fatalf("unexpected expires_at: got %q want %q", response.ExpiresAt, expectedExpiry)
	}
}

func TestAdminAuthLoginReturnsServiceUnavailableWhenLoginDisabled(t *testing.T) {
	auth, err := NewAuthenticator(option.AdminAPIServiceOptions{
		Admins: []option.AdminCredential{
			{Username: "alice", Password: "wonderland"},
		},
	})
	if err != nil {
		t.Fatalf("NewAuthenticator returned error: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"alice","password":"wonderland"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	auth.LoginHandler(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminAuthNewAuthenticatorRejectsNegativeTokenTTL(t *testing.T) {
	_, err := NewAuthenticator(option.AdminAPIServiceOptions{
		TokenSecret: "super-secret",
		TokenTTL:    badoption.Duration(-1 * time.Second),
		Admins: []option.AdminCredential{
			{Username: "alice", Password: "wonderland"},
		},
	})
	if err == nil {
		t.Fatal("expected error for negative token TTL")
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

func TestAdminAuthMiddlewareAcceptsBasicAuthCredentials(t *testing.T) {
	auth, err := NewAuthenticator(option.AdminAPIServiceOptions{
		Admins: []option.AdminCredential{
			{Username: "alice", Password: "wonderland"},
		},
	})
	if err != nil {
		t.Fatalf("NewAuthenticator returned error: %v", err)
	}

	var subject AuthSubject
	protected := auth.Middleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var loaded bool
		subject, loaded = SubjectFromContext(request.Context())
		if !loaded {
			t.Fatal("expected subject in context")
		}
		writer.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.SetBasicAuth("alice", "wonderland")
	recorder := httptest.NewRecorder()

	protected.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	if subject.Username != "alice" {
		t.Fatalf("unexpected subject username: %q", subject.Username)
	}
	if subject.StaticToken {
		t.Fatal("basic auth subject should not be marked as static token")
	}
}

func TestAdminAuthMiddlewareRejectsInvalidBasicAuthCredentials(t *testing.T) {
	auth, err := NewAuthenticator(option.AdminAPIServiceOptions{
		Admins: []option.AdminCredential{
			{Username: "alice", Password: "wonderland"},
		},
	})
	if err != nil {
		t.Fatalf("NewAuthenticator returned error: %v", err)
	}

	protected := auth.Middleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("alice:wrong-password")))
	recorder := httptest.NewRecorder()

	protected.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminAuthMiddlewarePopulatesContextForSignedLoginToken(t *testing.T) {
	auth, err := NewAuthenticator(option.AdminAPIServiceOptions{
		TokenSecret: "super-secret",
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

	var subject AuthSubject
	protected := auth.Middleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var loaded bool
		subject, loaded = SubjectFromContext(request.Context())
		if !loaded {
			t.Fatal("expected subject in context")
		}
		writer.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	protected.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	if subject.Username != "alice" {
		t.Fatalf("unexpected subject username: %q", subject.Username)
	}
	if subject.StaticToken {
		t.Fatal("signed login token subject should not be marked as static token")
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
