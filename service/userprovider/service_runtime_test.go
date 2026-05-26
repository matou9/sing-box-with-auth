package userprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json/badoption"

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

func TestUserProviderDeleteOverlayOnlyUserDoesNotSuppressFutureSourceUser(t *testing.T) {
	service := newTestService(nil)

	err := service.loadAndPush()
	require.NoError(t, err)

	err = service.CreateUser(option.User{
		Name:     "overlay",
		Password: "overlay-password",
	})
	require.NoError(t, err)

	err = service.DeleteUser("overlay")
	require.NoError(t, err)

	service.inlineUsers = []option.User{
		{Name: "overlay", Password: "source-password"},
	}
	err = service.loadAndPush()
	require.NoError(t, err)

	require.Len(t, service.server().pushes, 4)
	require.Equal(t, []adapter.User{
		{Name: "overlay", Password: "source-password"},
	}, normalizeUsers(service.server().lastUsers()))

	user, found := service.GetUser("overlay")
	require.True(t, found)
	require.Equal(t, adapter.User{Name: "overlay", Password: "source-password"}, user)
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

// TestServiceCloseTerminatesSourceGoroutines verifies Unit 2 of the
// 2026-05-14 memory-leak fix plan: Service.Close() must synchronously wait
// for every source Run goroutine to exit. Pre-fix, sources were launched
// with bare `go xxx.Run(...)` and Close returned before they observed
// ctx.Done — leaking one goroutine per source per reload cycle.
//
// This test focuses on FileSource because its Run loop has no internal
// network I/O and therefore cleanly isolates the wg plumbing under test.
// The HTTPSource follow-up is asserted by
// TestServiceCloseTerminatesSourceGoroutinesIncludingHTTP after Unit 5's
// client-reuse + Close-idle-conns fix landed. Redis / Postgres source
// loops are exercised under their respective build-tag integration tests.
func TestServiceCloseTerminatesSourceGoroutines(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()

	tmpDir := t.TempDir()
	userFile := filepath.Join(tmpDir, "users.json")
	require.NoError(t, os.WriteFile(userFile, []byte("[]"), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	logger := log.NewNOPFactory().Logger()
	s := &Service{
		ctx:    ctx,
		cancel: cancel,
		logger: logger,
		fileSource: NewFileSource(logger, &option.UserProviderFileOptions{
			Path:           userFile,
			UpdateInterval: badoption.Duration(time.Hour),
		}),
	}

	// Replicate Service.Start's goroutine launch pattern. This exercises
	// the wg plumbing that Close() depends on without taking on Start's
	// InboundManager/ManagedUserServer wiring.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.fileSource.Run(s.ctx, func() {})
	}()

	// Allow the source loop to enter its select.
	time.Sleep(50 * time.Millisecond)

	// Close mirrors Service.Close: cancel → close transports → wg.Wait.
	cancel()
	require.NoError(t, common.Close(s.httpSource, s.redisSource, s.postgresSource))
	s.wg.Wait()

	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	after := runtime.NumGoroutine()
	if delta := after - before; delta > 2 {
		t.Fatalf("expected goroutine count to return to baseline (tolerance ±2); before=%d after=%d delta=%d", before, after, delta)
	}
}

// TestServiceCloseTerminatesSourceGoroutinesIncludingHTTP is the
// follow-up promised in Unit 2's comment: after Unit 5 made HTTPSource
// reuse a single *http.Client and release its idle conns on Close, the
// goroutine-count baseline assertion can be extended to cover it. If
// this regresses, either HTTPSource is again allocating per-fetch
// transports or Service.Close stopped calling CloseIdleConnections.
func TestServiceCloseTerminatesSourceGoroutinesIncludingHTTP(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))

	ctx, cancel := context.WithCancel(context.Background())
	logger := log.NewNOPFactory().Logger()
	s := &Service{
		ctx:    ctx,
		cancel: cancel,
		logger: logger,
		httpSource: NewHTTPSource(ctx, logger, &option.UserProviderHTTPOptions{
			URL:            httpServer.URL,
			UpdateInterval: badoption.Duration(time.Hour),
		}),
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.httpSource.Run(s.ctx, func() {})
	}()

	// Allow the initial fetch + ticker entry.
	time.Sleep(100 * time.Millisecond)

	cancel()
	require.NoError(t, common.Close(s.httpSource, s.redisSource, s.postgresSource))
	s.wg.Wait()

	// httptest.Server owns its own listener/accept goroutines and they
	// only exit on Close — they are not the leak Unit 5 is meant to
	// catch, so close it explicitly before sampling NumGoroutine.
	httpServer.Close()

	// Idle connection readLoop/writeLoop goroutines from net/http need a
	// moment to retire after CloseIdleConnections; allow generously.
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	after := runtime.NumGoroutine()
	if delta := after - before; delta > 2 {
		t.Fatalf("expected goroutine count to return to baseline after HTTPSource close (tolerance ±2); before=%d after=%d delta=%d", before, after, delta)
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
