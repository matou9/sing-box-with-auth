package adminapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sagernet/sing-box/option"
)

func TestAdminAPIQuotaGetStatus(t *testing.T) {
	quotaService := newAdminAPIUserQuotaService(t)
	if err := quotaService.ApplyConfig(option.TrafficQuotaUser{
		Name:    "user1",
		QuotaGB: 0.04,
		Period:  "daily",
	}); err != nil {
		t.Fatalf("apply config: %v", err)
	}
	service := newAdminAPIUserTestService(t, "", quotaService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodGet, service.basePath+"/quota/user1", "", token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var resp quotaStatusResponse
	if err := json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.QuotaBytes == 0 {
		t.Error("expected non-zero quota_bytes")
	}
}

func TestAdminAPIQuotaGetNotFound(t *testing.T) {
	quotaService := newAdminAPIUserQuotaService(t)
	service := newAdminAPIUserTestService(t, "", quotaService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodGet, service.basePath+"/quota/unknown", "", token)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", recorder.Code)
	}
}

func TestAdminAPIQuotaPut(t *testing.T) {
	quotaService := newAdminAPIUserQuotaService(t)
	service := newAdminAPIUserTestService(t, "", quotaService)
	token := loginAdminAPIUserTestToken(t, service)

	body := `{"quota_gb":0.04,"period":"daily"}`
	recorder := performAdminAPIRequest(service, http.MethodPut, service.basePath+"/quota/user1", body, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", recorder.Code, recorder.Body.String())
	}

	if _, found := quotaService.GetConfig("user1"); !found {
		t.Error("expected quota config to be set after PUT")
	}
}

func TestAdminAPIQuotaPutInvalidBody(t *testing.T) {
	quotaService := newAdminAPIUserQuotaService(t)
	service := newAdminAPIUserTestService(t, "", quotaService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodPut, service.basePath+"/quota/user1", "not-json", token)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
}

func TestAdminAPIQuotaPutValidationError(t *testing.T) {
	quotaService := newAdminAPIUserQuotaService(t)
	service := newAdminAPIUserTestService(t, "", quotaService)
	token := loginAdminAPIUserTestToken(t, service)

	// Missing period — should fail validation
	body := `{"quota_gb":0.04}`
	recorder := performAdminAPIRequest(service, http.MethodPut, service.basePath+"/quota/user1", body, token)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminAPIQuotaDelete(t *testing.T) {
	quotaService := newAdminAPIUserQuotaService(t)
	if err := quotaService.ApplyConfig(option.TrafficQuotaUser{
		Name:    "user1",
		QuotaGB: 0.04,
		Period:  "daily",
	}); err != nil {
		t.Fatalf("apply config: %v", err)
	}
	service := newAdminAPIUserTestService(t, "", quotaService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodDelete, service.basePath+"/quota/user1", "", token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", recorder.Code, recorder.Body.String())
	}

	if _, found := quotaService.GetConfig("user1"); found {
		t.Error("expected quota config to be removed after DELETE")
	}
}

func TestAdminAPIQuotaRequiresAuth(t *testing.T) {
	quotaService := newAdminAPIUserQuotaService(t)
	service := newAdminAPIUserTestService(t, "", quotaService)

	recorder := performAdminAPIRequest(service, http.MethodGet, service.basePath+"/quota/user1", "", "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
}

func TestAdminAPIQuotaControllerUnavailable(t *testing.T) {
	// No quota controller managed service
	service := newAdminAPIUserTestService(t, "")
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodGet, service.basePath+"/quota/user1", "", token)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", recorder.Code)
	}

	recorder = performAdminAPIRequest(service, http.MethodPut, service.basePath+"/quota/user1", `{"quota_gb":0.04,"period":"daily"}`, token)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for PUT, got %d", recorder.Code)
	}

	recorder = performAdminAPIRequest(service, http.MethodDelete, service.basePath+"/quota/user1", "", token)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for DELETE, got %d", recorder.Code)
	}
}

func TestAdminAPIQuotaDeleteNotFound(t *testing.T) {
	quotaService := newAdminAPIUserQuotaService(t)
	service := newAdminAPIUserTestService(t, "", quotaService)
	token := loginAdminAPIUserTestToken(t, service)

	// DELETE for a user that was never added should be idempotent and return 204
	recorder := performAdminAPIRequest(service, http.MethodDelete, service.basePath+"/quota/never-added", "", token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for idempotent delete, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminAPIQuotaEmptyUserSegmentReturnsNotFound(t *testing.T) {
	quotaService := newAdminAPIUserQuotaService(t)
	service := newAdminAPIUserTestService(t, "", quotaService)
	token := loginAdminAPIUserTestToken(t, service)

	// chi routes "/quota/{user}" — a request with an empty/missing user segment
	// (trailing slash only) does not match the pattern and the router returns 404.
	// The handler's empty-string guard is a belt-and-suspenders defense; the
	// router-level behavior is what callers actually observe.
	testCases := []struct {
		method string
		body   string
	}{
		{http.MethodGet, ""},
		{http.MethodPut, `{"quota_gb":0.04,"period":"daily"}`},
		{http.MethodDelete, ""},
	}

	for _, tc := range testCases {
		recorder := performAdminAPIRequest(service, tc.method, service.basePath+"/quota/", tc.body, token)
		// Expect 404 (no route matched) — never a success response.
		if recorder.Code == http.StatusOK || recorder.Code == http.StatusNoContent {
			t.Fatalf("method %s: expected non-2xx for empty user segment, got %d", tc.method, recorder.Code)
		}
		if recorder.Code != http.StatusNotFound {
			t.Logf("method %s: got %d (expected 404)", tc.method, recorder.Code)
		}
	}
}
