package adminapi

import (
	"net/http"
)

// speedPlaceholderHandler is intentionally not mounted yet.
// Later tasks will replace this with real speed admin API behavior.
func (s *Service) speedPlaceholderHandler(writer http.ResponseWriter, _ *http.Request) {
	http.Error(writer, "speed admin API placeholder", http.StatusNotImplemented)
}

func (s *Service) SpeedHandler(writer http.ResponseWriter, request *http.Request) {
	// Preserve the old symbol for local compile stability while making the placeholder explicit.
	s.speedPlaceholderHandler(writer, request)
}
