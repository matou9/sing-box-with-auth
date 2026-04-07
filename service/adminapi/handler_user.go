package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/sagernet/sing-box/option"
	userproviderservice "github.com/sagernet/sing-box/service/userprovider"
)

type UserPatch = userproviderservice.UserPatch

type namedUserRequest struct {
	Name string `json:"name"`
}

type updateUserRequest struct {
	Name     string  `json:"name"`
	Password *string `json:"password,omitempty"`
	UUID     *string `json:"uuid,omitempty"`
	AlterId  *int    `json:"alter_id,omitempty"`
	Flow     *string `json:"flow,omitempty"`
}

func (s *Service) ListUsersHandler(writer http.ResponseWriter, _ *http.Request) {
	if s.userProvider == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(s.userProvider.ListUsers()); err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
	}
}

func (s *Service) GetUserHandler(writer http.ResponseWriter, request *http.Request) {
	if s.userProvider == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var userRequest namedUserRequest
	if err := json.NewDecoder(request.Body).Decode(&userRequest); err != nil || userRequest.Name == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	user, found := s.userProvider.GetUser(userRequest.Name)
	if !found {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(user); err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
	}
}

func (s *Service) CreateUserHandler(writer http.ResponseWriter, request *http.Request) {
	if s.userProvider == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var user option.User
	if err := json.NewDecoder(request.Body).Decode(&user); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.userProvider.CreateUser(user); err != nil {
		writeUserProviderError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Service) UpdateUserHandler(writer http.ResponseWriter, request *http.Request) {
	if s.userProvider == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var userRequest updateUserRequest
	if err := json.NewDecoder(request.Body).Decode(&userRequest); err != nil || userRequest.Name == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.userProvider.UpdateUser(userRequest.Name, UserPatch{
		Password: userRequest.Password,
		UUID:     userRequest.UUID,
		AlterId:  userRequest.AlterId,
		Flow:     userRequest.Flow,
	}); err != nil {
		writeUserProviderError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Service) DeleteUserHandler(writer http.ResponseWriter, request *http.Request) {
	if s.userProvider == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var userRequest namedUserRequest
	if err := json.NewDecoder(request.Body).Decode(&userRequest); err != nil || userRequest.Name == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.userProvider.DeleteUser(userRequest.Name); err != nil {
		writeUserProviderError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func writeUserProviderError(writer http.ResponseWriter, err error) {
	writer.WriteHeader(userProviderErrorStatus(err))
}

func userProviderErrorStatus(err error) int {
	switch {
	case err == nil:
		return http.StatusNoContent
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable
	case isUserProviderNotFoundError(err):
		return http.StatusNotFound
	case isUserProviderValidationError(err):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func isUserProviderValidationError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "missing ") || strings.Contains(message, "already exists") || strings.Contains(message, "invalid ")
}

func isUserProviderNotFoundError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}
