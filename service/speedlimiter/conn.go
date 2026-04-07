package speedlimiter

import (
	"context"
	"net"
	"time"

	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"

	"golang.org/x/time/rate"
)

// ThrottledConn wraps a net.Conn with token bucket rate limiting.
// Upload limiter controls Write, download limiter controls Read.
// A nil limiter means no limit in that direction.
type ThrottledConn struct {
	net.Conn
	ctx      context.Context
	upload   *rate.Limiter
	download *rate.Limiter
}

func NewThrottledConn(ctx context.Context, conn net.Conn, upload, download *rate.Limiter) net.Conn {
	if upload == nil && download == nil {
		return conn
	}
	return &ThrottledConn{
		Conn:     conn,
		ctx:      ctx,
		upload:   upload,
		download: download,
	}
}

// Read throttles client upload: data flows client → proxy → destination.
// The outbound reads from this conn to get data the client sent.
func (c *ThrottledConn) Read(p []byte) (n int, err error) {
	n, err = c.Conn.Read(p)
	if n > 0 && c.upload != nil {
		if waitErr := waitN(c.ctx, c.upload, n); waitErr != nil {
			return n, waitErr
		}
	}
	return
}

// Write throttles client download: data flows destination → proxy → client.
// The outbound writes to this conn to send data back to the client.
func (c *ThrottledConn) Write(p []byte) (n int, err error) {
	if len(p) > 0 && c.download != nil {
		if waitErr := waitN(c.ctx, c.download, len(p)); waitErr != nil {
			return 0, waitErr
		}
	}
	return c.Conn.Write(p)
}

// NOTE: Do NOT implement Upstream() — sing's bufio would unwrap this conn
// and bypass the throttled Read/Write.

// ThrottledPacketConn wraps a N.PacketConn with token bucket rate limiting.
type ThrottledPacketConn struct {
	N.PacketConn
	ctx      context.Context
	upload   *rate.Limiter
	download *rate.Limiter
}

func NewThrottledPacketConn(ctx context.Context, conn N.PacketConn, upload, download *rate.Limiter) N.PacketConn {
	if upload == nil && download == nil {
		return conn
	}
	return &ThrottledPacketConn{
		PacketConn: conn,
		ctx:        ctx,
		upload:     upload,
		download:   download,
	}
}

// ReadPacket throttles client upload (UDP).
func (c *ThrottledPacketConn) ReadPacket(buffer *buf.Buffer) (destination M.Socksaddr, err error) {
	destination, err = c.PacketConn.ReadPacket(buffer)
	if err == nil && buffer.Len() > 0 && c.upload != nil {
		if waitErr := waitN(c.ctx, c.upload, buffer.Len()); waitErr != nil {
			return destination, waitErr
		}
	}
	return
}

// WritePacket throttles client download (UDP).
func (c *ThrottledPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	if buffer.Len() > 0 && c.download != nil {
		if waitErr := waitN(c.ctx, c.download, buffer.Len()); waitErr != nil {
			return waitErr
		}
	}
	return c.PacketConn.WritePacket(buffer, destination)
}

// NOTE: Do NOT implement Upstream() — sing's bufio would unwrap this conn
// and bypass the throttled ReadPacket/WritePacket.

// waitN calls limiter.WaitN in chunks no larger than the limiter's burst size.
// This avoids rate.Limiter returning an error when n > burst.
func waitN(ctx context.Context, limiter *rate.Limiter, n int) error {
	burst := limiter.Burst()
	if burst <= 0 {
		burst = 1
	}
	for n > 0 {
		chunk := n
		if chunk > burst {
			chunk = burst
		}
		if err := limiter.WaitN(ctx, chunk); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

// NewLimiter creates a rate.Limiter from Mbps value.
// Returns nil if mbps <= 0 (no limit).
func NewLimiter(mbps int) *rate.Limiter {
	if mbps <= 0 {
		return nil
	}
	bps := float64(mbps) * float64(MbpsToBps)
	burst := int(bps) // 1 second worth of data
	if burst <= 0 {
		burst = 1
	}
	return rate.NewLimiter(rate.Limit(bps), burst)
}

// MbpsToBps converts megabits per second to bytes per second.
const MbpsToBps = 125000

// SetLimiterRate updates an existing limiter's rate from Mbps value.
func SetLimiterRate(limiter *rate.Limiter, mbps int) {
	if limiter == nil {
		return
	}
	bps := float64(mbps) * float64(MbpsToBps)
	burst := int(bps)
	if burst <= 0 {
		burst = 1
	}
	limiter.SetLimit(rate.Limit(bps))
	limiter.SetBurst(burst)
}

// Deadline-aware context helper: if the conn has a read/write deadline,
// ctx cancellation handles it. We rely on the ctx passed from RoutedConnection.
func init() {
	// Ensure compile-time interface satisfaction
	_ = net.Conn(&ThrottledConn{})
	_ = N.PacketConn(&ThrottledPacketConn{})
	_ = time.Time{}
}
