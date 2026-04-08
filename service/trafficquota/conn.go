package trafficquota

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var ErrQuotaExceeded = errors.New("traffic quota exceeded")

type QuotaConn struct {
	net.Conn
	closed    atomic.Bool
	closeOnce sync.Once
	onBytes   func(n int)
	onClose   func()
}

func NewQuotaConn(conn net.Conn, onBytes func(n int), onClose func()) *QuotaConn {
	return &QuotaConn{
		Conn:    conn,
		onBytes: onBytes,
		onClose: onClose,
	}
}

func (c *QuotaConn) Read(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, ErrQuotaExceeded
	}
	n, err := c.Conn.Read(p)
	if n > 0 && c.onBytes != nil {
		c.onBytes(n)
	}
	return n, err
}

func (c *QuotaConn) Write(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, ErrQuotaExceeded
	}
	n, err := c.Conn.Write(p)
	if n > 0 && c.onBytes != nil {
		c.onBytes(n)
	}
	return n, err
}

func (c *QuotaConn) Close() error {
	c.closeOnce.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return c.Conn.Close()
}

func (c *QuotaConn) markQuotaExceeded() {
	c.closed.Store(true)
}

type QuotaPacketConn struct {
	N.PacketConn
	closed    atomic.Bool
	closeOnce sync.Once
	onBytes   func(n int)
	onClose   func()
}

func NewQuotaPacketConn(conn N.PacketConn, onBytes func(n int), onClose func()) *QuotaPacketConn {
	return &QuotaPacketConn{
		PacketConn: conn,
		onBytes:    onBytes,
		onClose:    onClose,
	}
}

func (c *QuotaPacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	if c.closed.Load() {
		return M.Socksaddr{}, ErrQuotaExceeded
	}
	destination, err := c.PacketConn.ReadPacket(buffer)
	if err == nil && buffer.Len() > 0 && c.onBytes != nil {
		c.onBytes(buffer.Len())
	}
	return destination, err
}

func (c *QuotaPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	if c.closed.Load() {
		return ErrQuotaExceeded
	}
	err := c.PacketConn.WritePacket(buffer, destination)
	if err == nil && buffer.Len() > 0 && c.onBytes != nil {
		c.onBytes(buffer.Len())
	}
	return err
}

func (c *QuotaPacketConn) Close() error {
	c.closeOnce.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return c.PacketConn.Close()
}

func (c *QuotaPacketConn) markQuotaExceeded() {
	c.closed.Store(true)
}

// FrontHeadroom and RearHeadroom forward the underlying conn's headroom requirements
// so bufio.CopyPacketWithPool allocates buffers with sufficient header space.
// QuotaPacketConn must NOT implement Upstream() — that would let sing's bufio unwrap
// this conn and bypass quota tracking — so we forward headroom explicitly instead.
func (c *QuotaPacketConn) FrontHeadroom() int {
	return N.CalculateFrontHeadroom(c.PacketConn)
}

func (c *QuotaPacketConn) RearHeadroom() int {
	return N.CalculateRearHeadroom(c.PacketConn)
}
