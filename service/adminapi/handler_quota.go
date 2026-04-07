package adminapi

import (
	"net/http"
)

// quotaPlaceholderHandler is intentionally not mounted yet.
// Later tasks will replace this with real quota admin API behavior.
func (s *Service) quotaPlaceholderHandler(writer http.ResponseWriter, _ *http.Request) {
	http.Error(writer, "quota admin API placeholder", http.StatusNotImplemented)
}

func (s *Service) QuotaHandler(writer http.ResponseWriter, request *http.Request) {
	// Preserve the old symbol for local compile stability while making the placeholder explicit.
	s.quotaPlaceholderHandler(writer, request)
}
