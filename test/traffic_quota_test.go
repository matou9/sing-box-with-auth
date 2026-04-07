package main

import (
	"context"
	"io"
	"testing"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/protocol/socks"

	"github.com/stretchr/testify/require"
)

const (
	tqServerPort uint16 = 20100 + iota
	tqClientPort
	tqTestPort
)

func TestTrafficQuotaHelperBasicFlow(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")

	fixture := newTrojanQuotaFixture("trojan-basic", certPem, keyPem)

	runTrafficQuotaProtocolTest(t, fixture, quotaScenario{
		quotaBytes:    1 * 1024 * 1024,
		initialBytes:  256 * 1024,
		overflowBytes: 2 * 1024 * 1024,
	})
}

func TestTrafficQuotaDisconnectsAndBlocksNewConnections(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")

	runTrafficQuotaProtocolTest(t, newTrojanQuotaFixture("trojan", certPem, keyPem), quotaScenario{
		quotaBytes:    128 * 1024,
		initialBytes:  0,
		overflowBytes: 1024 * 1024,
	})
}

func TestTrafficQuotaAndSpeedLimiterCoexist(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")

	const (
		srvPort  uint16 = 24110
		cliPort  uint16 = 24111
		testPort uint16 = 24112
		dataSize        = 250 * 1024
	)

	startDataServer(t, testPort, dataSize)
	startTrafficQuotaFixtureInstance(
		t,
		srvPort,
		cliPort,
		newTrojanQuotaFixture("trojan-speed-limiter", certPem, keyPem),
		quotaServiceOptionsForUser("testuser", 2*1024*1024),
		option.Service{
			Type: C.TypeSpeedLimiter,
			Tag:  "speed-limiter",
			Options: &option.SpeedLimiterServiceOptions{
				Users: []option.SpeedLimiterUser{
					{Name: "testuser", UploadMbps: 1, DownloadMbps: 1},
				},
			},
		},
	)

	dialer := socks.NewClient(N.SystemDialer, M.ParseSocksaddrHostPort("127.0.0.1", cliPort), socks.Version5, "", "")
	conn, err := dialer.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort("127.0.0.1", testPort))
	require.NoError(t, err)
	defer conn.Close()

	start := time.Now()
	written, err := io.Copy(io.Discard, conn)
	require.NoError(t, err)
	elapsed := time.Since(start)

	if written != dataSize {
		t.Fatalf("expected full transfer under quota, got %d bytes", written)
	}
	if elapsed < 800*time.Millisecond {
		t.Fatalf("expected speed limiter to slow transfer, elapsed=%v", elapsed)
	}
}

func trafficQuotaGB(bytes int) float64 {
	return float64(bytes) / float64(1<<30)
}
