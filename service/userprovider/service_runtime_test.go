package userprovider

import (
	"sort"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"

	"github.com/stretchr/testify/require"
)

func TestUserProviderCreateUserAddsOverlayAndPushes(t *testing.T) {
	service := newTestService([]option.User{
		{Name: "source", Password: "source-password"},
	})

	err := service.loadAndPush()
	require.NoError(t, err)
	require.Len(t, service.server().pushes, 1)

	err = service.CreateUser(option.User{
		Name:     "overlay",
		Password: "overlay-password",
	})
	require.NoError(t, err)

	require.Len(t, service.server().pushes, 2)
	require.ElementsMatch(t, []adapter.User{
		{Name: "source", Password: "source-password"},
		{Name: "overlay", Password: "overlay-password"},
	}, normalizeUsers(service.server().lastUsers()))
	require.ElementsMatch(t, []adapter.User{
		{Name: "source", Password: "source-password"},
		{Name: "overlay", Password: "overlay-password"},
	}, normalizeUsers(service.ListUsers()))

	user, found := service.GetUser("overlay")
	require.True(t, found)
	require.Equal(t, adapter.User{Name: "overlay", Password: "overlay-password"}, user)
}

func TestUserProviderUpdateUserOverridesExistingSourceValue(t *testing.T) {
	service := newTestService([]option.User{
		{Name: "sekai", Password: "source-password"},
	})

	err := service.loadAndPush()
	require.NoError(t, err)
	require.Len(t, service.server().pushes, 1)

	password := "overlay-password"
	err = service.UpdateUser("sekai", UserPatch{Password: &password})
	require.NoError(t, err)

	require.Len(t, service.server().pushes, 2)
	require.Equal(t, []adapter.User{
		{Name: "sekai", Password: "overlay-password"},
	}, normalizeUsers(service.server().lastUsers()))

	user, found := service.GetUser("sekai")
	require.True(t, found)
	require.Equal(t, adapter.User{Name: "sekai", Password: "overlay-password"}, user)
}

func TestUserProviderDeleteUserRemovesOverlayAndPushes(t *testing.T) {
	service := newTestService([]option.User{
		{Name: "source", Password: "source-password"},
	})

	err := service.loadAndPush()
	require.NoError(t, err)

	err = service.CreateUser(option.User{
		Name:     "overlay",
		Password: "overlay-password",
	})
	require.NoError(t, err)

	err = service.DeleteUser("overlay")
	require.NoError(t, err)

	require.Len(t, service.server().pushes, 3)
	require.Equal(t, []adapter.User{
		{Name: "source", Password: "source-password"},
	}, normalizeUsers(service.server().lastUsers()))

	_, found := service.GetUser("overlay")
	require.False(t, found)
	require.Equal(t, []adapter.User{
		{Name: "source", Password: "source-password"},
	}, normalizeUsers(service.ListUsers()))
}

func TestUserProviderDeleteUserSuppressesSourceBackedUser(t *testing.T) {
	service := newTestService([]option.User{
		{Name: "source", Password: "source-password"},
		{Name: "other", Password: "other-password"},
	})

	err := service.loadAndPush()
	require.NoError(t, err)

	err = service.DeleteUser("source")
	require.NoError(t, err)

	require.Len(t, service.server().pushes, 2)
	require.Equal(t, []adapter.User{
		{Name: "other", Password: "other-password"},
	}, normalizeUsers(service.server().lastUsers()))

	_, found := service.GetUser("source")
	require.False(t, found)
	require.Equal(t, []adapter.User{
		{Name: "other", Password: "other-password"},
	}, normalizeUsers(service.ListUsers()))
}

func TestUserProviderLoadAndPushDoesNotReintroduceTombstonedUser(t *testing.T) {
	service := newTestService([]option.User{
		{Name: "source", Password: "source-password"},
		{Name: "other", Password: "other-password"},
	})

	err := service.loadAndPush()
	require.NoError(t, err)

	err = service.DeleteUser("source")
	require.NoError(t, err)

	err = service.loadAndPush()
	require.NoError(t, err)

	require.Len(t, service.server().pushes, 3)
	require.Equal(t, []adapter.User{
		{Name: "other", Password: "other-password"},
	}, normalizeUsers(service.server().lastUsers()))

	_, found := service.GetUser("source")
	require.False(t, found)
}

func TestUserProviderDeleteUserKeepsUpdatedSourceBackedUserHidden(t *testing.T) {
	service := newTestService([]option.User{
		{Name: "source", Password: "source-password"},
		{Name: "other", Password: "other-password"},
	})

	err := service.loadAndPush()
	require.NoError(t, err)

	password := "overlay-password"
	err = service.UpdateUser("source", UserPatch{Password: &password})
	require.NoError(t, err)

	err = service.DeleteUser("source")
	require.NoError(t, err)

	err = service.loadAndPush()
	require.NoError(t, err)

	require.Len(t, service.server().pushes, 4)
	require.Equal(t, []adapter.User{
		{Name: "other", Password: "other-password"},
	}, normalizeUsers(service.server().lastUsers()))

	_, found := service.GetUser("source")
	require.False(t, found)
}

func newTestService(inlineUsers []option.User) *Service {
	server := &testManagedUserServer{tag: "test-in"}
	return &Service{
		logger:      log.NewNOPFactory().Logger(),
		servers:     []adapter.ManagedUserServer{server},
		inlineUsers: inlineUsers,
	}
}

func (s *Service) server() *testManagedUserServer {
	return s.servers[0].(*testManagedUserServer)
}

type testManagedUserServer struct {
	tag    string
	pushes [][]adapter.User
}

func (s *testManagedUserServer) Type() string {
	return "test"
}

func (s *testManagedUserServer) Tag() string {
	return s.tag
}

func (s *testManagedUserServer) Start(stage adapter.StartStage) error {
	return nil
}

func (s *testManagedUserServer) Close() error {
	return nil
}

func (s *testManagedUserServer) ReplaceUsers(users []adapter.User) error {
	cloned := make([]adapter.User, len(users))
	copy(cloned, users)
	s.pushes = append(s.pushes, cloned)
	return nil
}

func (s *testManagedUserServer) lastUsers() []adapter.User {
	if len(s.pushes) == 0 {
		return nil
	}
	return s.pushes[len(s.pushes)-1]
}

func normalizeUsers(users []adapter.User) []adapter.User {
	cloned := make([]adapter.User, len(users))
	copy(cloned, users)
	sort.Slice(cloned, func(i, j int) bool {
		return cloned[i].Name < cloned[j].Name
	})
	return cloned
}
