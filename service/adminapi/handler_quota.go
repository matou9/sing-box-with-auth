package adminapi

import "net/http"

func (s *Service) QuotaHandler(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusNotImplemented)
}
