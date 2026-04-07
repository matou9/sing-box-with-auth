# Inbound Traffic Quota Matrix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an end-to-end protocol matrix for `traffic-quota` so all supported user-aware inbound protocols are verified for under-quota success, over-quota cutoff, and post-exhaustion reconnect rejection.

**Architecture:** Build a shared quota-test helper in the `test/` module that constructs a self-contained server/client path using `user-provider` plus `traffic-quota`, then layer explicit protocol-specific tests on top of that helper. Implement the matrix in two waves so the helper design is proven on the most stable protocols before extending to QUIC- and transport-specific inbounds.

**Tech Stack:** Go test module in `test/`, `option.Options` self-tests, `user-provider`, `traffic-quota`, protocol-specific inbound/outbound option structs, Docker-backed protocol fixtures where already used by the existing suite.

---

## File Structure

- Create: `test/traffic_quota_helper_test.go`
  - Shared fixture types, common instance builder, normal-transfer assertion, quota-exhaustion assertion, reconnect-rejected assertion.
- Modify: `test/traffic_quota_test.go`
  - Keep representative quota tests here, including the existing Trojan-style representative test and the representative `10 MB` case.
- Create: `test/traffic_quota_tcp_inbounds_test.go`
  - Wave 1 tests for `socks`, `http`, and `mixed`.
- Create: `test/traffic_quota_v2ray_inbounds_test.go`
  - Wave 1 tests for `trojan`, `vless`, and `vmess`.
- Create: `test/traffic_quota_shadowsocks_test.go`
  - Wave 1 test for `shadowsocks` multi-user mode.
- Create: `test/traffic_quota_quic_inbounds_test.go`
  - Wave 2 tests for `hysteria`, `hysteria2`, and `tuic`.
- Create: `test/traffic_quota_special_inbounds_test.go`
  - Wave 2 tests for `naive`, `shadowtls`, and `anytls`.

Keep helper logic in one file and keep protocol definitions in grouped files so failures remain easy to localize.

## Common Test Conventions

- Fast sweep quota:
  - quota: `1 * 1024 * 1024`
  - initial transfer: `256 * 1024`
  - overflow transfer: `2 * 1024 * 1024`
- Representative quota:
  - quota: `10 * 1024 * 1024`
  - overflow transfer: `12 * 1024 * 1024`
- Tests run in memory mode only. Do not add Redis/Postgres persistence to this matrix.
- Reuse `createSelfSignedCertificate`, `startInstance`, `startDataServer`, `testTCP`, `testSuit`, and `testSuitLargeUDP` patterns where appropriate.

## Task 1: Build Shared Quota Test Helper

**Files:**
- Create: `test/traffic_quota_helper_test.go`
- Modify: `test/traffic_quota_test.go`
- Test: `test/traffic_quota_test.go`

- [ ] **Step 1: Write the failing helper-oriented test**

Add a small helper-driven test to prove the abstraction compiles before filling out the matrix:

```go
func TestTrafficQuotaHelperBasicFlow(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")

	fixture := quotaProtocolFixture{
		name: "trojan-basic",
		userProviderUser: option.User{
			Name:     "testuser",
			Password: "password",
		},
		serverInbound: option.Inbound{
			Type: C.TypeTrojan,
			Tag:  "trojan-in",
			Options: &option.TrojanInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
					ListenPort: tqServerPort,
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
					Server:     "127.0.0.1",
					ServerPort: tqServerPort,
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

	runTrafficQuotaProtocolTest(t, fixture, quotaScenario{
		quotaBytes:    1 * 1024 * 1024,
		initialBytes:  256 * 1024,
		overflowBytes: 2 * 1024 * 1024,
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd test && env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go test -run TestTrafficQuotaHelperBasicFlow .
```

Expected: FAIL with undefined `quotaProtocolFixture`, `quotaScenario`, or `runTrafficQuotaProtocolTest`.

- [ ] **Step 3: Write the shared helper implementation**

Create `test/traffic_quota_helper_test.go` with the shared fixture and scenario types plus common setup:

```go
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
	clientTest       func(t *testing.T, clientPort uint16, testPort uint16)
}

func runTrafficQuotaProtocolTest(t *testing.T, fixture quotaProtocolFixture, scenario quotaScenario) {
	t.Helper()

	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	startDataServer(t, tqTestPort, scenario.overflowBytes)
	startTrafficQuotaMatrixInstance(t, tqServerPort, tqClientPort, certPem, keyPem, fixture, scenario.quotaBytes)
	assertQuotaUnderLimit(t, tqClientPort, tqTestPort, scenario.initialBytes)
	assertQuotaExceededAndReconnectBlocked(t, tqClientPort, tqTestPort, scenario.overflowBytes)
}

func startTrafficQuotaMatrixInstance(
	t *testing.T,
	srvPort uint16,
	cliPort uint16,
	certPem string,
	keyPem string,
	fixture quotaProtocolFixture,
	quotaBytes int,
) {
	t.Helper()

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
			fixture.serverInbound,
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			fixture.clientOutbound,
		},
		Route: &option.RouteOptions{
			Rules: []option.Rule{
				{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultRule{
						RawDefaultRule: option.RawDefaultRule{Inbound: []string{"mixed-in"}},
						RuleAction: option.RuleAction{
							Action: C.RuleActionTypeRoute,
							RouteOptions: option.RouteActionOptions{Outbound: fixture.clientOutbound.Tag},
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
					Inbounds: []string{fixture.serverInbound.Tag},
					Users:    []option.User{fixture.userProviderUser},
				},
			},
			{
				Type: C.TypeTrafficQuota,
				Tag:  "traffic-quota",
				Options: &option.TrafficQuotaServiceOptions{
					Users: []option.TrafficQuotaUser{{
						Name:    fixture.userProviderUser.Name,
						QuotaGB: trafficQuotaGB(quotaBytes),
						Period:  "daily",
					}},
				},
			},
		},
	})
}
```

Add the common assertions:

```go
func assertQuotaUnderLimit(t *testing.T, clientPort uint16, testPort uint16, bytes int) {
	t.Helper()

	dialer := socks.NewClient(N.SystemDialer, M.ParseSocksaddrHostPort("127.0.0.1", clientPort), socks.Version5, "", "")
	conn, err := dialer.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort("127.0.0.1", testPort))
	require.NoError(t, err)
	defer conn.Close()

	_, err = io.CopyN(io.Discard, conn, int64(bytes))
	require.NoError(t, err)
}

func assertQuotaExceededAndReconnectBlocked(t *testing.T, clientPort uint16, testPort uint16, bytes int) {
	t.Helper()

	dialer := socks.NewClient(N.SystemDialer, M.ParseSocksaddrHostPort("127.0.0.1", clientPort), socks.Version5, "", "")
	conn, err := dialer.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort("127.0.0.1", testPort))
	require.NoError(t, err)
	defer conn.Close()

	buffer := make([]byte, 32*1024)
	totalRead := 0
	for totalRead < bytes {
		n, readErr := conn.Read(buffer)
		totalRead += n
		if readErr != nil {
			break
		}
	}
	require.Less(t, totalRead, bytes)

	secondConn, err := dialer.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort("127.0.0.1", testPort))
	if err == nil {
		defer secondConn.Close()
		_ = secondConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, readErr := secondConn.Read(buffer)
		require.True(t, readErr != nil || n == 0)
	}
}
```

- [ ] **Step 4: Run the helper test to verify it passes**

Run:

```bash
cd test && env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go test -run TestTrafficQuotaHelperBasicFlow .
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add test/traffic_quota_helper_test.go test/traffic_quota_test.go
git commit -m "test: extract shared traffic quota protocol helper"
```

## Task 2: Refactor Representative Trojan Tests and Add 10 MB Case

**Files:**
- Modify: `test/traffic_quota_test.go`
- Test: `test/traffic_quota_test.go`

- [ ] **Step 1: Replace the current ad hoc setup with the shared helper**

Refactor the existing quota test file to use the new fixture helper:

```go
func TestTrojanTrafficQuota(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")

	fixture := quotaProtocolFixture{
		name: "trojan",
		userProviderUser: option.User{Name: "testuser", Password: "password"},
		serverInbound: option.Inbound{
			Type: C.TypeTrojan,
			Tag:  "trojan-in",
			Options: &option.TrojanInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
					ListenPort: tqServerPort,
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
					Server:     "127.0.0.1",
					ServerPort: tqServerPort,
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

	runTrafficQuotaProtocolTest(t, fixture, quotaScenario{
		quotaBytes:    1 * 1024 * 1024,
		initialBytes:  256 * 1024,
		overflowBytes: 2 * 1024 * 1024,
	})
}
```

- [ ] **Step 2: Add a representative 10 MB Trojan test**

```go
func TestTrojanTrafficQuota10MB(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")

	fixture := quotaProtocolFixture{
		name: "trojan-10mb",
		userProviderUser: option.User{Name: "testuser", Password: "password"},
		serverInbound: option.Inbound{
			Type: C.TypeTrojan,
			Tag:  "trojan-in",
			Options: &option.TrojanInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
					ListenPort: tqServerPort,
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
					Server:     "127.0.0.1",
					ServerPort: tqServerPort,
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

	runTrafficQuotaProtocolTest(t, fixture, quotaScenario{
		quotaBytes:    10 * 1024 * 1024,
		initialBytes:  256 * 1024,
		overflowBytes: 12 * 1024 * 1024,
	})
}
```

- [ ] **Step 3: Run the representative quota tests**

Run:

```bash
cd test && env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go test -run 'TestTrojanTrafficQuota|TestTrojanTrafficQuota10MB' .
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add test/traffic_quota_test.go
git commit -m "test: cover trojan traffic quota with shared matrix helper"
```

## Task 3: Add Wave 1 TCP Entry Inbound Tests

**Files:**
- Create: `test/traffic_quota_tcp_inbounds_test.go`
- Test: `test/traffic_quota_tcp_inbounds_test.go`

- [ ] **Step 1: Write the failing TCP entry-inbound tests**

```go
func TestSOCKSTrafficQuota(t *testing.T) {}

func TestHTTPTrafficQuota(t *testing.T) {}

func TestMixedTrafficQuota(t *testing.T) {}
```

- [ ] **Step 2: Run the new tests to verify they fail meaningfully**

Run:

```bash
cd test && env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go test -run 'TestSOCKSTrafficQuota|TestHTTPTrafficQuota|TestMixedTrafficQuota' .
```

Expected: FAIL because the tests are empty or missing assertions.

- [ ] **Step 3: Implement `socks`, `http`, and `mixed` quota tests**

Add concrete helper-driven tests:

```go
func TestSOCKSTrafficQuota(t *testing.T) {
	fixture := quotaProtocolFixture{
		name: "socks",
		userProviderUser: option.User{Name: "testuser", Password: "password"},
		serverInbound: option.Inbound{
			Type: C.TypeSOCKS,
			Tag:  "socks-in",
			Options: &option.SocksInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
					ListenPort: tqServerPort,
				},
				Users: []auth.User{{Username: "placeholder", Password: "placeholder"}},
			},
		},
		clientOutbound: option.Outbound{
			Type: C.TypeSOCKS,
			Tag:  "socks-out",
			Options: &option.SOCKSOutboundOptions{
				ServerOptions: option.ServerOptions{
					Server:     "127.0.0.1",
					ServerPort: tqServerPort,
				},
				Username: "testuser",
				Password: "password",
			},
		},
	}
	runTrafficQuotaProtocolTest(t, fixture, quotaScenario{quotaBytes: 1 * 1024 * 1024, initialBytes: 256 * 1024, overflowBytes: 2 * 1024 * 1024})
}

func TestHTTPTrafficQuota(t *testing.T) {
	fixture := quotaProtocolFixture{
		name: "http",
		userProviderUser: option.User{Name: "testuser", Password: "password"},
		serverInbound: option.Inbound{
			Type: C.TypeHTTP,
			Tag:  "http-in",
			Options: &option.HTTPMixedInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
					ListenPort: tqServerPort,
				},
				Users: []auth.User{{Username: "placeholder", Password: "placeholder"}},
			},
		},
		clientOutbound: option.Outbound{
			Type: C.TypeHTTP,
			Tag:  "http-out",
			Options: &option.HTTPOutboundOptions{
				ServerOptions: option.ServerOptions{
					Server:     "127.0.0.1",
					ServerPort: tqServerPort,
				},
				Username: "testuser",
				Password: "password",
			},
		},
	}
	runTrafficQuotaProtocolTest(t, fixture, quotaScenario{quotaBytes: 1 * 1024 * 1024, initialBytes: 256 * 1024, overflowBytes: 2 * 1024 * 1024})
}

func TestMixedTrafficQuota(t *testing.T) {
	fixture := quotaProtocolFixture{
		name: "mixed",
		userProviderUser: option.User{Name: "testuser", Password: "password"},
		serverInbound: option.Inbound{
			Type: C.TypeMixed,
			Tag:  "mixed-server",
			Options: &option.HTTPMixedInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
					ListenPort: tqServerPort,
				},
				Users: []auth.User{{Username: "placeholder", Password: "placeholder"}},
			},
		},
		clientOutbound: option.Outbound{
			Type: C.TypeSOCKS,
			Tag:  "mixed-out",
			Options: &option.SOCKSOutboundOptions{
				ServerOptions: option.ServerOptions{
					Server:     "127.0.0.1",
					ServerPort: tqServerPort,
				},
				Username: "testuser",
				Password: "password",
			},
		},
	}
	runTrafficQuotaProtocolTest(t, fixture, quotaScenario{quotaBytes: 1 * 1024 * 1024, initialBytes: 256 * 1024, overflowBytes: 2 * 1024 * 1024})
}
```

- [ ] **Step 4: Run the TCP entry-inbound tests**

Run:

```bash
cd test && env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go test -run 'TestSOCKSTrafficQuota|TestHTTPTrafficQuota|TestMixedTrafficQuota' .
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add test/traffic_quota_tcp_inbounds_test.go
git commit -m "test: add traffic quota coverage for socks http and mixed inbounds"
```

## Task 4: Add Wave 1 Protocol Tests for Trojan, VLESS, VMess, and Shadowsocks Multi-User

**Files:**
- Create: `test/traffic_quota_v2ray_inbounds_test.go`
- Create: `test/traffic_quota_shadowsocks_test.go`
- Test: `test/traffic_quota_v2ray_inbounds_test.go`
- Test: `test/traffic_quota_shadowsocks_test.go`

- [ ] **Step 1: Write the failing V2Ray-family and Shadowsocks test entries**

```go
func TestVLESSTrafficQuota(t *testing.T) {}

func TestVMessTrafficQuota(t *testing.T) {}

func TestShadowsocksMultiUserTrafficQuota(t *testing.T) {}
```

- [ ] **Step 2: Run the tests to confirm they fail**

Run:

```bash
cd test && env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go test -run 'TestVLESSTrafficQuota|TestVMessTrafficQuota|TestShadowsocksMultiUserTrafficQuota' .
```

Expected: FAIL because the protocol tests are not implemented yet.

- [ ] **Step 3: Implement `vless` and `vmess` tests**

Use existing option structs and self-test patterns:

```go
func TestVLESSTrafficQuota(t *testing.T) {
	userID := "b831381d-6324-4d53-ad4f-8cda48b30811"
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")

	fixture := quotaProtocolFixture{
		name: "vless",
		userProviderUser: option.User{Name: "testuser", UUID: userID},
		serverInbound: option.Inbound{
			Type: C.TypeVLESS,
			Tag:  "vless-in",
			Options: &option.VLESSInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
					ListenPort: tqServerPort,
				},
				Users: []option.VLESSUser{{Name: "placeholder", UUID: userID}},
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
			Type: C.TypeVLESS,
			Tag:  "vless-out",
			Options: &option.VLESSOutboundOptions{
				ServerOptions: option.ServerOptions{
					Server:     "127.0.0.1",
					ServerPort: tqServerPort,
				},
				UUID: userID,
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
	runTrafficQuotaProtocolTest(t, fixture, quotaScenario{quotaBytes: 1 * 1024 * 1024, initialBytes: 256 * 1024, overflowBytes: 2 * 1024 * 1024})
}

func TestVMessTrafficQuota(t *testing.T) {
	userID := "b831381d-6324-4d53-ad4f-8cda48b30811"
	fixture := quotaProtocolFixture{
		name: "vmess",
		userProviderUser: option.User{Name: "testuser", UUID: userID, AlterId: 0},
		serverInbound: option.Inbound{
			Type: C.TypeVMess,
			Tag:  "vmess-in",
			Options: &option.VMessInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
					ListenPort: tqServerPort,
				},
				Users: []option.VMessUser{{Name: "placeholder", UUID: userID, AlterId: 0}},
			},
		},
		clientOutbound: option.Outbound{
			Type: C.TypeVMess,
			Tag:  "vmess-out",
			Options: &option.VMessOutboundOptions{
				ServerOptions: option.ServerOptions{
					Server:     "127.0.0.1",
					ServerPort: tqServerPort,
				},
				UUID:    userID,
				Security: "auto",
				AlterId: 0,
			},
		},
	}
	runTrafficQuotaProtocolTest(t, fixture, quotaScenario{quotaBytes: 1 * 1024 * 1024, initialBytes: 256 * 1024, overflowBytes: 2 * 1024 * 1024})
}
```

- [ ] **Step 4: Implement `shadowsocks` multi-user mode test**

Use the multi-user path, not the single-password path:

```go
func TestShadowsocksMultiUserTrafficQuota(t *testing.T) {
	method := "aes-128-gcm"
	password := mkBase64(t, 16)

	fixture := quotaProtocolFixture{
		name: "shadowsocks-multi-user",
		userProviderUser: option.User{Name: "testuser", Password: password},
		serverInbound: option.Inbound{
			Type: C.TypeShadowsocks,
			Tag:  "ss-in",
			Options: &option.ShadowsocksInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
					ListenPort: tqServerPort,
				},
				Method: method,
				Users:  []option.ShadowsocksUser{{Name: "placeholder", Password: password}},
			},
		},
		clientOutbound: option.Outbound{
			Type: C.TypeShadowsocks,
			Tag:  "ss-out",
			Options: &option.ShadowsocksOutboundOptions{
				ServerOptions: option.ServerOptions{
					Server:     "127.0.0.1",
					ServerPort: tqServerPort,
				},
				Method:   method,
				Password: password,
			},
		},
	}
	runTrafficQuotaProtocolTest(t, fixture, quotaScenario{quotaBytes: 1 * 1024 * 1024, initialBytes: 256 * 1024, overflowBytes: 2 * 1024 * 1024})
}
```

- [ ] **Step 5: Run Wave 1 V2Ray-family and Shadowsocks tests**

Run:

```bash
cd test && env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go test -run 'TestVLESSTrafficQuota|TestVMessTrafficQuota|TestShadowsocksMultiUserTrafficQuota' .
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add test/traffic_quota_v2ray_inbounds_test.go test/traffic_quota_shadowsocks_test.go
git commit -m "test: add wave 1 traffic quota matrix for vless vmess and shadowsocks"
```

## Task 5: Add Wave 2 QUIC Inbound Tests

**Files:**
- Create: `test/traffic_quota_quic_inbounds_test.go`
- Test: `test/traffic_quota_quic_inbounds_test.go`

- [ ] **Step 1: Write the failing QUIC protocol test entries**

```go
func TestHysteriaTrafficQuota(t *testing.T) {}

func TestHysteria2TrafficQuota(t *testing.T) {}

func TestTUICTrafficQuota(t *testing.T) {}
```

- [ ] **Step 2: Run the QUIC tests to confirm they fail**

Run:

```bash
cd test && env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go test -run 'TestHysteriaTrafficQuota|TestHysteria2TrafficQuota|TestTUICTrafficQuota' .
```

Expected: FAIL because the tests are not implemented yet.

- [ ] **Step 3: Implement `hysteria`, `hysteria2`, and `tuic` tests**

Follow the existing self-test patterns and keep the matrix data sizes small:

```go
func TestHysteriaTrafficQuota(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	fixture := quotaProtocolFixture{
		name: "hysteria",
		userProviderUser: option.User{Name: "testuser", Password: "password"},
		serverInbound: option.Inbound{
			Type: C.TypeHysteria,
			Tag:  "hy-in",
			Options: &option.HysteriaInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
					ListenPort: tqServerPort,
				},
				UpMbps:   100,
				DownMbps: 100,
				Users:    []option.HysteriaUser{{Name: "placeholder", AuthString: "password"}},
				Obfs:     "quota-obfs",
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
			Type: C.TypeHysteria,
			Tag:  "hy-out",
			Options: &option.HysteriaOutboundOptions{
				ServerOptions: option.ServerOptions{
					Server:     "127.0.0.1",
					ServerPort: tqServerPort,
				},
				UpMbps:     100,
				DownMbps:   100,
				AuthString: "password",
				Obfs:       "quota-obfs",
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
	runTrafficQuotaProtocolTest(t, fixture, quotaScenario{quotaBytes: 1 * 1024 * 1024, initialBytes: 256 * 1024, overflowBytes: 2 * 1024 * 1024})
}
```

Use the same style for `hysteria2` and `tuic`, copying the stable self-test options from `test/hysteria2_test.go` and `test/tuic_test.go`.

- [ ] **Step 4: Run the QUIC inbound tests**

Run:

```bash
cd test && env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go test -run 'TestHysteriaTrafficQuota|TestHysteria2TrafficQuota|TestTUICTrafficQuota' .
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add test/traffic_quota_quic_inbounds_test.go
git commit -m "test: add traffic quota coverage for quic user inbounds"
```

## Task 6: Add Wave 2 Special Transport Tests

**Files:**
- Create: `test/traffic_quota_special_inbounds_test.go`
- Test: `test/traffic_quota_special_inbounds_test.go`

- [ ] **Step 1: Write the failing special-transport test entries**

```go
func TestNaiveTrafficQuota(t *testing.T) {}

func TestShadowTLSTrafficQuota(t *testing.T) {}

func TestAnyTLSTrafficQuota(t *testing.T) {}
```

- [ ] **Step 2: Run the special-transport tests to confirm they fail**

Run:

```bash
cd test && env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go test -run 'TestNaiveTrafficQuota|TestShadowTLSTrafficQuota|TestAnyTLSTrafficQuota' .
```

Expected: FAIL because the tests are not implemented yet.

- [ ] **Step 3: Implement `naive`, `shadowtls`, and `anytls` tests**

Use the existing inbound baselines and keep each fixture focused:

```go
func TestNaiveTrafficQuota(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	fixture := quotaProtocolFixture{
		name: "naive",
		userProviderUser: option.User{Name: "testuser", Password: "password"},
		serverInbound: option.Inbound{
			Type: C.TypeNaive,
			Tag:  "naive-in",
			Options: &option.NaiveInboundOptions{
				ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
					ListenPort: tqServerPort,
				},
				Users: []auth.User{{Username: "placeholder", Password: "password"}},
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
		clientOutbound: option.Outbound{
			Type: C.TypeNaive,
			Tag:  "naive-out",
			Options: &option.NaiveOutboundOptions{
				ServerOptions: option.ServerOptions{
					Server:     "127.0.0.1",
					ServerPort: tqServerPort,
				},
				Username: "testuser",
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
	runTrafficQuotaProtocolTest(t, fixture, quotaScenario{quotaBytes: 1 * 1024 * 1024, initialBytes: 256 * 1024, overflowBytes: 2 * 1024 * 1024})
}
```

For `shadowtls`, follow the authenticated-user detour model from `test/shadowtls_test.go`. For `anytls`, create the first dedicated `test/` baseline using `option.AnyTLSInboundOptions`, `option.AnyTLSOutboundOptions`, TLS certificates, and `user-provider`-supplied password users.

- [ ] **Step 4: Run the special-transport tests**

Run:

```bash
cd test && env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go test -run 'TestNaiveTrafficQuota|TestShadowTLSTrafficQuota|TestAnyTLSTrafficQuota' .
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add test/traffic_quota_special_inbounds_test.go
git commit -m "test: add traffic quota coverage for special user inbounds"
```

## Task 7: Run Full Matrix Verification and Tighten Assertions

**Files:**
- Modify: `test/traffic_quota_helper_test.go`
- Modify: `test/traffic_quota_test.go`
- Modify: `test/traffic_quota_tcp_inbounds_test.go`
- Modify: `test/traffic_quota_v2ray_inbounds_test.go`
- Modify: `test/traffic_quota_shadowsocks_test.go`
- Modify: `test/traffic_quota_quic_inbounds_test.go`
- Modify: `test/traffic_quota_special_inbounds_test.go`

- [ ] **Step 1: Run the full new matrix**

Run:

```bash
cd test && env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go test -run 'TrafficQuota' .
```

Expected: PASS

- [ ] **Step 2: Tighten any flaky assertions exposed by the full run**

If reconnect failures surface differently by protocol, normalize only in the shared assertion helper:

```go
func assertReconnectBlocked(t *testing.T, dialer *socks.Client, testPort uint16) {
	t.Helper()

	buffer := make([]byte, 32*1024)
	secondConn, err := dialer.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort("127.0.0.1", testPort))
	if err != nil {
		return
	}
	defer secondConn.Close()

	_ = secondConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, readErr := secondConn.Read(buffer)
	require.True(t, readErr != nil || n == 0)
}
```

- [ ] **Step 3: Run the existing quota tests plus the new matrix**

Run:

```bash
cd test && env GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go-mod-cache go test -run 'TrafficQuota|SpeedLimiter' .
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add test/traffic_quota_helper_test.go test/traffic_quota_test.go test/traffic_quota_tcp_inbounds_test.go test/traffic_quota_v2ray_inbounds_test.go test/traffic_quota_shadowsocks_test.go test/traffic_quota_quic_inbounds_test.go test/traffic_quota_special_inbounds_test.go
git commit -m "test: finalize inbound traffic quota protocol matrix"
```

## Self-Review

### Spec coverage

- Covered protocol matrix structure: yes, via grouped protocol files and shared helper
- Covered Wave 1 and Wave 2 rollout: yes, Tasks 3-6
- Covered representative `10 MB` test: yes, Task 2
- Covered under-quota, over-quota, and reconnect rejection: yes, Tasks 1-7
- Persistence and real-time expiry remain out of scope as required

### Placeholder scan

- No deferred-content markers remain in the plan
- Commands and file paths are concrete
- Each code-writing step includes code to add or adapt

### Type consistency

- Shared helper names remain consistent:
  - `quotaProtocolFixture`
  - `quotaScenario`
  - `runTrafficQuotaProtocolTest`
  - `startTrafficQuotaMatrixInstance`
  - `assertQuotaUnderLimit`
  - `assertQuotaExceededAndReconnectBlocked`
