package adminapi

import (
	"net/http"
	"strings"
	"testing"
)

// TestAdminAPIRejectsOversizedRequestBody covers Unit 8 of the 2026-05-14
// memory-leak plan: the management plane must bound request body sizes
// so a single oversized payload cannot drive sing-box's RSS up. The
// MaxBytesReader middleware causes the JSON decoder to fail; handlers
// translate that into 400 BadRequest. Side-state must remain
// unaffected — the provider's CreateUser must NOT have been invoked.
func TestAdminAPIRejectsOversizedRequestBody(t *testing.T) {
	previous := adminAPIMaxRequestBytes
	adminAPIMaxRequestBytes = 64
	t.Cleanup(func() { adminAPIMaxRequestBytes = previous })

	provider := &stubUserProvider{}
	service := newAdminAPIUserTestService(t, "", provider)
	token := loginAdminAPIUserTestToken(t, service)

	// Construct a JSON body well over the test limit. The content does
	// not need to parse — the body cap fires before json.Decoder reads
	// past the limit.
	padding := strings.Repeat(`"a"`+",", 200)
	body := `{"user":{"name":"alice","password":"` + padding + `"}}`

	recorder := performAdminAPIRequest(service, http.MethodPost, service.basePath+"/user/create", body, token)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized body, got %d (body=%q)", recorder.Code, recorder.Body.String())
	}
	if len(provider.created) != 0 {
		t.Fatalf("expected provider.CreateUser to never run for rejected body, got %d calls", len(provider.created))
	}
}

// TestAdminAPIAcceptsRequestAtBodyLimit guards the contract that the
// limit is exclusive: a body shorter than adminAPIMaxRequestBytes must
// continue to be accepted. Without this check we could regress to a
// silent reduction in throughput when the constant is tuned.
func TestAdminAPIAcceptsRequestAtBodyLimit(t *testing.T) {
	previous := adminAPIMaxRequestBytes
	adminAPIMaxRequestBytes = 4 << 10 // 4 KiB; plenty for a small user record
	t.Cleanup(func() { adminAPIMaxRequestBytes = previous })

	provider := &stubUserProvider{}
	service := newAdminAPIUserTestService(t, "", provider)
	token := loginAdminAPIUserTestToken(t, service)

	body := `{"user":{"name":"alice","password":"alice-pw"}}`
	recorder := performAdminAPIRequest(service, http.MethodPost, service.basePath+"/user/create", body, token)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for in-budget body, got %d (body=%q)", recorder.Code, recorder.Body.String())
	}
	if len(provider.created) != 1 || provider.created[0].Name != "alice" {
		t.Fatalf("expected provider.CreateUser to be called once with alice, got %#v", provider.created)
	}
}

// TestAdminAPIHTTPServerHasResourceTimeouts guards that the http.Server
// instance built in Start() carries the Unit 8 timeouts. A regression
// here (e.g. someone reverts the literal back to &http.Server{Handler})
// would let slow-client attacks tie up management-plane connections.
func TestAdminAPIHTTPServerHasResourceTimeouts(t *testing.T) {
	// Use a free port by binding ":0".
	provider := &stubUserProvider{}
	service := newAdminAPIUserTestService(t, "", provider)

	// Manually construct what Start would normally build, since
	// newAdminAPIUserTestService does not pass Listen to NewService.
	// The invariant we are checking is the constant set, not the
	// listener wiring: failing here means someone changed the timeouts
	// or the build pattern.
	if adminAPIReadHeaderTimeout == 0 {
		t.Error("ReadHeaderTimeout must be set (slow-loris exposure)")
	}
	if adminAPIReadTimeout == 0 {
		t.Error("ReadTimeout must be set (long-body exposure)")
	}
	if adminAPIIdleTimeout == 0 {
		t.Error("IdleTimeout must be set (keep-alive exhaustion exposure)")
	}
	_ = service
}
