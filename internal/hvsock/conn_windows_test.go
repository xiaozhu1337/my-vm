//go:build windows

package hvsock

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func socketPair(t *testing.T) (*conn, net.Conn) {
	t.Helper()
	startup.Do(func() { var data windows.WSAData; startupError = windows.WSAStartup(0x202, &data) })
	if startupError != nil {
		t.Fatal(startupError)
	}
	listener, err := windows.WSASocket(windows.AF_INET, windows.SOCK_STREAM, windows.IPPROTO_TCP, nil, 0, windows.WSA_FLAG_OVERLAPPED|windows.WSA_FLAG_NO_HANDLE_INHERIT)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.Closesocket(listener)
	if err = windows.Bind(listener, &windows.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err = windows.Listen(listener, 1); err != nil {
		t.Fatal(err)
	}
	local, err := windows.Getsockname(listener)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(local.(*windows.SockaddrInet4).Port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fd, _, e := winsock.NewProc("accept").Call(uintptr(listener), 0, 0)
	if fd == ^uintptr(0) {
		peer.Close()
		t.Fatal(e)
	}
	c, err := newConn(windows.Handle(fd))
	if err != nil {
		windows.Closesocket(windows.Handle(fd))
		peer.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); peer.Close() })
	return c, peer
}

func TestOverlappedSocketIODeadlinesAndClose(t *testing.T) {
	c, peer := socketPair(t)
	if _, err := peer.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 5)
	if _, err := io.ReadFull(c, data); err != nil || string(data) != "hello" {
		t.Fatal(string(data), err)
	}
	if _, err := c.Write([]byte("world")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(peer, data); err != nil || string(data) != "world" {
		t.Fatal(string(data), err)
	}
	result := make(chan error, 1)
	go func() { _, err := c.Read(data); result <- err }()
	if err := c.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("deadline did not interrupt pending read")
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	go func() { _, err := c.Read(data); result <- err }()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not interrupt pending read")
	}
}

func TestOverlappedContextCancellationReapsPendingIO(t *testing.T) {
	c, _ := socketPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	submitted := make(chan struct{})
	result := make(chan error, 1)
	buffer := make([]byte, 8)
	go func() {
		_, err := c.operation(ctx, &c.read, func(o *windows.Overlapped, n *uint32) error {
			b := windows.WSABuf{Len: uint32(len(buffer)), Buf: &buffer[0]}
			var flags uint32
			err := windows.WSARecv(c.fd, &b, 1, n, &flags, o, nil)
			close(submitted)
			return err
		})
		result <- err
	}()
	<-submitted
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled overlapped operation leaked")
	}
	// Reusing the same direction catches cancellation that leaves stale I/O behind.
	if err := c.SetReadDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(buffer); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestExpiredDeadlineDoesNotConsumeQueuedData(t *testing.T) {
	c, peer := socketPair(t)
	if _, err := peer.Write([]byte("queued")); err != nil {
		t.Fatal(err)
	}
	if err := c.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 6)
	if n, err := c.Read(buffer); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expired read consumed queued bytes: n=%d err=%v", n, err)
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(c, buffer); err != nil || string(buffer) != "queued" {
		t.Fatal(string(buffer), err)
	}
	if err := c.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Write(buffer); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expired write submitted bytes: n=%d err=%v", n, err)
	}
}

func TestDialAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Dial(ctx, &winio.HvsockAddr{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
