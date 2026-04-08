package adminapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/sagernet/sing-box/option"
)

type quotaStatusResponse struct {
	UsageBytes int64 `json:"usage_bytes"`
	QuotaBytes int64 `json:"quota_bytes"`
	Exceeded   bool  `json:"exceeded"`
}

type putQuotaRequest struct {
	QuotaGB     float64 `json:"quota_gb"`
	Period      string  `json:"period"`
	PeriodStart string  `json:"period_start,omitempty"`
	PeriodDays  int     `json:"period_days,omitempty"`
}

func (s *Service) GetQuotaStatusHandler(writer http.ResponseWriter, request *http.Request) {
	s.ensureManagedServices()
	if s.quotaController == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	user := chi.URLParam(request, "user")
	if user == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	status, found := s.quotaController.QuotaStatus(user)
	if !found {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(quotaStatusResponse{
		UsageBytes: status.UsageBytes,
		QuotaBytes: status.QuotaBytes,
		Exceeded:   status.Exceeded,
	})
}

func (s *Service) PutQuotaHandler(writer http.ResponseWriter, request *http.Request) {
	s.ensureManagedServices()
	if s.quotaController == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	user := chi.URLParam(request, "user")
	if user == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	var req putQuotaRequest
	if err := json.NewDecoder(request.Body).Decode(&req); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.quotaController.ApplyConfig(option.TrafficQuotaUser{
		Name:        user,
		QuotaGB:     req.QuotaGB,
		Period:      req.Period,
		PeriodStart: req.PeriodStart,
		PeriodDays:  req.PeriodDays,
	}); err != nil {
		writeRuntimeControlError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Service) DeleteQuotaHandler(writer http.ResponseWriter, request *http.Request) {
	s.ensureManagedServices()
	if s.quotaController == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	user := chi.URLParam(request, "user")
	if user == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.quotaController.RemoveConfig(user); err != nil {
		writeRuntimeControlError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
