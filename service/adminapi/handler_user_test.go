package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/service/speedlimiter"
	"github.com/sagernet/sing-box/service/trafficquota"
	singservice "github.com/sagernet/sing/service"
)

type stubUpdateCall struct {
	name  string
	patch UserPatch
}

type stubUserProvider struct {
	listUsers []adapter.User
	users     map[string]adapter.User

	createErr error
	updateErr error
	deleteErr error

	created []option.User
	updated []stubUpdateCall
	deleted []string
}

func (s *stubUserProvider) Start(stage adapter.StartStage) error {
	return nil
}

func (s *stubUserProvider) Close() error {
	return nil
}

func (s *stubUserProvider) Type() string {
	return "stub-user-provider"
}

func (s *stubUserProvider) Tag() string {
	return "stub-user-provider"
}

func (s *stubUserProvider) ListUsers() []adapter.User {
	users := make([]adapter.User, len(s.listUsers))
	copy(users, s.listUsers)
	return users
}

func (s *stubUserProvider) GetUser(name string) (adapter.User, bool) {
	user, found := s.users[name]
	return user, found
}

func (s *stubUserProvider) CreateUser(user option.User) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.created = append(s.created, user)
	if s.users == nil {
		s.users = make(map[string]adapter.User)
	}
	s.users[user.Name] = adapter.User{
		Name:     user.Name,
		Password: user.Password,
		UUID:     user.UUID,
		AlterId:  user.AlterId,
		Flow:     user.Flow,
	}
	return nil
}

func (s *stubUserProvider) UpdateUser(name string, patch UserPatch) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.updated = append(s.updated, stubUpdateCall{name: name, patch: patch})
	return nil
}

func (s *stubUserProvider) DeleteUser(name string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deleted = append(s.deleted, name)
	delete(s.users, name)
	return nil
}

func TestAdminAPIUserRoutesRequireAuthentication(t *testing.T) {
	service := newAdminAPIUserTestService(t, "", &stubUserProvider{})
	basePath := service.basePath

	testCases := []struct {
		name string
		path string
		body string
	}{
		{name: "list", path: "/user/list", body: `{}`},
		{name: "get", path: "/user/get", body: `{"name":"sekai"}`},
		{name: "create", path: "/user/create", body: `{"name":"sekai","password":"password"}`},
		{name: "update", path: "/user/update", body: `{"name":"sekai","password":"next"}`},
		{name: "delete", path: "/user/delete", body: `{"name":"sekai"}`},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := performAdminAPIRequest(service, http.MethodPost, basePath+testCase.path, testCase.body, "")
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestAdminAPIUserRoutesHonorConfiguredBasePath(t *testing.T) {
	provider := &stubUserProvider{
		listUsers: []adapter.User{{Name: "sekai", Password: "password"}},
	}
	service := newAdminAPIUserTestService(t, "/ops/admin", provider)
	token := loginAdminAPIUserTestToken(t, service)

	customPathResponse := performAdminAPIRequest(service, http.MethodPost, "/ops/admin/user/list", `{}`, token)
	if customPathResponse.Code != http.StatusOK {
		t.Fatalf("unexpected prefixed status: %d body=%s", customPathResponse.Code, customPathResponse.Body.String())
	}

	unprefixedResponse := performAdminAPIRequest(service, http.MethodPost, "/user/list", `{}`, token)
	if unprefixedResponse.Code != http.StatusNotFound {
		t.Fatalf("unexpected unprefixed status: %d body=%s", unprefixedResponse.Code, unprefixedResponse.Body.String())
	}

	loginResponse := performAdminAPIRequest(service, http.MethodPost, "/ops/admin/auth/login", `{"username":"alice","password":"wonderland"}`, "")
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("unexpected login status: %d body=%s", loginResponse.Code, loginResponse.Body.String())
	}

	bareLoginResponse := performAdminAPIRequest(service, http.MethodPost, "/auth/login", `{"username":"alice","password":"wonderland"}`, "")
	if bareLoginResponse.Code != http.StatusNotFound {
		t.Fatalf("unexpected bare login status: %d body=%s", bareLoginResponse.Code, bareLoginResponse.Body.String())
	}
}

func TestAdminAPIUserRoutesRejectWrongMethod(t *testing.T) {
	service := newAdminAPIUserTestService(t, "", &stubUserProvider{})
	basePath := service.basePath

	paths := []string{
		"/auth/login",
		"/user/list",
		"/user/get",
		"/user/create",
		"/user/update",
		"/user/delete",
	}

	for _, routePath := range paths {
		t.Run(routePath, func(t *testing.T) {
			recorder := performAdminAPIRequest(service, http.MethodGet, basePath+routePath, "", "")
			if recorder.Code != http.StatusMethodNotAllowed {
				t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestAdminAPIUserRoutesUseLoginBearerFlow(t *testing.T) {
	provider := &stubUserProvider{
		listUsers: []adapter.User{{Name: "sekai", Password: "password"}},
	}
	service := newAdminAPIUserTestService(t, "", provider)
	basePath := service.basePath

	loginResponse := performAdminAPIRequest(service, http.MethodPost, basePath+"/auth/login", `{"username":"alice","password":"wonderland"}`, "")
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("unexpected login status: %d body=%s", loginResponse.Code, loginResponse.Body.String())
	}

	var loginResult struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginResult); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if loginResult.Token == "" {
		t.Fatal("expected bearer token from login response")
	}

	listResponse := performAdminAPIRequest(service, http.MethodPost, basePath+"/user/list", `{}`, loginResult.Token)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("unexpected list status: %d body=%s", listResponse.Code, listResponse.Body.String())
	}

	var users []adapter.User
	if err := json.Unmarshal(listResponse.Body.Bytes(), &users); err != nil {
		t.Fatalf("decode user list: %v", err)
	}
	if len(users) != 1 || users[0].Name != "sekai" {
		t.Fatalf("unexpected users: %#v", users)
	}
}

func TestAdminAPIUserRoutesWithBearerToken(t *testing.T) {
	provider := &stubUserProvider{
		listUsers: []adapter.User{{Name: "sekai", Password: "password"}},
		users: map[string]adapter.User{
			"sekai": {Name: "sekai", Password: "password"},
		},
	}
	service := newAdminAPIUserTestService(t, "", provider)
	token := loginAdminAPIUserTestToken(t, service)
	basePath := service.basePath
	newPassword := "next"

	testCases := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		assert     func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:       "list",
			path:       "/user/list",
			body:       `{}`,
			wantStatus: http.StatusOK,
			assert: func(t *testing.T, recorder *httptest.ResponseRecorder) {
				var users []adapter.User
				if err := json.Unmarshal(recorder.Body.Bytes(), &users); err != nil {
					t.Fatalf("decode users: %v", err)
				}
				if len(users) != 1 || users[0].Name != "sekai" {
					t.Fatalf("unexpected users: %#v", users)
				}
			},
		},
		{
			name:       "get",
			path:       "/user/get",
			body:       `{"name":"sekai"}`,
			wantStatus: http.StatusOK,
			assert: func(t *testing.T, recorder *httptest.ResponseRecorder) {
				var user adapter.User
				if err := json.Unmarshal(recorder.Body.Bytes(), &user); err != nil {
					t.Fatalf("decode user: %v", err)
				}
				if user.Name != "sekai" {
					t.Fatalf("unexpected user: %#v", user)
				}
			},
		},
		{
			name:       "create",
			path:       "/user/create",
			body:       `{"name":"new-user","password":"create-password"}`,
			wantStatus: http.StatusNoContent,
			assert: func(t *testing.T, _ *httptest.ResponseRecorder) {
				if len(provider.created) != 1 {
					t.Fatalf("unexpected create calls: %d", len(provider.created))
				}
				if provider.created[0].Name != "new-user" {
					t.Fatalf("unexpected created user: %#v", provider.created[0])
				}
			},
		},
		{
			name:       "update",
			path:       "/user/update",
			body:       `{"name":"sekai","password":"next"}`,
			wantStatus: http.StatusNoContent,
			assert: func(t *testing.T, _ *httptest.ResponseRecorder) {
				if len(provider.updated) != 1 {
					t.Fatalf("unexpected update calls: %d", len(provider.updated))
				}
				if provider.updated[0].name != "sekai" {
					t.Fatalf("unexpected update target: %#v", provider.updated[0])
				}
				if provider.updated[0].patch.Password == nil || *provider.updated[0].patch.Password != newPassword {
					t.Fatalf("unexpected update patch: %#v", provider.updated[0].patch)
				}
			},
		},
		{
			name:       "delete",
			path:       "/user/delete",
			body:       `{"name":"sekai"}`,
			wantStatus: http.StatusNoContent,
			assert: func(t *testing.T, _ *httptest.ResponseRecorder) {
				if len(provider.deleted) != 1 || provider.deleted[0] != "sekai" {
					t.Fatalf("unexpected delete calls: %#v", provider.deleted)
				}
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := performAdminAPIRequest(service, http.MethodPost, basePath+testCase.path, testCase.body, token)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
			}
			testCase.assert(t, recorder)
		})
	}
}

func TestAdminAPIUserCreateAppliesQuotaAndSpeed(t *testing.T) {
	provider := &stubUserProvider{}
	quotaService := newAdminAPIUserQuotaService(t)
	speedService := newAdminAPIUserSpeedService(t)
	service := newAdminAPIUserTestService(t, "", provider, quotaService, speedService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodPost, service.basePath+"/user/create", `{
		"user":{"name":"new-user","password":"create-password"},
		"quota":{"quota_gb":1,"period":"daily"},
		"speed":{"upload_mbps":5,"download_mbps":10},
		"speed_schedules":[{"time_range":"08:00-18:00","upload_mbps":2,"download_mbps":4}]
	}`, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}

	if len(provider.created) != 1 {
		t.Fatalf("unexpected create calls: %d", len(provider.created))
	}
	if provider.created[0].Name != "new-user" {
		t.Fatalf("unexpected created user: %#v", provider.created[0])
	}

	quotaStatus, ok := quotaService.QuotaStatus("new-user")
	if !ok {
		t.Fatal("expected quota config for new-user")
	}
	if quotaStatus.QuotaBytes != 1<<30 {
		t.Fatalf("quota bytes = %d, want %d", quotaStatus.QuotaBytes, int64(1<<30))
	}
	if quotaStatus.UsageBytes != 0 || quotaStatus.Exceeded {
		t.Fatalf("unexpected quota status: %+v", quotaStatus)
	}

	uploadMbps, downloadMbps, ok := speedService.CurrentSpeed("new-user")
	if !ok {
		t.Fatal("expected speed config for new-user")
	}
	if uploadMbps != 5 || downloadMbps != 10 {
		t.Fatalf("current speed = %d/%d, want 5/10", uploadMbps, downloadMbps)
	}

	schedules, ok := speedService.GetUserSchedules("new-user")
	if !ok {
		t.Fatal("expected runtime schedules for new-user")
	}
	if len(schedules) != 1 {
		t.Fatalf("schedule length = %d, want 1", len(schedules))
	}
	if schedules[0] != (speedlimiter.UserSchedule{
		TimeRange:    "08:00-18:00",
		UploadMbps:   2,
		DownloadMbps: 4,
	}) {
		t.Fatalf("unexpected schedules: %+v", schedules)
	}
}

func TestAdminAPIUserDeleteRemovesUserQuotaAndSchedules(t *testing.T) {
	provider := &stubUserProvider{}
	quotaService := newAdminAPIUserQuotaService(t)
	speedService := newAdminAPIUserSpeedService(t)
	service := newAdminAPIUserTestService(t, "", provider, quotaService, speedService)
	token := loginAdminAPIUserTestToken(t, service)

	if err := provider.CreateUser(option.User{Name: "sekai", Password: "password"}); err != nil {
		t.Fatalf("prime user provider: %v", err)
	}
	if err := quotaService.ApplyConfig(option.TrafficQuotaUser{
		Name:    "sekai",
		QuotaGB: 1,
		Period:  "daily",
	}); err != nil {
		t.Fatalf("prime quota service: %v", err)
	}
	if err := speedService.ApplyConfig(option.SpeedLimiterUser{
		Name:         "sekai",
		UploadMbps:   5,
		DownloadMbps: 10,
	}); err != nil {
		t.Fatalf("prime speed service: %v", err)
	}
	if err := speedService.ReplaceUserSchedules("sekai", []speedlimiter.UserSchedule{
		{
			TimeRange:    "08:00-18:00",
			UploadMbps:   2,
			DownloadMbps: 4,
		},
	}); err != nil {
		t.Fatalf("prime speed schedules: %v", err)
	}

	recorder := performAdminAPIRequest(service, http.MethodPost, service.basePath+"/user/delete", `{"user":"sekai","purge_limits":true}`, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}

	if len(provider.deleted) != 1 || provider.deleted[0] != "sekai" {
		t.Fatalf("unexpected delete calls: %#v", provider.deleted)
	}
	if _, ok := quotaService.QuotaStatus("sekai"); ok {
		t.Fatal("expected quota state to be removed for sekai")
	}
	if _, _, ok := speedService.CurrentSpeed("sekai"); ok {
		t.Fatal("expected speed config to be removed for sekai")
	}
	if _, ok := speedService.GetUserSchedules("sekai"); ok {
		t.Fatal("expected runtime schedules to be removed for sekai")
	}
}

func TestAdminAPIUserCreateRollsBackOnScheduleFailure(t *testing.T) {
	provider := &stubUserProvider{}
	quotaService := newAdminAPIUserQuotaService(t)
	speedService := newAdminAPIFailingScheduleSpeedService(t, errors.New("schedule apply failed"))
	service := newAdminAPIUserTestService(t, "", provider, quotaService, speedService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodPost, service.basePath+"/user/create", `{
		"user":{"name":"rollback-user","password":"create-password"},
		"quota":{"quota_gb":1,"period":"daily"},
		"speed":{"upload_mbps":5,"download_mbps":10},
		"speed_schedules":[{"time_range":"08:00-18:00","upload_mbps":2,"download_mbps":4}]
	}`, token)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}

	if len(provider.created) != 1 || provider.created[0].Name != "rollback-user" {
		t.Fatalf("unexpected create calls: %#v", provider.created)
	}
	if len(provider.deleted) != 1 || provider.deleted[0] != "rollback-user" {
		t.Fatalf("expected rollback delete call, got %#v", provider.deleted)
	}
	if _, ok := quotaService.QuotaStatus("rollback-user"); ok {
		t.Fatal("expected quota rollback for rollback-user")
	}
	if _, _, ok := speedService.CurrentSpeed("rollback-user"); ok {
		t.Fatal("expected speed rollback for rollback-user")
	}
}

func TestAdminAPIUserDeleteKeepsLifecycleWhenQuotaCleanupFails(t *testing.T) {
	provider := &stubUserProvider{}
	quotaService := newAdminAPIFailingRemoveQuotaService(t, errors.New("quota cleanup failed"))
	speedService := newAdminAPICountingSpeedService(t)
	service := newAdminAPIUserTestService(t, "", provider, quotaService, speedService)
	token := loginAdminAPIUserTestToken(t, service)

	if err := provider.CreateUser(option.User{Name: "sekai", Password: "password"}); err != nil {
		t.Fatalf("prime user provider: %v", err)
	}
	if err := quotaService.ApplyConfig(option.TrafficQuotaUser{
		Name:    "sekai",
		QuotaGB: 1,
		Period:  "daily",
	}); err != nil {
		t.Fatalf("prime quota service: %v", err)
	}
	if err := speedService.ApplyConfig(option.SpeedLimiterUser{
		Name:         "sekai",
		UploadMbps:   5,
		DownloadMbps: 10,
	}); err != nil {
		t.Fatalf("prime speed service: %v", err)
	}
	if err := speedService.ReplaceUserSchedules("sekai", []speedlimiter.UserSchedule{
		{
			TimeRange:    "08:00-18:00",
			UploadMbps:   2,
			DownloadMbps: 4,
		},
	}); err != nil {
		t.Fatalf("prime speed schedules: %v", err)
	}

	recorder := performAdminAPIRequest(service, http.MethodPost, service.basePath+"/user/delete", `{"user":"sekai","purge_limits":true}`, token)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}

	if len(provider.deleted) != 1 || provider.deleted[0] != "sekai" {
		t.Fatalf("expected user delete before compensation, got %#v", provider.deleted)
	}
	if _, ok := provider.GetUser("sekai"); !ok {
		t.Fatal("expected user to be restored after cleanup failure")
	}
	if _, ok := quotaService.QuotaStatus("sekai"); !ok {
		t.Fatal("expected quota state to remain for sekai")
	}
	if _, _, ok := speedService.CurrentSpeed("sekai"); !ok {
		t.Fatal("expected speed state to remain for sekai")
	}
	if _, ok := speedService.GetUserSchedules("sekai"); !ok {
		t.Fatal("expected schedule state to remain for sekai")
	}
	if speedService.removeConfigCalls == 0 {
		t.Fatal("expected speed cleanup attempt before quota failure")
	}
	if speedService.removeScheduleCalls == 0 {
		t.Fatal("expected schedule cleanup attempt before quota failure")
	}
}

func TestAdminAPIUserProviderErrorMapping(t *testing.T) {
	testCases := []struct {
		name       string
		path       string
		body       string
		provider   *stubUserProvider
		wantStatus int
	}{
		{
			name:       "create validation",
			path:       "/user/create",
			body:       `{"name":"sekai"}`,
			provider:   &stubUserProvider{createErr: errors.New("missing name")},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "update not found",
			path:       "/user/update",
			body:       `{"name":"sekai","password":"next"}`,
			provider:   &stubUserProvider{updateErr: errors.New("user sekai not found")},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "delete internal",
			path: "/user/delete",
			body: `{"name":"sekai"}`,
			provider: &stubUserProvider{
				users:     map[string]adapter.User{"sekai": {Name: "sekai"}},
				deleteErr: errors.New("write failed"),
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "delete unavailable",
			path: "/user/delete",
			body: `{"name":"sekai"}`,
			provider: &stubUserProvider{
				users:     map[string]adapter.User{"sekai": {Name: "sekai"}},
				deleteErr: context.Canceled,
			},
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := newAdminAPIUserTestService(t, "", testCase.provider)
			token := loginAdminAPIUserTestToken(t, service)
			recorder := performAdminAPIRequest(service, http.MethodPost, service.basePath+testCase.path, testCase.body, token)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func newAdminAPIUserTestService(t *testing.T, routePath string, managedServices ...adapter.Service) *Service {
	t.Helper()

	manager := &stubAdminAPIServiceManager{services: managedServices}
	ctx := singservice.ContextWith[adapter.ServiceManager](context.Background(), manager)

	rawService, err := NewService(ctx, log.NewNOPFactory().Logger(), "admin", option.AdminAPIServiceOptions{
		Path:        routePath,
		TokenSecret: "super-secret",
		Admins: []option.AdminCredential{
			{Username: "alice", Password: "wonderland"},
		},
	})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}
	service, ok := rawService.(*Service)
	if !ok {
		t.Fatalf("unexpected admin-api service type: %T", rawService)
	}
	service.authenticator.now = func() time.Time {
		return time.Unix(1_700_000_000, 0)
	}
	if err := service.Start(adapter.StartStateStart); err != nil {
		t.Fatalf("service.Start: %v", err)
	}
	return service
}

func newAdminAPIUserQuotaService(t *testing.T) *trafficquota.Service {
	t.Helper()

	rawService, err := trafficquota.NewService(context.Background(), log.NewNOPFactory().Logger(), "quota", option.TrafficQuotaServiceOptions{})
	if err != nil {
		t.Fatalf("new quota service: %v", err)
	}
	service, ok := rawService.(*trafficquota.Service)
	if !ok {
		t.Fatalf("unexpected quota service type: %T", rawService)
	}
	return service
}

func newAdminAPIUserSpeedService(t *testing.T) *speedlimiter.Service {
	t.Helper()

	rawService, err := speedlimiter.NewService(context.Background(), log.NewNOPFactory().Logger(), "speed", option.SpeedLimiterServiceOptions{})
	if err != nil {
		t.Fatalf("new speed service: %v", err)
	}
	service, ok := rawService.(*speedlimiter.Service)
	if !ok {
		t.Fatalf("unexpected speed service type: %T", rawService)
	}
	return service
}

type countingSpeedService struct {
	*speedlimiter.Service
	removeConfigCalls   int
	removeScheduleCalls int
}

func newAdminAPICountingSpeedService(t *testing.T) *countingSpeedService {
	t.Helper()

	return &countingSpeedService{Service: newAdminAPIUserSpeedService(t)}
}

func (s *countingSpeedService) RemoveConfig(user string) error {
	s.removeConfigCalls++
	return s.Service.RemoveConfig(user)
}

func (s *countingSpeedService) RemoveUserSchedules(user string) error {
	s.removeScheduleCalls++
	return s.Service.RemoveUserSchedules(user)
}

type failingScheduleSpeedService struct {
	*speedlimiter.Service
	replaceErr error
}

func newAdminAPIFailingScheduleSpeedService(t *testing.T, replaceErr error) *failingScheduleSpeedService {
	t.Helper()

	return &failingScheduleSpeedService{
		Service:    newAdminAPIUserSpeedService(t),
		replaceErr: replaceErr,
	}
}

func (s *failingScheduleSpeedService) ReplaceUserSchedules(user string, schedules []speedlimiter.UserSchedule) error {
	return s.replaceErr
}

type failingRemoveQuotaService struct {
	*trafficquota.Service
	removeErr error
}

func newAdminAPIFailingRemoveQuotaService(t *testing.T, removeErr error) *failingRemoveQuotaService {
	t.Helper()

	return &failingRemoveQuotaService{
		Service:   newAdminAPIUserQuotaService(t),
		removeErr: removeErr,
	}
}

func (s *failingRemoveQuotaService) RemoveConfig(user string) error {
	return s.removeErr
}

func loginAdminAPIUserTestToken(t *testing.T, service *Service) string {
	t.Helper()

	recorder := performAdminAPIRequest(service, http.MethodPost, service.basePath+"/auth/login", `{"username":"alice","password":"wonderland"}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected login status: %d body=%s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if response.Token == "" {
		t.Fatal("expected login token")
	}
	return response.Token
}

func performAdminAPIRequest(service *Service, method string, requestPath string, body string, bearerToken string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, requestPath, strings.NewReader(body))
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, request)
	return recorder
}

type stubAdminAPIServiceManager struct {
	services []adapter.Service
}

func (m *stubAdminAPIServiceManager) Start(stage adapter.StartStage) error {
	return nil
}

func (m *stubAdminAPIServiceManager) Close() error {
	return nil
}

func (m *stubAdminAPIServiceManager) Services() []adapter.Service {
	return append([]adapter.Service(nil), m.services...)
}

func (m *stubAdminAPIServiceManager) Get(tag string) (adapter.Service, bool) {
	for _, managedService := range m.services {
		if managedService.Tag() == tag {
			return managedService, true
		}
	}
	return nil, false
}

func (m *stubAdminAPIServiceManager) Remove(tag string) error {
	return errors.New("not implemented")
}

func (m *stubAdminAPIServiceManager) Create(ctx context.Context, logger log.ContextLogger, tag string, serviceType string, options any) error {
	return errors.New("not implemented")
}
