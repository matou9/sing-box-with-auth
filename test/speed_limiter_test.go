package main

import (
	"context"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/protocol/socks"

	"github.com/stretchr/testify/require"
)

const (
	slServerPort uint16 = 20000 + iota
	slClientPort
	slTestPort
)

// startDataServer starts a TCP server on the given port that sends dataSize bytes
// to each accepted connection, then closes. Returns the listener.
func startDataServer(t *testing.T, port uint16, dataSize int) net.Listener {
	listener, err := net.Listen("tcp", M.ParseSocksaddrHostPort("127.0.0.1", port).String())
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				remaining := dataSize
				buf := make([]byte, 32*1024)
				for remaining > 0 {
					n := len(buf)
					if n > remaining {
						n = remaining
					}
					written, err := conn.Write(buf[:n])
					if err != nil {
						return
					}
					remaining -= written
				}
			}()
		}
	}()
	return listener
}

// startSinkServer starts a TCP server that reads and discards all data.
func startSinkServer(t *testing.T, port uint16) net.Listener {
	listener, err := net.Listen("tcp", M.ParseSocksaddrHostPort("127.0.0.1", port).String())
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				io.Copy(io.Discard, conn)
				conn.Close()
			}()
		}
	}()
	return listener
}

func speedLimiterInstance(t *testing.T, srvPort, cliPort uint16, certPem, keyPem string, limiterOpts *option.SpeedLimiterServiceOptions) {
	startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: cliPort,
					},
				},
			},
			{
				Type: C.TypeTrojan,
				Tag:  "trojan-in",
				Options: &option.TrojanInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: srvPort,
					},
					Users: []option.TrojanUser{
						{Name: "placeholder", Password: "placeholder"},
					},
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
						TLS: &option.InboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							CertificatePath: certPem,
							KeyPath:         keyPem,
						},
					},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeTrojan,
				Tag:  "trojan-out",
				Options: &option.TrojanOutboundOptions{
					ServerOptions: option.ServerOptions{
						Server:     "127.0.0.1",
						ServerPort: srvPort,
					},
					Password: "password",
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
						TLS: &option.OutboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							CertificatePath: certPem,
						},
					},
				},
			},
		},
		Route: &option.RouteOptions{
			Rules: []option.Rule{
				{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultRule{
						RawDefaultRule: option.RawDefaultRule{
							Inbound: []string{"mixed-in"},
						},
						RuleAction: option.RuleAction{
							Action: C.RuleActionTypeRoute,
							RouteOptions: option.RouteActionOptions{
								Outbound: "trojan-out",
							},
						},
					},
				},
			},
		},
		Services: []option.Service{
			{
				Type: C.TypeUserProvider,
				Tag:  "users",
				Options: &option.UserProviderServiceOptions{
					Inbounds: []string{"trojan-in"},
					Users: []option.User{
						{Name: "testuser", Password: "password"},
					},
				},
			},
			{
				Type: C.TypeSpeedLimiter,
				Tag:  "speed-limiter",
				Options: limiterOpts,
			},
		},
	})
}

// TestSpeedLimiterDownload verifies download throttling by reading data from
// a server through the throttled proxy path.
func TestSpeedLimiterDownload(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")

	dataSize := 250 * 1024 // 250KB
	startDataServer(t, slTestPort, dataSize)

	speedLimiterInstance(t, slServerPort, slClientPort, certPem, keyPem, &option.SpeedLimiterServiceOptions{
		Users: []option.SpeedLimiterUser{
			{Name: "testuser", UploadMbps: 1, DownloadMbps: 1},
		},
	})

	dialer := socks.NewClient(N.SystemDialer, M.ParseSocksaddrHostPort("127.0.0.1", slClientPort), socks.Version5, "", "")
	conn, err := dialer.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort("127.0.0.1", slTestPort))
	require.NoError(t, err)
	defer conn.Close()

	// Read 250KB through throttled path at 1 Mbps (125KB/s)
	buf := make([]byte, 32*1024)
	totalRead := 0
	start := time.Now()
	for totalRead < dataSize {
		n, err := conn.Read(buf)
		if err != nil {
			break
		}
		totalRead += n
	}
	elapsed := time.Since(start)

	// 250KB at 1 Mbps → ~2s, with burst allow ~0.8s minimum
	if elapsed < 800*time.Millisecond {
		t.Errorf("download too fast for 1Mbps limit: %v (read %d bytes)", elapsed, totalRead)
	}
	t.Logf("download %d bytes in %v (1Mbps limit)", totalRead, elapsed)
}

// TestSpeedLimiterUnlimited verifies unlisted users are not throttled.
func TestSpeedLimiterUnlimited(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")

	const (
		srvPort uint16 = 20010
		cliPort uint16 = 20011
		tstPort uint16 = 20012
	)

	dataSize := 250 * 1024
	startDataServer(t, tstPort, dataSize)

	speedLimiterInstance(t, srvPort, cliPort, certPem, keyPem, &option.SpeedLimiterServiceOptions{
		Users: []option.SpeedLimiterUser{
			// "other" is limited, but "testuser" (our user) is NOT
			{Name: "other", UploadMbps: 1, DownloadMbps: 1},
		},
	})

	dialer := socks.NewClient(N.SystemDialer, M.ParseSocksaddrHostPort("127.0.0.1", cliPort), socks.Version5, "", "")
	conn, err := dialer.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort("127.0.0.1", tstPort))
	require.NoError(t, err)
	defer conn.Close()

	buf := make([]byte, 32*1024)
	totalRead := 0
	start := time.Now()
	for totalRead < dataSize {
		n, err := conn.Read(buf)
		if err != nil {
			break
		}
		totalRead += n
	}
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("unlimited user download too slow: %v", elapsed)
	}
	t.Logf("unlimited download %d bytes in %v", totalRead, elapsed)
}

// TestSpeedLimiterMultiConnAggregate verifies that multiple connections from
// the same user share a single rate limiter.
func TestSpeedLimiterMultiConnAggregate(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")

	const (
		srvPort uint16 = 20020
		cliPort uint16 = 20021
		tstPort uint16 = 20022
	)

	dataPerConn := 100 * 1024 // 100KB per connection
	startDataServer(t, tstPort, dataPerConn)

	speedLimiterInstance(t, srvPort, cliPort, certPem, keyPem, &option.SpeedLimiterServiceOptions{
		Users: []option.SpeedLimiterUser{
			{Name: "testuser", UploadMbps: 1, DownloadMbps: 1},
		},
	})

	dialer := socks.NewClient(N.SystemDialer, M.ParseSocksaddrHostPort("127.0.0.1", cliPort), socks.Version5, "", "")

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := dialer.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort("127.0.0.1", tstPort))
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			io.Copy(io.Discard, conn)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// 300KB total at 1 Mbps (125KB/s) → ~2.4s with burst ~1.4s
	if elapsed < 800*time.Millisecond {
		t.Errorf("aggregate download too fast: %v for 300KB at 1Mbps", elapsed)
	}
	t.Logf("3 connections downloaded 300KB total in %v (1Mbps aggregate limit)", elapsed)
}
