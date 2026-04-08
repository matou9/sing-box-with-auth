package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/service/speedlimiter"
	userproviderservice "github.com/sagernet/sing-box/service/userprovider"
)

type UserPatch = userproviderservice.UserPatch

type namedUserRequest struct {
	Name string `json:"name"`
}

type createUserRequest struct {
	User           option.User                 `json:"user"`
	Quota          *option.TrafficQuotaUser    `json:"quota,omitempty"`
	Speed          *option.SpeedLimiterUser    `json:"speed,omitempty"`
	SpeedSchedules []speedlimiter.UserSchedule `json:"speed_schedules,omitempty"`
}

type updateUserRequest struct {
	Name     string  `json:"name"`
	Password *string `json:"password,omitempty"`
	UUID     *string `json:"uuid,omitempty"`
	AlterId  *int    `json:"alter_id,omitempty"`
	Flow     *string `json:"flow,omitempty"`
}

type deleteUserRequest struct {
	Name string `json:"name"`
	User string `json:"user"`
}

func (r deleteUserRequest) userName() string {
	if r.User != "" {
		return r.User
	}
	return r.Name
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
	createRequest, err := decodeCreateUserRequest(request)
	if err != nil || createRequest.User.Name == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if createRequest.Quota != nil && s.quotaController == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if (createRequest.Speed != nil || len(createRequest.SpeedSchedules) > 0) && s.speedController == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if err := s.userProvider.CreateUser(createRequest.User); err != nil {
		writeUserProviderError(writer, err)
		return
	}
	if createRequest.Quota != nil {
		quotaConfig := *createRequest.Quota
		quotaConfig.Name = createRequest.User.Name
		if err := s.quotaController.ApplyConfig(quotaConfig); err != nil {
			writeRuntimeControlError(writer, err)
			return
		}
	}
	if createRequest.Speed != nil {
		speedConfig := *createRequest.Speed
		speedConfig.Name = createRequest.User.Name
		if err := s.speedController.ApplyConfig(speedConfig); err != nil {
			writeRuntimeControlError(writer, err)
			return
		}
	}
	if len(createRequest.SpeedSchedules) > 0 {
		if err := s.speedController.ReplaceUserSchedules(createRequest.User.Name, createRequest.SpeedSchedules); err != nil {
			writeRuntimeControlError(writer, err)
			return
		}
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
	var userRequest deleteUserRequest
	if err := json.NewDecoder(request.Body).Decode(&userRequest); err != nil || userRequest.userName() == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.userProvider.DeleteUser(userRequest.userName()); err != nil {
		writeUserProviderError(writer, err)
		return
	}
	if s.quotaController != nil {
		if err := s.quotaController.RemoveConfig(userRequest.userName()); err != nil {
			writeRuntimeControlError(writer, err)
			return
		}
	}
	if s.speedController != nil {
		if err := s.speedController.RemoveConfig(userRequest.userName()); err != nil {
			writeRuntimeControlError(writer, err)
			return
		}
		if err := s.speedController.RemoveUserSchedules(userRequest.userName()); err != nil {
			writeRuntimeControlError(writer, err)
			return
		}
	}
	writer.WriteHeader(http.StatusNoContent)
}

func writeUserProviderError(writer http.ResponseWriter, err error) {
	writer.WriteHeader(userProviderErrorStatus(err))
}

func writeRuntimeControlError(writer http.ResponseWriter, err error) {
	writer.WriteHeader(runtimeControlErrorStatus(err))
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

func runtimeControlErrorStatus(err error) int {
	switch {
	case err == nil:
		return http.StatusNoContent
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable
	case isRuntimeControlValidationError(err):
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

func isRuntimeControlValidationError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "missing ") ||
		strings.Contains(message, "invalid ") ||
		strings.Contains(message, "unsupported ") ||
		strings.Contains(message, "parse ")
}

func decodeCreateUserRequest(request *http.Request) (createUserRequest, error) {
	body, err := readRequestBody(request)
	if err != nil {
		return createUserRequest{}, err
	}

	var createRequest createUserRequest
	if err := json.Unmarshal(body, &createRequest); err != nil {
		return createUserRequest{}, err
	}
	if createRequest.User.Name != "" {
		return createRequest, nil
	}

	var flatUser option.User
	if err := json.Unmarshal(body, &flatUser); err != nil {
		return createUserRequest{}, err
	}
	createRequest.User = flatUser
	return createRequest, nil
}

func readRequestBody(request *http.Request) ([]byte, error) {
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(request.Body); err != nil {
		return nil, err
	}
	request.Body = http.NoBody
	return buffer.Bytes(), nil
}
