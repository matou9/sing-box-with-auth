package adminapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/service/speedlimiter"
)

type speedConfigResponse struct {
	UploadMbps   int `json:"upload_mbps"`
	DownloadMbps int `json:"download_mbps"`
}

type putSpeedSchedulesRequest struct {
	Schedules []speedlimiter.UserSchedule `json:"schedules"`
}

func (s *Service) GetSpeedHandler(writer http.ResponseWriter, request *http.Request) {
	s.ensureManagedServices()
	if s.speedController == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	user := chi.URLParam(request, "user")
	if user == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	config, found := s.speedController.GetConfig(user)
	if !found {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(speedConfigResponse{
		UploadMbps:   config.UploadMbps,
		DownloadMbps: config.DownloadMbps,
	})
}

func (s *Service) PutSpeedHandler(writer http.ResponseWriter, request *http.Request) {
	s.ensureManagedServices()
	if s.speedController == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	user := chi.URLParam(request, "user")
	if user == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	var req option.SpeedLimiterUser
	if err := json.NewDecoder(request.Body).Decode(&req); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	req.Name = user
	if err := s.speedController.ApplyConfig(req); err != nil {
		writeRuntimeControlError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Service) DeleteSpeedHandler(writer http.ResponseWriter, request *http.Request) {
	s.ensureManagedServices()
	if s.speedController == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	user := chi.URLParam(request, "user")
	if user == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.speedController.RemoveConfig(user); err != nil {
		writeRuntimeControlError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Service) GetSpeedSchedulesHandler(writer http.ResponseWriter, request *http.Request) {
	s.ensureManagedServices()
	if s.speedController == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	user := chi.URLParam(request, "user")
	if user == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	schedules, found := s.speedController.GetUserSchedules(user)
	if !found {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(schedules)
}

func (s *Service) PutSpeedSchedulesHandler(writer http.ResponseWriter, request *http.Request) {
	s.ensureManagedServices()
	if s.speedController == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	user := chi.URLParam(request, "user")
	if user == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	var req putSpeedSchedulesRequest
	if err := json.NewDecoder(request.Body).Decode(&req); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.speedController.ReplaceUserSchedules(user, req.Schedules); err != nil {
		writeRuntimeControlError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Service) DeleteSpeedSchedulesHandler(writer http.ResponseWriter, request *http.Request) {
	s.ensureManagedServices()
	if s.speedController == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	user := chi.URLParam(request, "user")
	if user == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.speedController.RemoveUserSchedules(user); err != nil {
		writeRuntimeControlError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
