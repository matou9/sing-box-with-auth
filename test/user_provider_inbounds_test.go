package main

import (
	"context"
	"net/netip"
	"os"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/naive"
	"github.com/sagernet/sing-box/protocol/shadowsocks"
	"github.com/sagernet/sing-box/protocol/shadowtls"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

// TestUserProviderShadowsocks2022 verifies that a shadowsocks inbound with no
// inline users works with user-provider when managed mode is explicitly enabled.
func TestUserProviderShadowsocks2022(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	mainPassword := mkBase64(t, 16)
	userPassword := mkBase64(t, 16)
	clientListenPort := uint16(11020)
	serverListenPort := uint16(11021)

	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: clientListenPort,
					},
				},
			},
			{
				Type: C.TypeShadowsocks,
				Tag:  "ss-in",
				Options: &option.ShadowsocksInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverListenPort,
					},
					Method:   method,
					Password: mainPassword,
					Managed:  true,
				},
			},
		},
		Outbounds: []option.Outbound{
			{
				Type: C.TypeDirect,
			},
			{
				Type: C.TypeShadowsocks,
				Tag:  "ss-out",
				Options: &option.ShadowsocksOutboundOptions{
					ServerOptions: option.ServerOptions{
						Server:     "127.0.0.1",
						ServerPort: serverListenPort,
					},
					Method:   method,
					Password: mainPassword + ":" + userPassword,
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
								Outbound: "ss-out",
							},
						},
					},
				},
			},
		},
		Services: []option.Service{
			{
				Type: C.TypeUserProvider,
				Tag:  "main-users",
				Options: &option.UserProviderServiceOptions{
					Inbounds: []string{"ss-in"},
					Users: []option.User{
						{
							Name:     "sekai",
							Password: userPassword,
						},
					},
				},
			},
		},
	})

	// Verify the inbound is the managed multi-user implementation.
	ssIn, ok := instance.Inbound().Get("ss-in")
	require.True(t, ok)
	_, isMulti := ssIn.(*shadowsocks.MultiInbound)
	require.True(t, isMulti, "managed shadowsocks inbound should use MultiInbound")
	_, isManaged := ssIn.(adapter.ManagedUserServer)
	require.True(t, isManaged, "shadowsocks MultiInbound should implement ManagedUserServer")

	testSuit(t, clientListenPort, testPort)
}

// TestUserProviderShadowsocksLegacy verifies that a legacy AEAD shadowsocks
// inbound with no inline users works with user-provider when managed mode is
// explicitly enabled.
func TestUserProviderShadowsocksLegacy(t *testing.T) {
	method := "aes-256-gcm"
	mainPassword := "legacy-main-password-is-ignored"
	userPassword := "legacy-user-password"
	clientListenPort := uint16(11080)
	serverListenPort := uint16(11081)

	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: clientListenPort,
					},
				},
			},
			{
				Type: C.TypeShadowsocks,
				Tag:  "ss-in",
				Options: &option.ShadowsocksInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverListenPort,
					},
					Method:   method,
					Password: mainPassword,
					Managed:  true,
				},
			},
		},
		Outbounds: []option.Outbound{
			{
				Type: C.TypeDirect,
			},
			{
				Type: C.TypeShadowsocks,
				Tag:  "ss-out",
				Options: &option.ShadowsocksOutboundOptions{
					ServerOptions: option.ServerOptions{
						Server:     "127.0.0.1",
						ServerPort: serverListenPort,
					},
					Method:   method,
					Password: userPassword,
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
								Outbound: "ss-out",
							},
						},
					},
				},
			},
		},
		Services: []option.Service{
			{
				Type: C.TypeUserProvider,
				Tag:  "main-users",
				Options: &option.UserProviderServiceOptions{
					Inbounds: []string{"ss-in"},
					Users: []option.User{
						{
							Name:     "sekai",
							Password: userPassword,
						},
					},
				},
			},
		},
	})

	ssIn, ok := instance.Inbound().Get("ss-in")
	require.True(t, ok)
	_, isMulti := ssIn.(*shadowsocks.MultiInbound)
	require.True(t, isMulti, "managed legacy shadowsocks inbound should use MultiInbound")
	_, isManaged := ssIn.(adapter.ManagedUserServer)
	require.True(t, isManaged, "legacy shadowsocks MultiInbound should implement ManagedUserServer")

	testSuit(t, clientListenPort, testPort)
}

// TestUserProviderShadowsocksNoAutoUpgrade verifies that a shadowsocks inbound
// NOT referenced by any user-provider keeps its single-user mode behavior.
func TestUserProviderShadowsocksNoAutoUpgrade(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	password := mkBase64(t, 16)
	clientListenPort := uint16(11022)
	serverListenPort := uint16(11023)

	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: clientListenPort,
					},
				},
			},
			{
				Type: C.TypeShadowsocks,
				Tag:  "ss-in",
				Options: &option.ShadowsocksInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverListenPort,
					},
					Method:   method,
					Password: password,
				},
			},
		},
		Outbounds: []option.Outbound{
			{
				Type: C.TypeDirect,
			},
			{
				Type: C.TypeShadowsocks,
				Tag:  "ss-out",
				Options: &option.ShadowsocksOutboundOptions{
					ServerOptions: option.ServerOptions{
						Server:     "127.0.0.1",
						ServerPort: serverListenPort,
					},
					Method:   method,
					Password: password,
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
								Outbound: "ss-out",
							},
						},
					},
				},
			},
		},
	})

	// Without user-provider reference, the inbound should remain single-user
	// (i.e., NOT be a MultiInbound).
	ssIn, ok := instance.Inbound().Get("ss-in")
	require.True(t, ok)
	_, isMulti := ssIn.(*shadowsocks.MultiInbound)
	require.False(t, isMulti, "shadowsocks inbound without user-provider reference should not be MultiInbound")

	testSuit(t, clientListenPort, testPort)
}

func TestUserProviderShadowsocksRequiresExplicitManaged(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	mainPassword := mkBase64(t, 16)
	userPassword := mkBase64(t, 16)
	clientListenPort := uint16(11024)
	serverListenPort := uint16(11025)

	ctx, cancel := context.WithCancel(include.Context(context.Background()))
	t.Cleanup(cancel)

	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{
			Log: &option.LogOptions{
				Level: "warning",
			},
			Inbounds: []option.Inbound{
				{
					Type: C.TypeMixed,
					Tag:  "mixed-in",
					Options: &option.HTTPMixedInboundOptions{
						ListenOptions: option.ListenOptions{
							Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
							ListenPort: clientListenPort,
						},
					},
				},
				{
					Type: C.TypeShadowsocks,
					Tag:  "ss-in",
					Options: &option.ShadowsocksInboundOptions{
						ListenOptions: option.ListenOptions{
							Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
							ListenPort: serverListenPort,
						},
						Method:   method,
						Password: mainPassword,
					},
				},
			},
			Outbounds: []option.Outbound{
				{
					Type: C.TypeDirect,
				},
				{
					Type: C.TypeShadowsocks,
					Tag:  "ss-out",
					Options: &option.ShadowsocksOutboundOptions{
						ServerOptions: option.ServerOptions{
							Server:     "127.0.0.1",
							ServerPort: serverListenPort,
						},
						Method:   method,
						Password: mainPassword + ":" + userPassword,
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
									Outbound: "ss-out",
								},
							},
						},
					},
				},
			},
			Services: []option.Service{
				{
					Type: C.TypeUserProvider,
					Tag:  "main-users",
					Options: &option.UserProviderServiceOptions{
						Inbounds: []string{"ss-in"},
						Users: []option.User{
							{
								Name:     "sekai",
								Password: userPassword,
							},
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = instance.Close()
	})

	err = instance.Start()
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not support user-provider")
}

// TestUserProviderNaive verifies that a naive inbound can start without inline
// users when user-provider is configured, and that traffic authenticates via
// user-provider pushed users.
func TestUserProviderNaive(t *testing.T) {
	caPem, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	caPemContent, err := os.ReadFile(caPem)
	require.NoError(t, err)
	clientListenPort := uint16(11030)
	serverListenPort := uint16(11031)

	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: clientListenPort,
					},
				},
			},
			{
				Type: C.TypeNaive,
				Tag:  "naive-in",
				Options: &option.NaiveInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverListenPort,
					},
					// Users intentionally left empty: user-provider supplies them.
					Network: network.NetworkTCP,
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
			{
				Type: C.TypeDirect,
			},
			{
				Type: C.TypeNaive,
				Tag:  "naive-out",
				Options: &option.NaiveOutboundOptions{
					ServerOptions: option.ServerOptions{
						Server:     "127.0.0.1",
						ServerPort: serverListenPort,
					},
					Username: "sekai",
					Password: "password",
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
						TLS: &option.OutboundTLSOptions{
							Enabled:     true,
							ServerName:  "example.org",
							Certificate: []string{string(caPemContent)},
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
								Outbound: "naive-out",
							},
						},
					},
				},
			},
		},
		Services: []option.Service{
			{
				Type: C.TypeUserProvider,
				Tag:  "main-users",
				Options: &option.UserProviderServiceOptions{
					Inbounds: []string{"naive-in"},
					Users: []option.User{
						{
							Name:     "sekai",
							Password: "password",
						},
					},
				},
			},
		},
	})

	// Verify the naive inbound implements ManagedUserServer.
	naiveIn, ok := instance.Inbound().Get("naive-in")
	require.True(t, ok)
	_, isNaive := naiveIn.(*naive.Inbound)
	require.True(t, isNaive)
	_, isManaged := naiveIn.(adapter.ManagedUserServer)
	require.True(t, isManaged, "naive inbound should implement ManagedUserServer")

	testTCP(t, clientListenPort, testPort)
}

// TestUserProviderShadowTLSv3 verifies that a shadowtls v3 inbound can start
// without inline users when user-provider is configured, and that traffic
// authenticates after user-provider pushes users.
func TestUserProviderShadowTLSv3(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	ssPassword := mkBase64(t, 16)
	stPassword := "hello"
	clientListenPort := uint16(11040)
	serverListenPort := uint16(11041)
	detourListenPort := uint16(11042)

	instance := startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: clientListenPort,
					},
				},
			},
			{
				Type: C.TypeShadowTLS,
				Tag:  "st-in",
				Options: &option.ShadowTLSInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverListenPort,
						Detour:     "detour",
					},
					Handshake: option.ShadowTLSHandshakeOptions{
						ServerOptions: option.ServerOptions{
							Server:     "google.com",
							ServerPort: 443,
						},
					},
					Version: 3,
					// Users intentionally left empty: user-provider supplies them.
				},
			},
			{
				Type: C.TypeShadowsocks,
				Tag:  "detour",
				Options: &option.ShadowsocksInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: detourListenPort,
					},
					Method:   method,
					Password: ssPassword,
				},
			},
		},
		Outbounds: []option.Outbound{
			{
				Type: C.TypeShadowsocks,
				Options: &option.ShadowsocksOutboundOptions{
					Method:   method,
					Password: ssPassword,
					DialerOptions: option.DialerOptions{
						Detour: "detour",
					},
				},
			},
			{
				Type: C.TypeShadowTLS,
				Tag:  "detour",
				Options: &option.ShadowTLSOutboundOptions{
					ServerOptions: option.ServerOptions{
						Server:     "127.0.0.1",
						ServerPort: serverListenPort,
					},
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
						TLS: &option.OutboundTLSOptions{
							Enabled:    true,
							ServerName: "google.com",
						},
					},
					Version:  3,
					Password: stPassword,
				},
			},
			{
				Type: C.TypeDirect,
				Tag:  "direct",
			},
		},
		Route: &option.RouteOptions{
			Rules: []option.Rule{
				{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultRule{
						RawDefaultRule: option.RawDefaultRule{
							Inbound: []string{"detour"},
						},
						RuleAction: option.RuleAction{
							Action: C.RuleActionTypeRoute,
							RouteOptions: option.RouteActionOptions{
								Outbound: "direct",
							},
						},
					},
				},
			},
		},
		Services: []option.Service{
			{
				Type: C.TypeUserProvider,
				Tag:  "main-users",
				Options: &option.UserProviderServiceOptions{
					Inbounds: []string{"st-in"},
					Users: []option.User{
						{
							Name:     "sekai",
							Password: stPassword,
						},
					},
				},
			},
		},
	})

	// Verify the shadowtls inbound implements ManagedUserServer.
	stIn, ok := instance.Inbound().Get("st-in")
	require.True(t, ok)
	_, isShadowTLS := stIn.(*shadowtls.Inbound)
	require.True(t, isShadowTLS)
	_, isManaged := stIn.(adapter.ManagedUserServer)
	require.True(t, isManaged, "shadowtls inbound should implement ManagedUserServer")

	testTCP(t, clientListenPort, testPort)
}
