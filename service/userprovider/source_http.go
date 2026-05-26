package userprovider

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
	"github.com/sagernet/sing/service"
)

// errHTTPSourceClosed is returned by ensureClient after Close has been
// observed. It signals fetch (and its caller, Run) that the source is
// shut down so they exit silently instead of logging an error or, worse,
// rebuilding a transport that nobody will ever release.
var errHTTPSourceClosed = errors.New("user-provider http source closed")

const (
	// httpSourceIdleConnTimeout / MaxIdleConns / MaxIdleConnsPerHost bound
	// the persistent-connection pool so idle conns are reclaimed even when
	// the upstream stays available; they mirror http.DefaultTransport's
	// conservative defaults to avoid surprising regressions.
	httpSourceIdleConnTimeout     = 90 * time.Second
	httpSourceMaxIdleConns        = 16
	httpSourceMaxIdleConnsPerHost = 4

	defaultHTTPSourceMaxResponseBytes int64 = 32 << 20 // 32 MiB
)

// httpSourceMaxResponseBytes caps the response body size accepted by
// fetch. Exposed as a package variable so tests can shrink it without
// having to fabricate large responses. Default is sized for ~100K users
// at ~300 bytes each with comfortable headroom.
var httpSourceMaxResponseBytes = defaultHTTPSourceMaxResponseBytes

type HTTPSource struct {
	ctx            context.Context
	logger         log.ContextLogger
	url            string
	updateInterval time.Duration
	downloadDetour string
	lastEtag       string
	access         sync.RWMutex
	cachedUsers    []option.User

	// clientMu protects lazy client construction, CloseIdleConnections
	// during shutdown, and the closed flag. It is intentionally separate
	// from `access` (which guards cachedUsers) so fetch-in-flight cannot
	// block ListUsers / CachedUsers callers.
	clientMu sync.Mutex
	client   *http.Client
	closed   bool
}

func NewHTTPSource(ctx context.Context, logger log.ContextLogger, options *option.UserProviderHTTPOptions) *HTTPSource {
	updateInterval := time.Duration(options.UpdateInterval)
	if updateInterval == 0 {
		updateInterval = 5 * time.Minute
	}
	return &HTTPSource{
		ctx:            ctx,
		logger:         logger,
		url:            options.URL,
		updateInterval: updateInterval,
		downloadDetour: options.DownloadDetour,
	}
}

func (s *HTTPSource) CachedUsers() []option.User {
	s.access.RLock()
	defer s.access.RUnlock()
	return s.cachedUsers
}

func (s *HTTPSource) Run(ctx context.Context, onUpdate func()) {
	// Initial fetch
	if err := s.fetch(ctx); err != nil {
		if !errors.Is(err, errHTTPSourceClosed) {
			s.logger.Error("initial HTTP fetch: ", err)
		}
	} else {
		onUpdate()
	}
	ticker := time.NewTicker(s.updateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := s.fetch(ctx)
			if err != nil {
				if errors.Is(err, errHTTPSourceClosed) {
					return
				}
				s.logger.Error("HTTP fetch: ", err)
			} else {
				onUpdate()
			}
		}
	}
}

// ensureClient returns the cached *http.Client, lazily constructing it
// on first use. After Close has been observed it refuses to build a new
// client, returning errHTTPSourceClosed; without this guard a fetch
// arriving after Close (e.g. a stuck Run loop, a misbehaving test, or a
// future caller) would silently allocate a fresh transport whose idle
// connection goroutines would never be reclaimed — the exact leak Unit 5
// is meant to plug.
func (s *HTTPSource) ensureClient() (*http.Client, error) {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if s.closed {
		return nil, errHTTPSourceClosed
	}
	if s.client != nil {
		return s.client, nil
	}
	client, err := s.buildClientLocked()
	if err != nil {
		return nil, err
	}
	s.client = client
	return s.client, nil
}

func (s *HTTPSource) buildClientLocked() (*http.Client, error) {
	var dialer N.Dialer
	if s.downloadDetour != "" {
		outboundManager := service.FromContext[adapter.OutboundManager](s.ctx)
		if outboundManager != nil {
			outbound, loaded := outboundManager.Outbound(s.downloadDetour)
			if !loaded {
				return nil, E.New("download detour not found: ", s.downloadDetour)
			}
			dialer = outbound
		}
	}
	if dialer == nil {
		outboundManager := service.FromContext[adapter.OutboundManager](s.ctx)
		if outboundManager != nil {
			dialer = outboundManager.Default()
		}
	}
	if dialer == nil {
		// Fall back to the stdlib default client when there is no
		// outbound manager in context (typical for tests and embedded
		// usage). We deliberately do NOT call CloseIdleConnections on
		// this on shutdown — it is process-wide shared state.
		return http.DefaultClient, nil
	}
	return &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: C.TCPTimeout,
			IdleConnTimeout:     httpSourceIdleConnTimeout,
			MaxIdleConns:        httpSourceMaxIdleConns,
			MaxIdleConnsPerHost: httpSourceMaxIdleConnsPerHost,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, M.ParseSocksaddr(addr))
			},
			TLSClientConfig: &tls.Config{
				Time:    ntp.TimeFuncFromContext(s.ctx),
				RootCAs: adapter.RootPoolFromContext(s.ctx),
			},
		},
	}, nil
}

func (s *HTTPSource) fetch(ctx context.Context) error {
	s.logger.Debug("fetching users from ", s.url)
	httpClient, err := s.ensureClient()
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, "GET", s.url, nil)
	if err != nil {
		return err
	}
	if s.lastEtag != "" {
		request.Header.Set("If-None-Match", s.lastEtag)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotModified:
		s.logger.Debug("users not modified")
		return nil
	default:
		return E.New("unexpected status: ", response.Status)
	}
	// Cap body size to prevent a misbehaving or hostile upstream from
	// driving sing-box's RSS arbitrarily large. ReadAll + json.Unmarshal
	// preserves the existing trailing-garbage rejection semantics
	// (json.Unmarshal requires the entire input to be a single JSON
	// value), so we don't need a streaming decoder here.
	bodyReader := http.MaxBytesReader(nil, response.Body, httpSourceMaxResponseBytes)
	content, err := io.ReadAll(bodyReader)
	if err != nil {
		return E.Cause(err, "read users response")
	}
	var users []option.User
	err = json.Unmarshal(content, &users)
	if err != nil {
		return E.Cause(err, "parse users response")
	}
	eTagHeader := response.Header.Get("Etag")
	if eTagHeader != "" {
		s.lastEtag = eTagHeader
	}
	s.access.Lock()
	s.cachedUsers = users
	s.access.Unlock()
	s.logger.Info("fetched ", len(users), " users from HTTP")
	return nil
}

// Close marks the source as shut down and releases any pooled idle
// connections held by the cached client. Idempotent: a second Close is a
// no-op. The stdlib's http.DefaultClient is process-wide shared state, so
// it is deliberately not poked even if we ended up using it as the
// fallback dialer.
func (s *HTTPSource) Close() error {
	// nil-receiver tolerance matches RedisSource / PostgresSource Close
	// so common.Close(s.httpSource, ...) works when the source is unset.
	if s == nil {
		return nil
	}
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.client != nil && s.client != http.DefaultClient {
		s.client.CloseIdleConnections()
	}
	return nil
}
