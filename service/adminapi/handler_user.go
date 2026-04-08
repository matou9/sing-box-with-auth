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
	"github.com/sagernet/sing-box/service/trafficquota"
	userproviderservice "github.com/sagernet/sing-box/service/userprovider"
	E "github.com/sagernet/sing/common/exceptions"
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
	Name       string `json:"name"`
	User       string `json:"user"`
	PurgeLimit *bool  `json:"purge_limits,omitempty"`
}

type deleteRuntimeState struct {
	user         option.User
	hasUser      bool
	quota        trafficquota.RuntimeState
	hasQuota     bool
	speed        option.SpeedLimiterUser
	hasSpeed     bool
	schedules    []speedlimiter.UserSchedule
	hasSchedules bool
}

func (r deleteUserRequest) userName() string {
	if r.User != "" {
		return r.User
	}
	return r.Name
}

func (s *Service) ListUsersHandler(writer http.ResponseWriter, _ *http.Request) {
	s.ensureManagedServices()
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
	s.ensureManagedServices()
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
	s.ensureManagedServices()
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
	var (
		quotaApplied bool
		speedApplied bool
	)
	if createRequest.Quota != nil {
		quotaConfig := *createRequest.Quota
		quotaConfig.Name = createRequest.User.Name
		if err := s.quotaController.ApplyConfig(quotaConfig); err != nil {
			s.handleCreateRollbackError(writer, createRequest.User.Name, quotaApplied, speedApplied, err)
			return
		}
		quotaApplied = true
	}
	if createRequest.Speed != nil {
		speedConfig := *createRequest.Speed
		speedConfig.Name = createRequest.User.Name
		if err := s.speedController.ApplyConfig(speedConfig); err != nil {
			s.handleCreateRollbackError(writer, createRequest.User.Name, quotaApplied, speedApplied, err)
			return
		}
		speedApplied = true
	}
	if len(createRequest.SpeedSchedules) > 0 {
		if err := s.speedController.ReplaceUserSchedules(createRequest.User.Name, createRequest.SpeedSchedules); err != nil {
			s.handleCreateRollbackError(writer, createRequest.User.Name, quotaApplied, speedApplied, err)
			return
		}
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleCreateRollbackError(writer http.ResponseWriter, name string, quotaApplied bool, speedApplied bool, createErr error) {
	rollbackErr := s.rollbackCreatedUser(name, quotaApplied, speedApplied)
	if rollbackErr != nil {
		createErr = E.Errors(createErr, E.Cause(rollbackErr, "rollback create user lifecycle"))
	}
	writeRuntimeControlError(writer, createErr)
}

func (s *Service) rollbackCreatedUser(name string, quotaApplied bool, speedApplied bool) error {
	var errs []error
	if speedApplied && s.speedController != nil {
		if err := s.speedController.RemoveConfig(name); err != nil {
			errs = append(errs, E.Cause(err, "remove speed config"))
		}
		if err := s.speedController.RemoveUserSchedules(name); err != nil {
			errs = append(errs, E.Cause(err, "remove speed schedules"))
		}
	}
	if quotaApplied && s.quotaController != nil {
		if err := s.quotaController.RemoveConfig(name); err != nil {
			errs = append(errs, E.Cause(err, "remove quota config"))
		}
	}
	if err := s.userProvider.DeleteUser(name); err != nil {
		errs = append(errs, E.Cause(err, "delete created user"))
	}
	return E.Errors(errs...)
}

func (s *Service) UpdateUserHandler(writer http.ResponseWriter, request *http.Request) {
	s.ensureManagedServices()
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
	s.ensureManagedServices()
	if s.userProvider == nil {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var userRequest deleteUserRequest
	if err := json.NewDecoder(request.Body).Decode(&userRequest); err != nil || userRequest.userName() == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	name := userRequest.userName()
	state := s.snapshotDeleteRuntimeState(name)
	if !state.hasUser {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	if err := s.userProvider.DeleteUser(name); err != nil {
		writeUserProviderError(writer, err)
		return
	}
	if err := s.cleanupDeleteRuntimeState(name); err != nil {
		restoreErr := s.restoreDeleteRuntimeState(state)
		if restoreErr != nil {
			err = E.Errors(err, E.Cause(restoreErr, "restore delete runtime state"))
		}
		writeRuntimeControlError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Service) snapshotDeleteRuntimeState(name string) deleteRuntimeState {
	var state deleteRuntimeState
	if user, ok := s.userProvider.GetUser(name); ok {
		state.user = option.User{
			Name:     user.Name,
			Password: user.Password,
			UUID:     user.UUID,
			AlterId:  user.AlterId,
			Flow:     user.Flow,
		}
		state.hasUser = true
	}
	if s.quotaController != nil {
		state.quota, state.hasQuota = s.quotaController.SnapshotState(name)
	}
	if s.speedController != nil {
		state.speed, state.hasSpeed = s.speedController.GetConfig(name)
		state.schedules, state.hasSchedules = s.speedController.GetUserSchedules(name)
	}
	return state
}

func (s *Service) cleanupDeleteRuntimeState(name string) error {
	if s.speedController != nil {
		if err := s.speedController.RemoveUserSchedules(name); err != nil {
			return err
		}
		if err := s.speedController.RemoveConfig(name); err != nil {
			return err
		}
	}
	if s.quotaController != nil {
		if err := s.quotaController.RemoveConfig(name); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) restoreDeleteRuntimeState(state deleteRuntimeState) error {
	var errs []error
	if state.hasUser {
		if err := s.userProvider.CreateUser(state.user); err != nil {
			errs = append(errs, E.Cause(err, "restore deleted user"))
		}
	}
	if s.quotaController != nil && state.hasQuota {
		if err := s.quotaController.RestoreState(state.quota); err != nil {
			errs = append(errs, E.Cause(err, "restore quota runtime state"))
		}
	}
	if s.speedController != nil && state.hasSpeed {
		if err := s.speedController.ApplyConfig(state.speed); err != nil {
			errs = append(errs, E.Cause(err, "restore speed config"))
		}
	}
	if s.speedController != nil && state.hasSchedules {
		if err := s.speedController.ReplaceUserSchedules(state.user.Name, state.schedules); err != nil {
			errs = append(errs, E.Cause(err, "restore speed schedules"))
		}
	}
	return E.Errors(errs...)
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
