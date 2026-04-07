package speedlimiter

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// Direction convention (from proxy perspective):
//   Read  = reading data FROM client = client UPLOAD  → upload limiter
//   Write = writing data TO client   = client DOWNLOAD → download limiter

func TestThrottledConn_UploadLimit(t *testing.T) {
	// Upload = Read direction. Upload limiter throttles Read.
	limiter := NewLimiter(1) // 1 Mbps
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	throttled := NewThrottledConn(ctx, client, limiter, nil)

	// Write data from server side (simulates client sending data)
	dataSize := 200000
	go func() {
		server.Write(make([]byte, dataSize))
	}()

	// Read from throttled side (proxy reading client upload)
	buf := make([]byte, 32*1024)
	totalRead := 0
	start := time.Now()
	for totalRead < dataSize {
		n, err := throttled.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		totalRead += n
	}
	elapsed := time.Since(start)

	if elapsed < 500*time.Millisecond {
		t.Errorf("upload (Read) too fast: %v < 500ms (read %d bytes)", elapsed, totalRead)
	}
}

func TestThrottledConn_DownloadLimit(t *testing.T) {
	// Download = Write direction. Download limiter throttles Write.
	limiter := NewLimiter(1) // 1 Mbps
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	throttled := NewThrottledConn(ctx, client, nil, limiter)

	// Drain from server side in background
	go io.Copy(io.Discard, server)

	// Write to throttled side (proxy sending data to client)
	data := make([]byte, 200000)
	start := time.Now()
	_, err := throttled.Write(data)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if elapsed < 500*time.Millisecond {
		t.Errorf("download (Write) too fast: %v < 500ms", elapsed)
	}
}

func TestThrottledConn_OnlyUploadLimited(t *testing.T) {
	limiter := NewLimiter(1) // 1 Mbps upload only
	ctx := context.Background()

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	throttled := NewThrottledConn(ctx, client, limiter, nil)

	// Write direction (download) should be unlimited
	go io.Copy(io.Discard, server)

	data := make([]byte, 50000)
	start := time.Now()
	_, err := throttled.Write(data)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("download should be unlimited but took %v", elapsed)
	}
}

func TestThrottledConn_OnlyDownloadLimited(t *testing.T) {
	limiter := NewLimiter(1) // 1 Mbps download only
	ctx := context.Background()

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	throttled := NewThrottledConn(ctx, client, nil, limiter)

	// Read direction (upload) should be unlimited
	dataSize := 50000
	go func() {
		server.Write(make([]byte, dataSize))
	}()

	buf := make([]byte, dataSize)
	start := time.Now()
	totalRead := 0
	for totalRead < dataSize {
		n, err := throttled.Read(buf[totalRead:])
		if err != nil {
			t.Fatal(err)
		}
		totalRead += n
	}
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("upload should be unlimited but took %v", elapsed)
	}
}

func TestThrottledConn_NilLimiters_Passthrough(t *testing.T) {
	ctx := context.Background()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	result := NewThrottledConn(ctx, client, nil, nil)
	if result != client {
		t.Error("expected original conn when both limiters are nil")
	}
}

func TestThrottledConn_ZeroBytes(t *testing.T) {
	limiter := NewLimiter(1)
	ctx := context.Background()

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	throttled := NewThrottledConn(ctx, client, limiter, limiter)

	go io.Copy(io.Discard, server)

	n, err := throttled.Write([]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes written, got %d", n)
	}
}

func TestThrottledConn_ContextCancel(t *testing.T) {
	limiter := NewLimiter(1)
	ctx, cancel := context.WithCancel(context.Background())

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	throttled := NewThrottledConn(ctx, client, nil, limiter) // download limiter on Write

	go io.Copy(io.Discard, server)

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	// Write a lot of data — should be interrupted by context cancel
	data := make([]byte, 10*1024*1024)
	_, err := throttled.Write(data)
	if err == nil {
		t.Error("expected error after context cancel")
	}
}

func TestThrottledConn_InnerConnClosed(t *testing.T) {
	limiter := NewLimiter(100)
	ctx := context.Background()

	server, client := net.Pipe()
	defer server.Close()

	throttled := NewThrottledConn(ctx, client, limiter, nil)

	client.Close()

	buf := make([]byte, 1024)
	_, err := throttled.Read(buf)
	if err == nil {
		t.Error("expected error reading from closed conn")
	}
}

func TestThrottledConn_SharedLimiter_AggregateLimit(t *testing.T) {
	// Two connections sharing the same download limiter (same user)
	limiter := NewLimiter(1) // 1 Mbps shared
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server1, client1 := net.Pipe()
	defer server1.Close()
	defer client1.Close()

	server2, client2 := net.Pipe()
	defer server2.Close()
	defer client2.Close()

	throttled1 := NewThrottledConn(ctx, client1, nil, limiter) // download limiter
	throttled2 := NewThrottledConn(ctx, client2, nil, limiter)

	go io.Copy(io.Discard, server1)
	go io.Copy(io.Discard, server2)

	// Write concurrently (download direction)
	dataPerConn := 100000
	var wg sync.WaitGroup
	start := time.Now()

	wg.Add(2)
	go func() {
		defer wg.Done()
		throttled1.Write(make([]byte, dataPerConn))
	}()
	go func() {
		defer wg.Done()
		throttled2.Write(make([]byte, dataPerConn))
	}()
	wg.Wait()

	elapsed := time.Since(start)

	if elapsed < 400*time.Millisecond {
		t.Errorf("aggregate download too fast: %v < 400ms", elapsed)
	}
}

func TestThrottledConn_LargeBuffer_ChunkedWaitN(t *testing.T) {
	bps := float64(1000000) // ~1MB/s
	limiter := rate.NewLimiter(rate.Limit(bps), 1024) // burst = 1KB

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	throttled := NewThrottledConn(ctx, client, nil, limiter) // download limiter

	go io.Copy(io.Discard, server)

	// Write 64KB — much larger than burst (1KB). Without chunking, WaitN would error.
	data := make([]byte, 64*1024)
	_, err := throttled.Write(data)
	if err != nil {
		t.Fatalf("large buffer write failed (should chunk WaitN): %v", err)
	}
}

func TestWaitN_ChunksCorrectly(t *testing.T) {
	limiter := rate.NewLimiter(rate.Limit(10000), 100)
	ctx := context.Background()

	err := waitN(ctx, limiter, 350)
	if err != nil {
		t.Fatalf("waitN(350) with burst=100 should chunk, got error: %v", err)
	}
}

func TestNewLimiter_ZeroMbps(t *testing.T) {
	l := NewLimiter(0)
	if l != nil {
		t.Error("expected nil limiter for 0 Mbps")
	}

	l = NewLimiter(-1)
	if l != nil {
		t.Error("expected nil limiter for negative Mbps")
	}
}

func TestSetLimiterRate(t *testing.T) {
	l := NewLimiter(10)
	originalLimit := l.Limit()

	SetLimiterRate(l, 20)
	newLimit := l.Limit()

	if newLimit <= originalLimit {
		t.Errorf("expected rate increase: %v -> %v", originalLimit, newLimit)
	}

	expectedRate := rate.Limit(20 * MbpsToBps)
	if newLimit != expectedRate {
		t.Errorf("expected rate %v, got %v", expectedRate, newLimit)
	}
}

func TestSetLimiterRate_NilSafe(t *testing.T) {
	SetLimiterRate(nil, 10)
}

var _ net.Conn = (*ThrottledConn)(nil)
