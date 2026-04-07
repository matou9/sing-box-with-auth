package main

import (
	"context"
	"io"
	"net/netip"
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

type quotaScenario struct {
	quotaBytes    int
	initialBytes  int
	overflowBytes int
}

type quotaProtocolFixture struct {
	name             string
	userProviderUser option.User
	serverInbound    option.Inbound
	clientOutbound   option.Outbound
}

func newTrojanQuotaFixture(name string, certPem string, keyPem string) quotaProtocolFixture {
	return quotaProtocolFixture{
		name:             name,
		userProviderUser: option.User{Name: "testuser", Password: "password"},
		serverInbound: option.Inbound{
			Type: C.TypeTrojan,
			Tag:  "trojan-in",
			Options: &option.TrojanInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen: common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
				},
				Users: []option.TrojanUser{{Name: "placeholder", Password: "placeholder"}},
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
		clientOutbound: option.Outbound{
			Type: C.TypeTrojan,
			Tag:  "trojan-out",
			Options: &option.TrojanOutboundOptions{
				ServerOptions: option.ServerOptions{
					Server: "127.0.0.1",
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
	}
}

func runTrafficQuotaProtocolTest(t *testing.T, fixture quotaProtocolFixture, scenario quotaScenario) {
	t.Helper()

	startDataServer(t, tqTestPort, scenario.overflowBytes)
	startTrafficQuotaFixtureInstance(t, tqServerPort, tqClientPort, fixture, quotaServiceOptionsForUser(fixture.userProviderUser.Name, scenario.quotaBytes))
	assertQuotaUnderLimit(t, tqClientPort, tqTestPort, scenario.initialBytes)
	assertQuotaExceededAndReconnectBlocked(t, tqClientPort, tqTestPort, scenario)
}

func startTrafficQuotaFixtureInstance(
	t *testing.T,
	srvPort uint16,
	cliPort uint16,
	fixture quotaProtocolFixture,
	quotaOpts *option.TrafficQuotaServiceOptions,
	extraServices ...option.Service,
) {
	t.Helper()

	services := []option.Service{
		{
			Type: C.TypeUserProvider,
			Tag:  "users",
			Options: &option.UserProviderServiceOptions{
				Inbounds: []string{fixture.serverInbound.Tag},
				Users:    []option.User{fixture.userProviderUser},
			},
		},
		{
			Type:    C.TypeTrafficQuota,
			Tag:     "traffic-quota",
			Options: quotaOpts,
		},
	}
	services = append(services, extraServices...)

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
			withListenPort(fixture.serverInbound, srvPort),
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			withServerPort(fixture.clientOutbound, srvPort),
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
								Outbound: fixture.clientOutbound.Tag,
							},
						},
					},
				},
			},
		},
		Services: services,
	})
}

func quotaServiceOptionsForUser(userName string, quotaBytes int) *option.TrafficQuotaServiceOptions {
	return &option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: userName, QuotaGB: trafficQuotaGB(quotaBytes), Period: "daily"},
		},
	}
}

func assertQuotaUnderLimit(t *testing.T, clientPort uint16, testPort uint16, bytes int) {
	t.Helper()

	conn := dialTrafficQuotaConn(t, clientPort, testPort)
	defer conn.Close()

	_, err := io.CopyN(io.Discard, conn, int64(bytes))
	require.NoError(t, err)
}

func assertQuotaExceededAndReconnectBlocked(t *testing.T, clientPort uint16, testPort uint16, scenario quotaScenario) {
	t.Helper()

	conn := dialTrafficQuotaConn(t, clientPort, testPort)
	defer conn.Close()

	buffer := make([]byte, 32*1024)
	totalRead := 0
	for totalRead < scenario.overflowBytes {
		n, readErr := conn.Read(buffer)
		totalRead += n
		if readErr != nil {
			break
		}
	}

	require.Less(t, totalRead, scenario.overflowBytes)

	dialer := socks.NewClient(N.SystemDialer, M.ParseSocksaddrHostPort("127.0.0.1", clientPort), socks.Version5, "", "")
	secondConn, err := dialer.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort("127.0.0.1", testPort))
	if err == nil {
		defer secondConn.Close()
		_ = secondConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, readErr := secondConn.Read(buffer)
		require.True(t, readErr != nil || n == 0)
	}
}

func dialTrafficQuotaConn(t *testing.T, clientPort uint16, testPort uint16) io.ReadWriteCloser {
	t.Helper()

	dialer := socks.NewClient(N.SystemDialer, M.ParseSocksaddrHostPort("127.0.0.1", clientPort), socks.Version5, "", "")
	conn, err := dialer.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort("127.0.0.1", testPort))
	require.NoError(t, err)
	return conn
}

func withListenPort(inbound option.Inbound, port uint16) option.Inbound {
	switch opts := inbound.Options.(type) {
	case *option.TrojanInboundOptions:
		cloned := *opts
		cloned.ListenPort = port
		inbound.Options = &cloned
	default:
		panic("unsupported inbound options in traffic quota helper")
	}
	return inbound
}

func withServerPort(outbound option.Outbound, port uint16) option.Outbound {
	switch opts := outbound.Options.(type) {
	case *option.TrojanOutboundOptions:
		cloned := *opts
		cloned.ServerPort = port
		outbound.Options = &cloned
	default:
		panic("unsupported outbound options in traffic quota helper")
	}
	return outbound
}
