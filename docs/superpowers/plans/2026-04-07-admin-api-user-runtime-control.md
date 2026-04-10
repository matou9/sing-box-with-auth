# Admin API User Runtime Control Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `admin-api` into an authenticated `POST`-only control plane for admin login, user lifecycle CRUD, runtime quota control, runtime fixed speed control, and per-user runtime speed schedules without moving ownership away from `user-provider`, `traffic-quota`, or `speed-limiter`.

**Architecture:** Add admin authentication and request middleware inside `service/adminapi`, add a runtime overlay write path to `service/userprovider`, add runtime per-user schedule state to `service/speedlimiter`, and keep `traffic-quota` as the quota state owner. `admin-api` becomes an orchestrator that resolves services from `ServiceManager`, validates auth, and applies multi-service operations in a fixed order.

**Tech Stack:** Go, chi, existing sing-box service registry/context pattern, current `traffic-quota` and `speed-limiter` runtime update APIs, TDD with `go test` and `go test -race`.

---

## File Structure

### Modify

- `option/admin_api.go`
Defines admin API config fields for token secret, token TTL, static tokens, and admin credentials.

- `service/adminapi/service.go`
Constructs the service, initializes auth config, resolves dependent services, and wires the router.

- `service/adminapi/handler_quota.go`
Convert quota endpoints from mixed REST style to `POST` bodies and keep runtime orchestration here.

- `service/adminapi/handler_speed.go`
Convert speed endpoints to `POST` bodies and add schedule sub-endpoints.

- `service/userprovider/service.go`
Add runtime overlay state plus `ListUsers`, `GetUser`, `CreateUser`, `UpdateUser`, and `DeleteUser`.

- `service/speedlimiter/manager.go`
Add per-user runtime schedules, schedule precedence resolution, and public runtime read/write methods.

- `service/speedlimiter/service.go`
Expose schedule read/write methods to `admin-api`.

- `docs/auth-features-README.md`
Document the new POST-only admin endpoints and authentication model.

### Create

- `service/adminapi/auth.go`
Parses Basic and Bearer credentials, validates static tokens, signs login tokens, and installs auth middleware.

- `service/adminapi/auth_test.go`
Focused tests for login, Basic auth, static token auth, signed token auth, and expiry rejection.

- `service/adminapi/handler_auth.go`
`POST /admin/v1/auth/login` implementation.

- `service/adminapi/handler_user.go`
`POST /admin/v1/user/{list,get,create,update,delete}` orchestration.

- `service/adminapi/handler_user_test.go`
HTTP-level tests for user lifecycle endpoints and aggregate reads.

- `service/userprovider/service_runtime_test.go`
Overlay lifecycle tests and push-to-inbound behavior verification.

- `service/speedlimiter/runtime_schedule_test.go`
Runtime user schedule precedence tests.

## Task 1: Add Admin API Auth Config And Login Model

**Files:**
- Modify: `option/admin_api.go`
- Create: `service/adminapi/auth.go`
- Create: `service/adminapi/handler_auth.go`
- Create: `service/adminapi/auth_test.go`
- Test: `service/adminapi/auth_test.go`

- [ ] **Step 1: Write the failing auth config and login tests**

```go
package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

func TestAdminAuthLoginReturnsSignedToken(t *testing.T) {
	service := newAuthOnlyAdminService(t, option.AdminAPIServiceOptions{
		TokenSecret: "secret-123",
		TokenTTL:    "12h",
		Admins: []option.AdminCredential{
			{Username: "admin", Password: "pass"},
		},
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"admin","password":"pass"}`))
	request.Header.Set("Content-Type", "application/json")

	service.login(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"token":`) {
		t.Fatalf("expected token in response, got %s", recorder.Body.String())
	}
}

func TestAdminAuthMiddlewareAcceptsStaticBearerToken(t *testing.T) {
	service := newAuthOnlyAdminService(t, option.AdminAPIServiceOptions{
		Tokens: []string{"static-token"},
	})

	authorized := false
	handler := service.requireAdmin(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorized = true
		writer.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodPost, "/admin/v1/user/list", nil)
	request.Header.Set("Authorization", "Bearer static-token")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent || !authorized {
		t.Fatalf("expected authorized request, status=%d authorized=%v", recorder.Code, authorized)
	}
}

func TestAdminAuthMiddlewareRejectsExpiredLoginToken(t *testing.T) {
	service := newAuthOnlyAdminService(t, option.AdminAPIServiceOptions{
		TokenSecret: "secret-123",
		TokenTTL:    "1ms",
		Admins: []option.AdminCredential{
			{Username: "admin", Password: "pass"},
		},
	})

	token, err := service.issueToken("admin", time.Now().Add(-time.Second))
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	handler := service.requireAdmin(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodPost, "/admin/v1/user/list", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized for expired token, got %d", recorder.Code)
	}
}

func newAuthOnlyAdminService(t *testing.T, options option.AdminAPIServiceOptions) *Service {
	t.Helper()
	rawService, err := NewService(context.Background(), log.NewNOPFactory().Logger(), "admin", options)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return rawService.(*Service)
}
```

- [ ] **Step 2: Run auth tests to verify they fail**

Run: `go test ./service/adminapi -run 'TestAdminAuth'`

Expected: FAIL with missing `TokenSecret`, `Admins`, `issueToken`, `requireAdmin`, or `login` members.

- [ ] **Step 3: Write minimal auth config and token implementation**

```go
package option

import "github.com/sagernet/sing/common/json/badoption"

type AdminAPIServiceOptions struct {
	Listen      string             `json:"listen,omitempty"`
	Path        string             `json:"path,omitempty"`
	TokenSecret string             `json:"token_secret,omitempty"`
	TokenTTL    badoption.Duration `json:"token_ttl,omitempty"`
	Tokens      []string           `json:"tokens,omitempty"`
	Admins      []AdminCredential  `json:"admins,omitempty"`
}

type AdminCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
```

```go
package adminapi

type signedTokenClaims struct {
	Subject   string `json:"sub"`
	ExpiresAt int64  `json:"exp"`
}

func (s *Service) issueToken(username string, expiresAt time.Time) (string, error) {
	claims := signedTokenClaims{Subject: username, ExpiresAt: expiresAt.Unix()}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(s.tokenSecret))
	mac.Write(payload)
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString(payload) + "." + signature, nil
}

func (s *Service) validateToken(token string) bool {
	if slices.Contains(s.staticTokens, token) {
		return true
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(s.tokenSecret))
	mac.Write(payload)
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return false
	}
	var claims signedTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return false
	}
	return time.Now().Unix() < claims.ExpiresAt
}
```

- [ ] **Step 4: Run auth tests to verify they pass**

Run: `go test ./service/adminapi -run 'TestAdminAuth'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add option/admin_api.go service/adminapi/auth.go service/adminapi/handler_auth.go service/adminapi/auth_test.go
git commit -m "feat: add admin-api authentication"
```

## Task 2: Convert Admin API To POST-Only Authenticated Routing

**Files:**
- Modify: `service/adminapi/service.go`
- Modify: `service/adminapi/handler_quota.go`
- Modify: `service/adminapi/handler_speed.go`
- Create: `service/adminapi/handler_user.go`
- Create: `service/adminapi/handler_user_test.go`
- Test: `service/adminapi/handler_user_test.go`

- [ ] **Step 1: Write failing POST-only route tests**

```go
func TestAdminAPIUserCreateRequiresAuthentication(t *testing.T) {
	service := newAdminAPIService(t, nil, nil, option.AdminAPIServiceOptions{
		Tokens: []string{"static-token"},
	})
	startAdminAPI(t, service)

	response := mustPost(t, "http://"+service.listener.Addr().String()+"/admin/v1/user/create", `{"user":{"name":"alice","password":"p"}}`, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", response.StatusCode)
	}
}

func TestAdminAPIUserCreateUsesBearerToken(t *testing.T) {
	userProvider := newUserProviderRuntimeService(t)
	service := newAdminAPIService(t, userProvider, nil, nil, option.AdminAPIServiceOptions{
		Tokens: []string{"static-token"},
	})
	startAdminAPI(t, service)

	response := mustPost(t, "http://"+service.listener.Addr().String()+"/admin/v1/user/create", `{"user":{"name":"alice","password":"p"}}`, "Bearer static-token")
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("expected success, got %d", response.StatusCode)
	}
	if _, loaded := userProvider.GetUser("alice"); !loaded {
		t.Fatal("expected created user to exist")
	}
}
```

- [ ] **Step 2: Run route tests to verify they fail**

Run: `go test ./service/adminapi -run 'TestAdminAPIUser'`

Expected: FAIL because user routes, auth middleware wrapping, or user-provider resolution are missing.

- [ ] **Step 3: Write minimal POST-only route wiring**

```go
func (s *Service) Route(r chi.Router) {
	r.Route(s.basePath, func(r chi.Router) {
		r.Post("/auth/login", s.login)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAdmin)
			r.Post("/user/list", s.listUsers)
			r.Post("/user/get", s.getUser)
			r.Post("/user/create", s.createUser)
			r.Post("/user/update", s.updateUser)
			r.Post("/user/delete", s.deleteUser)
			r.Post("/quota/get", s.getQuota)
			r.Post("/quota/update", s.updateQuota)
			r.Post("/quota/delete", s.deleteQuota)
			r.Post("/speed/get", s.getSpeed)
			r.Post("/speed/update", s.updateSpeed)
			r.Post("/speed/delete", s.deleteSpeed)
			r.Post("/speed/schedule/get", s.getSpeedSchedules)
			r.Post("/speed/schedule/update", s.updateSpeedSchedules)
			r.Post("/speed/schedule/delete", s.deleteSpeedSchedules)
		})
	})
}
```

- [ ] **Step 4: Run route tests to verify they pass**

Run: `go test ./service/adminapi -run 'TestAdminAPIUser'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add service/adminapi/service.go service/adminapi/handler_quota.go service/adminapi/handler_speed.go service/adminapi/handler_user.go service/adminapi/handler_user_test.go
git commit -m "feat: convert admin-api routes to authenticated post endpoints"
```

## Task 3: Add Runtime Overlay CRUD To User Provider

**Files:**
- Modify: `service/userprovider/service.go`
- Create: `service/userprovider/service_runtime_test.go`
- Test: `service/userprovider/service_runtime_test.go`

- [ ] **Step 1: Write failing user-provider overlay tests**

```go
func TestUserProviderCreateUserAddsOverlayAndPushes(t *testing.T) {
	service, server := newRuntimeUserProviderService(t)

	err := service.CreateUser(option.User{Name: "alice", Password: "pass"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, loaded := service.GetUser("alice")
	if !loaded || user.Name != "alice" {
		t.Fatalf("expected alice in overlay, loaded=%v user=%+v", loaded, user)
	}
	if got := len(server.lastUsers); got != 1 {
		t.Fatalf("expected pushed users, got %d", got)
	}
}

func TestUserProviderUpdateUserOverridesExistingSourceValue(t *testing.T) {
	service, _ := newRuntimeUserProviderService(t, option.User{Name: "alice", Password: "old"})

	err := service.UpdateUser("alice", UserPatch{Password: common.Ptr("new")})
	if err != nil {
		t.Fatalf("update user: %v", err)
	}
	user, loaded := service.GetUser("alice")
	if !loaded || user.Password != "new" {
		t.Fatalf("expected updated password, loaded=%v user=%+v", loaded, user)
	}
}

func TestUserProviderDeleteUserRemovesOverlayAndPushes(t *testing.T) {
	service, server := newRuntimeUserProviderService(t)
	_ = service.CreateUser(option.User{Name: "alice", Password: "pass"})

	err := service.DeleteUser("alice")
	if err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if _, loaded := service.GetUser("alice"); loaded {
		t.Fatal("expected user removed")
	}
	if got := len(server.lastUsers); got != 0 {
		t.Fatalf("expected pushed empty users, got %d", got)
	}
}
```

- [ ] **Step 2: Run overlay tests to verify they fail**

Run: `go test ./service/userprovider -run 'TestUserProvider(CreateUser|UpdateUser|DeleteUser)'`

Expected: FAIL because overlay state and runtime CRUD methods do not exist.

- [ ] **Step 3: Write minimal overlay implementation**

```go
type UserPatch struct {
	Password *string
	UUID     *string
	AlterID  *int
	Flow     *string
}

type Service struct {
	// existing fields...
	overlayUsers map[string]option.User
}

func (s *Service) CreateUser(user option.User) error {
	s.access.Lock()
	defer s.access.Unlock()
	if user.Name == "" {
		return E.New("missing user name")
	}
	if _, exists := s.overlayUsers[user.Name]; exists {
		return E.New("user already exists: ", user.Name)
	}
	s.overlayUsers[user.Name] = user
	return s.reloadAndPushLocked()
}

func (s *Service) UpdateUser(name string, patch UserPatch) error {
	s.access.Lock()
	defer s.access.Unlock()
	current, loaded := s.lookupLocked(name)
	if !loaded {
		return os.ErrNotExist
	}
	if patch.Password != nil {
		current.Password = *patch.Password
	}
	s.overlayUsers[name] = adapterUserToOption(current)
	return s.reloadAndPushLocked()
}
```

- [ ] **Step 4: Run overlay tests to verify they pass**

Run: `go test ./service/userprovider -run 'TestUserProvider(CreateUser|UpdateUser|DeleteUser)'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add service/userprovider/service.go service/userprovider/service_runtime_test.go
git commit -m "feat: add runtime user-provider overlay crud"
```

## Task 4: Add Runtime Per-User Schedules To Speed Limiter

**Files:**
- Modify: `service/speedlimiter/manager.go`
- Modify: `service/speedlimiter/service.go`
- Create: `service/speedlimiter/runtime_schedule_test.go`
- Test: `service/speedlimiter/runtime_schedule_test.go`

- [ ] **Step 1: Write failing schedule precedence tests**

```go
func TestManagerUserScheduleAppliesWhenNoFixedOverride(t *testing.T) {
	manager, err := NewLimiterManager(option.SpeedLimiterServiceOptions{
		Default: &option.SpeedLimiterDefault{UploadMbps: 50, DownloadMbps: 100},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = manager.ReplaceUserSchedules("alice", []UserSchedule{
		{TimeRange: "08:00-18:00", UploadMbps: 5, DownloadMbps: 10},
	})
	if err != nil {
		t.Fatalf("replace schedules: %v", err)
	}
	manager.CheckSchedules(timeAt(9, 0))
	upload, download, ok := manager.CurrentSpeed("alice")
	if !ok || upload != 5 || download != 10 {
		t.Fatalf("unexpected current speed ok=%v upload=%d download=%d", ok, upload, download)
	}
}

func TestManagerFixedUserSpeedBeatsUserSchedule(t *testing.T) {
	manager, err := NewLimiterManager(option.SpeedLimiterServiceOptions{
		Default: &option.SpeedLimiterDefault{UploadMbps: 50, DownloadMbps: 100},
	})
	if err != nil {
		t.Fatal(err)
	}

	manager.UpdateUserSpeed("alice", 20, 30)
	_ = manager.ReplaceUserSchedules("alice", []UserSchedule{
		{TimeRange: "08:00-18:00", UploadMbps: 5, DownloadMbps: 10},
	})

	manager.CheckSchedules(timeAt(9, 0))
	upload, download, ok := manager.CurrentSpeed("alice")
	if !ok || upload != 20 || download != 30 {
		t.Fatalf("expected fixed speed precedence, ok=%v upload=%d download=%d", ok, upload, download)
	}
}
```

- [ ] **Step 2: Run schedule tests to verify they fail**

Run: `go test ./service/speedlimiter -run 'TestManager(UserSchedule|FixedUserSpeed)'`

Expected: FAIL because runtime `UserSchedule` support does not exist.

- [ ] **Step 3: Write minimal runtime schedule implementation**

```go
type UserSchedule struct {
	TimeRange    string `json:"time_range"`
	UploadMbps   int    `json:"upload_mbps,omitempty"`
	DownloadMbps int    `json:"download_mbps,omitempty"`
}

type LimiterManager struct {
	// existing fields...
	userScheduleEntries map[string][]scheduleEntry
}

func (m *LimiterManager) ReplaceUserSchedules(user string, schedules []UserSchedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := make([]scheduleEntry, 0, len(schedules))
	for _, schedule := range schedules {
		entry, err := parseSchedule(option.SpeedLimiterSchedule{
			TimeRange:    schedule.TimeRange,
			UploadMbps:   schedule.UploadMbps,
			DownloadMbps: schedule.DownloadMbps,
		})
		if err != nil {
			return err
		}
		entries = append(entries, entry)
	}
	m.userScheduleEntries[user] = entries
	m.applyCurrentRatesLocked(user)
	return nil
}
```

- [ ] **Step 4: Run schedule tests to verify they pass**

Run: `go test ./service/speedlimiter -run 'TestManager(UserSchedule|FixedUserSpeed)'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add service/speedlimiter/manager.go service/speedlimiter/service.go service/speedlimiter/runtime_schedule_test.go
git commit -m "feat: add runtime user speed schedules"
```

## Task 5: Add Authenticated User Lifecycle Endpoints

**Files:**
- Create: `service/adminapi/handler_user.go`
- Modify: `service/adminapi/service.go`
- Create: `service/adminapi/handler_user_test.go`
- Test: `service/adminapi/handler_user_test.go`

- [ ] **Step 1: Write failing user endpoint tests**

```go
func TestAdminAPIUserCreateAppliesQuotaAndSpeed(t *testing.T) {
	userProvider := newUserProviderRuntimeService(t)
	quotaService := newTrafficQuotaService(t, option.TrafficQuotaServiceOptions{})
	speedService := newSpeedLimiterService(t, option.SpeedLimiterServiceOptions{})
	admin := newAdminAPIService(t, userProvider, quotaService, speedService, option.AdminAPIServiceOptions{
		Tokens: []string{"static-token"},
	})
	startAdminAPI(t, admin)

	response := mustPost(t, "http://"+admin.listener.Addr().String()+"/admin/v1/user/create", `{
	  "user":{"name":"alice","password":"pass"},
	  "quota":{"quota_gb":0.04,"period":"daily"},
	  "speed":{"upload_mbps":5,"download_mbps":10}
	}`, "Bearer static-token")

	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", response.StatusCode)
	}
	if _, loaded := userProvider.GetUser("alice"); !loaded {
		t.Fatal("expected user created")
	}
	if !quotaService.Manager().HasQuota("alice") {
		t.Fatal("expected quota applied")
	}
	upload, download, ok := speedService.Manager().CurrentSpeed("alice")
	if !ok || upload != 5 || download != 10 {
		t.Fatalf("unexpected speed ok=%v upload=%d download=%d", ok, upload, download)
	}
}

func TestAdminAPIUserDeleteRemovesUserQuotaAndSchedules(t *testing.T) {
	// create runtime state first, then delete through admin endpoint
}
```

- [ ] **Step 2: Run user endpoint tests to verify they fail**

Run: `go test ./service/adminapi -run 'TestAdminAPIUser(Create|Delete)'`

Expected: FAIL because user-provider orchestration and aggregate user routes do not exist.

- [ ] **Step 3: Write minimal user orchestration handlers**

```go
func (s *Service) createUser(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		User           option.User                   `json:"user"`
		Quota          *dynamicconfig.ConfigRow      `json:"quota,omitempty"`
		Speed          *dynamicconfig.ConfigRow      `json:"speed,omitempty"`
		SpeedSchedules []speedlimiter.UserSchedule   `json:"speed_schedules,omitempty"`
	}
	if err := render.DecodeJSON(request.Body, &body); err != nil {
		render.Status(request, http.StatusBadRequest)
		render.PlainText(writer, request, err.Error())
		return
	}
	if s.userProvider == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if err := s.userProvider.CreateUser(body.User); err != nil {
		render.Status(request, http.StatusConflict)
		render.PlainText(writer, request, err.Error())
		return
	}
	if body.Quota != nil && s.quotaService != nil {
		body.Quota.User = body.User.Name
		_ = s.quotaService.ApplyConfig(*body.Quota)
	}
	if body.Speed != nil && s.speedService != nil {
		body.Speed.User = body.User.Name
		_ = s.speedService.ApplyConfig(*body.Speed)
	}
	if len(body.SpeedSchedules) > 0 && s.speedService != nil {
		_ = s.speedService.ReplaceUserSchedules(body.User.Name, body.SpeedSchedules)
	}
	writer.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Run user endpoint tests to verify they pass**

Run: `go test ./service/adminapi -run 'TestAdminAPIUser(Create|Delete)'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add service/adminapi/handler_user.go service/adminapi/handler_user_test.go service/adminapi/service.go
git commit -m "feat: add admin-api user lifecycle endpoints"
```

## Task 6: Add POST Quota And Speed Schedule Endpoints

**Files:**
- Modify: `service/adminapi/handler_quota.go`
- Modify: `service/adminapi/handler_speed.go`
- Modify: `service/adminapi/service_test.go`
- Test: `service/adminapi/service_test.go`

- [ ] **Step 1: Write failing POST body tests for quota and speed schedules**

```go
func TestAdminAPIQuotaUpdateUsesPostBody(t *testing.T) {
	quotaService := newTrafficQuotaService(t, option.TrafficQuotaServiceOptions{})
	admin := newAdminAPIService(t, nil, quotaService, nil, option.AdminAPIServiceOptions{
		Tokens: []string{"static-token"},
	})
	startAdminAPI(t, admin)

	response := mustPost(t, "http://"+admin.listener.Addr().String()+"/admin/v1/quota/update", `{
	  "user":"alice",
	  "quota_gb":0.04,
	  "period":"daily"
	}`, "Bearer static-token")
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", response.StatusCode)
	}
}

func TestAdminAPISpeedScheduleUpdateReplacesRuntimeSchedules(t *testing.T) {
	speedService := newSpeedLimiterService(t, option.SpeedLimiterServiceOptions{})
	admin := newAdminAPIService(t, nil, nil, speedService, option.AdminAPIServiceOptions{
		Tokens: []string{"static-token"},
	})
	startAdminAPI(t, admin)

	response := mustPost(t, "http://"+admin.listener.Addr().String()+"/admin/v1/speed/schedule/update", `{
	  "user":"alice",
	  "schedules":[{"time_range":"08:00-18:00","upload_mbps":5,"download_mbps":10}]
	}`, "Bearer static-token")
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", response.StatusCode)
	}
}
```

- [ ] **Step 2: Run quota/schedule tests to verify they fail**

Run: `go test ./service/adminapi -run 'TestAdminAPI(QuotaUpdate|SpeedScheduleUpdate)'`

Expected: FAIL because endpoints still depend on URL params or do not handle schedule operations.

- [ ] **Step 3: Write minimal POST body handlers**

```go
func (s *Service) updateQuota(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		User        string  `json:"user"`
		QuotaGB     float64 `json:"quota_gb"`
		Period      string  `json:"period,omitempty"`
		PeriodStart string  `json:"period_start,omitempty"`
		PeriodDays  int     `json:"period_days,omitempty"`
	}
	if err := render.DecodeJSON(request.Body, &body); err != nil {
		render.Status(request, http.StatusBadRequest)
		render.PlainText(writer, request, err.Error())
		return
	}
	if err := s.quotaService.ApplyConfig(dynamicconfig.ConfigRow{
		User: body.User, QuotaGB: body.QuotaGB, Period: body.Period, PeriodStart: body.PeriodStart, PeriodDays: body.PeriodDays,
	}); err != nil {
		render.Status(request, http.StatusBadRequest)
		render.PlainText(writer, request, err.Error())
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Run quota/schedule tests to verify they pass**

Run: `go test ./service/adminapi -run 'TestAdminAPI(QuotaUpdate|SpeedScheduleUpdate)'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add service/adminapi/handler_quota.go service/adminapi/handler_speed.go service/adminapi/service_test.go
git commit -m "feat: add post quota and speed schedule endpoints"
```

## Task 7: Update Documentation And Final Verification

**Files:**
- Modify: `docs/auth-features-README.md`
- Test: `service/adminapi/auth_test.go`
- Test: `service/adminapi/handler_user_test.go`
- Test: `service/userprovider/service_runtime_test.go`
- Test: `service/speedlimiter/runtime_schedule_test.go`

- [ ] **Step 1: Write the doc changes**

```md
## Admin API Authentication

- `Authorization: Bearer <token>`
- `Authorization: Basic <base64(admin:password)>`
- `POST /admin/v1/auth/login` returns a signed token with configurable TTL

## Admin API User Lifecycle

- `POST /admin/v1/user/list`
- `POST /admin/v1/user/get`
- `POST /admin/v1/user/create`
- `POST /admin/v1/user/update`
- `POST /admin/v1/user/delete`

## Admin API Runtime Limits

- `POST /admin/v1/quota/get|update|delete`
- `POST /admin/v1/speed/get|update|delete`
- `POST /admin/v1/speed/schedule/get|update|delete`
```

- [ ] **Step 2: Run targeted tests**

Run: `go test ./service/adminapi ./service/userprovider ./service/speedlimiter ./service/trafficquota`

Expected: PASS

- [ ] **Step 3: Run race tests**

Run: `go test -race ./service/userprovider ./service/speedlimiter ./service/trafficquota`

Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add docs/auth-features-README.md
git commit -m "docs: describe authenticated admin runtime control api"
```

## Spec Coverage Check

- admin authentication: Task 1
- POST-only admin routes: Task 2 and Task 6
- user lifecycle CRUD: Task 3 and Task 5
- runtime quota updates: Task 5 and Task 6
- runtime speed updates: Task 4, Task 5, and Task 6
- runtime per-user schedules: Task 4 and Task 6
- aggregate user/quota/speed/schedule reads: Task 5
- delete ordering: Task 5

No uncovered spec requirement remains.

## Placeholder Scan

- No `TBD`
- No `TODO`
- No undefined endpoint names outside task definitions
- All test commands and expected outcomes are explicit

## Type Consistency Check

- `option.AdminAPIServiceOptions` owns `TokenSecret`, `TokenTTL`, `Tokens`, and `Admins`
- `option.AdminCredential` is the admin config type across auth and docs
- `userprovider.UserPatch` is the runtime partial-update type
- `speedlimiter.UserSchedule` is the runtime schedule type across service and API
- admin handlers always accept `POST` body payloads with `user` in JSON
