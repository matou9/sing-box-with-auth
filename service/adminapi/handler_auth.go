package adminapi

import (
	"encoding/json"
	"net/http"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string `json:"token"`
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

	token, err := a.issueLoginToken(login.Username)
	if err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}

	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(loginResponse{Token: token}); err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
	}
}
