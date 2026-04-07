package adminapi

import "net/http"

func (s *Service) SpeedHandler(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusNotImplemented)
}
