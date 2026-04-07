package trafficquota

import (
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func TestQuotaConnReadCountsBytes(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	var counted atomic.Int64
	conn := NewQuotaConn(client, func(n int) {
		counted.Add(int64(n))
	}, nil)

	go func() {
		_, _ = server.Write([]byte("hello"))
	}()

	buffer := make([]byte, 16)
	n, err := conn.Read(buffer)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(buffer[:n]) != "hello" {
		t.Fatalf("unexpected read payload: %q", buffer[:n])
	}
	if counted.Load() != int64(len("hello")) {
		t.Fatalf("unexpected counted bytes: %d", counted.Load())
	}
}

func TestQuotaConnWriteCountsBytes(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	var counted atomic.Int64
	conn := NewQuotaConn(client, func(n int) {
		counted.Add(int64(n))
	}, nil)

	go func() {
		_, _ = io.Copy(io.Discard, server)
	}()

	n, err := conn.Write([]byte("payload"))
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if n != len("payload") {
		t.Fatalf("unexpected write count: %d", n)
	}
	if counted.Load() != int64(len("payload")) {
		t.Fatalf("unexpected counted bytes: %d", counted.Load())
	}
}

func TestQuotaConnClosedReturnsQuotaError(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	conn := NewQuotaConn(client, nil, nil)
	conn.closed.Store(true)

	buffer := make([]byte, 8)
	if _, err := conn.Read(buffer); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected quota exceeded on read, got: %v", err)
	}
	if _, err := conn.Write([]byte("x")); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected quota exceeded on write, got: %v", err)
	}
}

func TestQuotaConnCloseCallsOnCloseOnce(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()

	var closed atomic.Int64
	conn := NewQuotaConn(client, nil, func() {
		closed.Add(1)
	})

	if err := conn.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if err := conn.Close(); err == nil {
		// net.Pipe returns an error on the second close; the callback should still be once-only.
	}
	if closed.Load() != 1 {
		t.Fatalf("unexpected close callback count: %d", closed.Load())
	}
}

func TestQuotaPacketConnReadCountsBytes(t *testing.T) {
	inner := &stubPacketConn{
		readPayload: []byte("packet"),
		readAddr:    M.ParseSocksaddrHostPort("127.0.0.1", 9000),
	}
	var counted atomic.Int64
	conn := NewQuotaPacketConn(inner, func(n int) {
		counted.Add(int64(n))
	}, nil)

	buffer := buf.NewPacket()
	defer buffer.Release()

	destination, err := conn.ReadPacket(buffer)
	if err != nil {
		t.Fatalf("read packet failed: %v", err)
	}
	if destination != inner.readAddr {
		t.Fatalf("unexpected packet destination: %v", destination)
	}
	if string(buffer.Bytes()) != "packet" {
		t.Fatalf("unexpected packet payload: %q", buffer.Bytes())
	}
	if counted.Load() != int64(len("packet")) {
		t.Fatalf("unexpected counted bytes: %d", counted.Load())
	}
}

func TestQuotaPacketConnWriteCountsBytes(t *testing.T) {
	inner := &stubPacketConn{}
	var counted atomic.Int64
	conn := NewQuotaPacketConn(inner, func(n int) {
		counted.Add(int64(n))
	}, nil)

	buffer := buf.As([]byte("reply"))
	destination := M.ParseSocksaddrHostPort("127.0.0.1", 9001)
	if err := conn.WritePacket(buffer, destination); err != nil {
		t.Fatalf("write packet failed: %v", err)
	}
	if inner.writeDestination != destination {
		t.Fatalf("unexpected write destination: %v", inner.writeDestination)
	}
	if string(inner.writePayload) != "reply" {
		t.Fatalf("unexpected write payload: %q", inner.writePayload)
	}
	if counted.Load() != int64(len("reply")) {
		t.Fatalf("unexpected counted bytes: %d", counted.Load())
	}
}

func TestQuotaPacketConnClosedReturnsQuotaError(t *testing.T) {
	conn := NewQuotaPacketConn(&stubPacketConn{}, nil, nil)
	conn.closed.Store(true)

	buffer := buf.NewPacket()
	defer buffer.Release()

	if _, err := conn.ReadPacket(buffer); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected quota exceeded on packet read, got: %v", err)
	}
	if err := conn.WritePacket(buf.As([]byte("x")), M.ParseSocksaddrHostPort("127.0.0.1", 9002)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected quota exceeded on packet write, got: %v", err)
	}
}

func TestQuotaPacketConnCloseCallsOnCloseOnce(t *testing.T) {
	inner := &stubPacketConn{}
	var closed atomic.Int64
	conn := NewQuotaPacketConn(inner, nil, func() {
		closed.Add(1)
	})

	if err := conn.Close(); err != nil {
		t.Fatalf("close packet conn failed: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second close packet conn failed: %v", err)
	}
	if closed.Load() != 1 {
		t.Fatalf("unexpected close callback count: %d", closed.Load())
	}
}

type stubPacketConn struct {
	readPayload      []byte
	readAddr         M.Socksaddr
	readErr          error
	writePayload     []byte
	writeDestination M.Socksaddr
	writeErr         error
	closed           atomic.Bool
}

var _ N.PacketConn = (*stubPacketConn)(nil)

func (c *stubPacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	if c.readErr != nil {
		return M.Socksaddr{}, c.readErr
	}
	if len(c.readPayload) > 0 {
		if _, err := buffer.Write(c.readPayload); err != nil {
			return M.Socksaddr{}, err
		}
	}
	return c.readAddr, nil
}

func (c *stubPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	c.writePayload = append([]byte(nil), buffer.Bytes()...)
	c.writeDestination = destination
	return nil
}

func (c *stubPacketConn) Close() error {
	c.closed.Store(true)
	return nil
}

func (c *stubPacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{}
}

func (c *stubPacketConn) SetDeadline(time.Time) error {
	return nil
}

func (c *stubPacketConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *stubPacketConn) SetWriteDeadline(time.Time) error {
	return nil
}
