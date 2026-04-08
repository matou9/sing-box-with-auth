package adminapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/service/speedlimiter"
)

func TestAdminAPISpeedGetConfig(t *testing.T) {
	speedService := newAdminAPIUserSpeedService(t)
	if err := speedService.ApplyConfig(option.SpeedLimiterUser{
		Name:         "user1",
		UploadMbps:   10,
		DownloadMbps: 20,
	}); err != nil {
		t.Fatalf("apply speed config: %v", err)
	}
	service := newAdminAPIUserTestService(t, "", speedService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodGet, service.basePath+"/speed/user1", "", token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var resp speedConfigResponse
	if err := json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.UploadMbps != 10 {
		t.Errorf("expected upload_mbps=10, got %d", resp.UploadMbps)
	}
	if resp.DownloadMbps != 20 {
		t.Errorf("expected download_mbps=20, got %d", resp.DownloadMbps)
	}
}

func TestAdminAPISpeedGetNotFound(t *testing.T) {
	speedService := newAdminAPIUserSpeedService(t)
	service := newAdminAPIUserTestService(t, "", speedService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodGet, service.basePath+"/speed/unknown", "", token)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", recorder.Code)
	}
}

func TestAdminAPISpeedPut(t *testing.T) {
	speedService := newAdminAPIUserSpeedService(t)
	service := newAdminAPIUserTestService(t, "", speedService)
	token := loginAdminAPIUserTestToken(t, service)

	body := `{"upload_mbps":5,"download_mbps":10}`
	recorder := performAdminAPIRequest(service, http.MethodPut, service.basePath+"/speed/user1", body, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", recorder.Code, recorder.Body.String())
	}

	config, found := speedService.GetConfig("user1")
	if !found {
		t.Fatal("expected speed config to be set after PUT")
	}
	if config.UploadMbps != 5 {
		t.Errorf("expected upload_mbps=5, got %d", config.UploadMbps)
	}
}

func TestAdminAPISpeedPutInvalidBody(t *testing.T) {
	speedService := newAdminAPIUserSpeedService(t)
	service := newAdminAPIUserTestService(t, "", speedService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodPut, service.basePath+"/speed/user1", "not-json", token)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
}

func TestAdminAPISpeedPutValidationError(t *testing.T) {
	speedService := newAdminAPIUserSpeedService(t)
	service := newAdminAPIUserTestService(t, "", speedService)
	token := loginAdminAPIUserTestToken(t, service)

	// No speed values and no group — should fail validation
	body := `{}`
	recorder := performAdminAPIRequest(service, http.MethodPut, service.basePath+"/speed/user1", body, token)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminAPISpeedDelete(t *testing.T) {
	speedService := newAdminAPIUserSpeedService(t)
	if err := speedService.ApplyConfig(option.SpeedLimiterUser{
		Name:       "user1",
		UploadMbps: 5,
	}); err != nil {
		t.Fatalf("apply speed config: %v", err)
	}
	service := newAdminAPIUserTestService(t, "", speedService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodDelete, service.basePath+"/speed/user1", "", token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", recorder.Code, recorder.Body.String())
	}

	if _, found := speedService.GetConfig("user1"); found {
		t.Error("expected speed config to be removed after DELETE")
	}
}

func TestAdminAPISpeedSchedulesGet(t *testing.T) {
	speedService := newAdminAPIUserSpeedService(t)
	if err := speedService.ApplyConfig(option.SpeedLimiterUser{
		Name:       "user1",
		UploadMbps: 5,
	}); err != nil {
		t.Fatalf("apply speed config: %v", err)
	}
	if err := speedService.ReplaceUserSchedules("user1", []speedlimiter.UserSchedule{
		{TimeRange: "08:00-18:00", UploadMbps: 2, DownloadMbps: 5},
	}); err != nil {
		t.Fatalf("replace schedules: %v", err)
	}
	service := newAdminAPIUserTestService(t, "", speedService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodGet, service.basePath+"/speed/user1/schedules", "", token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var schedules []speedlimiter.UserSchedule
	if err := json.NewDecoder(recorder.Body).Decode(&schedules); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(schedules) != 1 {
		t.Errorf("expected 1 schedule, got %d", len(schedules))
	}
}

func TestAdminAPISpeedSchedulesGetNotFound(t *testing.T) {
	speedService := newAdminAPIUserSpeedService(t)
	service := newAdminAPIUserTestService(t, "", speedService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodGet, service.basePath+"/speed/user1/schedules", "", token)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", recorder.Code)
	}
}

func TestAdminAPISpeedSchedulesPut(t *testing.T) {
	speedService := newAdminAPIUserSpeedService(t)
	service := newAdminAPIUserTestService(t, "", speedService)
	token := loginAdminAPIUserTestToken(t, service)

	body := `{"schedules":[{"time_range":"08:00-18:00","upload_mbps":2,"download_mbps":5}]}`
	recorder := performAdminAPIRequest(service, http.MethodPut, service.basePath+"/speed/user1/schedules", body, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", recorder.Code, recorder.Body.String())
	}

	schedules, found := speedService.GetUserSchedules("user1")
	if !found || len(schedules) == 0 {
		t.Error("expected schedules to be set after PUT")
	}
}

func TestAdminAPISpeedSchedulesPutInvalidBody(t *testing.T) {
	speedService := newAdminAPIUserSpeedService(t)
	service := newAdminAPIUserTestService(t, "", speedService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodPut, service.basePath+"/speed/user1/schedules", "bad-json", token)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
}

func TestAdminAPISpeedSchedulesDelete(t *testing.T) {
	speedService := newAdminAPIUserSpeedService(t)
	if err := speedService.ReplaceUserSchedules("user1", []speedlimiter.UserSchedule{
		{TimeRange: "08:00-18:00", UploadMbps: 2},
	}); err != nil {
		t.Fatalf("replace schedules: %v", err)
	}
	service := newAdminAPIUserTestService(t, "", speedService)
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodDelete, service.basePath+"/speed/user1/schedules", "", token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", recorder.Code, recorder.Body.String())
	}

	if _, found := speedService.GetUserSchedules("user1"); found {
		t.Error("expected schedules to be removed after DELETE")
	}
}

func TestAdminAPISpeedRequiresAuth(t *testing.T) {
	speedService := newAdminAPIUserSpeedService(t)
	service := newAdminAPIUserTestService(t, "", speedService)

	recorder := performAdminAPIRequest(service, http.MethodGet, service.basePath+"/speed/user1", "", "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
}

func TestAdminAPISpeedControllerUnavailable(t *testing.T) {
	// No speed controller managed service
	service := newAdminAPIUserTestService(t, "")
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodGet, service.basePath+"/speed/user1", "", token)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", recorder.Code)
	}

	recorder = performAdminAPIRequest(service, http.MethodPut, service.basePath+"/speed/user1", `{"upload_mbps":5}`, token)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for PUT, got %d", recorder.Code)
	}

	recorder = performAdminAPIRequest(service, http.MethodDelete, service.basePath+"/speed/user1", "", token)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for DELETE, got %d", recorder.Code)
	}
}

func TestAdminAPISpeedSchedulesControllerUnavailable(t *testing.T) {
	// No speed controller managed service — schedule sub-routes must also return 503
	service := newAdminAPIUserTestService(t, "")
	token := loginAdminAPIUserTestToken(t, service)

	recorder := performAdminAPIRequest(service, http.MethodGet, service.basePath+"/speed/user1/schedules", "", token)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET schedules: expected 503, got %d", recorder.Code)
	}

	recorder = performAdminAPIRequest(service, http.MethodPut, service.basePath+"/speed/user1/schedules", `{"schedules":[]}`, token)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT schedules: expected 503, got %d", recorder.Code)
	}

	recorder = performAdminAPIRequest(service, http.MethodDelete, service.basePath+"/speed/user1/schedules", "", token)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE schedules: expected 503, got %d", recorder.Code)
	}
}
