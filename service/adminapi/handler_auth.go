package adminapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

func (a *Authenticator) LoginHandler(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var login loginRequest
	if err := json.NewDecoder(request.Body).Decode(&login); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}

	expectedPassword, ok := a.admins[login.Username]
	if !ok || expectedPassword != login.Password {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}

	token, expiresAt, err := a.issueLoginTokenWithExpiry(login.Username)
	if err != nil {
		if errors.Is(err, errLoginDisabled) {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}

	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(loginResponse{
		Token:     token,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	}); err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
	}
}
