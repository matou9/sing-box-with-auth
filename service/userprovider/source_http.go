package userprovider

import (
	"context"
	"crypto/tls"
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

type HTTPSource struct {
	ctx            context.Context
	logger         log.ContextLogger
	url            string
	updateInterval time.Duration
	downloadDetour string
	lastEtag       string
	access         sync.RWMutex
	cachedUsers    []option.User
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
	err := s.fetch(ctx)
	if err != nil {
		s.logger.Error("initial HTTP fetch: ", err)
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
				s.logger.Error("HTTP fetch: ", err)
			} else {
				onUpdate()
			}
		}
	}
}

func (s *HTTPSource) fetch(ctx context.Context) error {
	s.logger.Debug("fetching users from ", s.url)
	var dialer N.Dialer
	if s.downloadDetour != "" {
		outboundManager := service.FromContext[adapter.OutboundManager](s.ctx)
		if outboundManager != nil {
			outbound, loaded := outboundManager.Outbound(s.downloadDetour)
			if !loaded {
				return E.New("download detour not found: ", s.downloadDetour)
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
	var httpClient *http.Client
	if dialer != nil {
		httpClient = &http.Client{
			Transport: &http.Transport{
				ForceAttemptHTTP2:   true,
				TLSHandshakeTimeout: C.TCPTimeout,
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.DialContext(ctx, network, M.ParseSocksaddr(addr))
				},
				TLSClientConfig: &tls.Config{
					Time:    ntp.TimeFuncFromContext(s.ctx),
					RootCAs: adapter.RootPoolFromContext(s.ctx),
				},
			},
		}
	} else {
		httpClient = http.DefaultClient
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
	content, err := io.ReadAll(response.Body)
	if err != nil {
		return err
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

func (s *HTTPSource) Close() error {
	return nil
}
