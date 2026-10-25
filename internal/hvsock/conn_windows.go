//go:build windows

package hvsock

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"golang.org/x/sys/windows"
)

var winsock = windows.NewLazySystemDLL("ws2_32.dll")
var startup sync.Once
var startupError error

type address struct {
	Family, Reserved uint16
	VMID, ServiceID  guid.GUID
}

type direction struct {
	sync.Mutex
	wake     windows.Handle
	deadline time.Time // guarded by conn.mu
}

type conn struct {
	fd            windows.Handle
	local, remote winio.HvsockAddr
	mu            sync.Mutex
	closed        bool
	active        sync.WaitGroup
	read, write   direction
	done          chan struct{}
}

// Dial owns the overlapped socket so cancellation can stop the pending ConnectEx.
func Dial(ctx context.Context, remote *winio.HvsockAddr) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	startup.Do(func() { var data windows.WSAData; startupError = windows.WSAStartup(0x202, &data) })
	if startupError != nil {
		return nil, startupError
	}
	fd, err := windows.WSASocket(34, windows.SOCK_STREAM, 1, nil, 0, windows.WSA_FLAG_OVERLAPPED|windows.WSA_FLAG_NO_HANDLE_INHERIT)
	if err != nil {
		return nil, err
	}
	c, err := newConn(fd)
	if err != nil {
		windows.Closesocket(fd)
		return nil, err
	}
	c.remote = *remote
	failed := true
	defer func() {
		if failed {
			c.Close()
		}
	}()
	raw := address{Family: 34, VMID: remote.VMID, ServiceID: remote.ServiceID}
	result, _, callErr := winsock.NewProc("bind").Call(uintptr(fd), uintptr(unsafe.Pointer(&raw)), unsafe.Sizeof(raw))
	if int32(result) == -1 {
		return nil, callErr
	}
	var connect uintptr
	var size uint32
	err = windows.WSAIoctl(fd, windows.SIO_GET_EXTENSION_FUNCTION_POINTER, (*byte)(unsafe.Pointer(&windows.WSAID_CONNECTEX)), uint32(unsafe.Sizeof(windows.WSAID_CONNECTEX)), (*byte)(unsafe.Pointer(&connect)), uint32(unsafe.Sizeof(connect)), &size, nil, 0)
	if err != nil {
		return nil, err
	}
	_, err = c.operation(ctx, &c.write, func(o *windows.Overlapped, n *uint32) error {
		ok, _, e := syscall.SyscallN(connect, uintptr(fd), uintptr(unsafe.Pointer(&raw)), unsafe.Sizeof(raw), 0, 0, uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(o)))
		if ok == 0 {
			return e
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err = windows.Setsockopt(fd, windows.SOL_SOCKET, windows.SO_UPDATE_CONNECT_CONTEXT, nil, 0); err != nil {
		return nil, err
	}
	var local address
	size = uint32(unsafe.Sizeof(local))
	result, _, callErr = winsock.NewProc("getsockname").Call(uintptr(fd), uintptr(unsafe.Pointer(&local)), uintptr(unsafe.Pointer(&size)))
	if int32(result) == -1 {
		return nil, callErr
	}
	c.local = winio.HvsockAddr{VMID: local.VMID, ServiceID: local.ServiceID}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	failed = false
	return c, nil
}

func newConn(fd windows.Handle) (*conn, error) {
	c := &conn{fd: fd, done: make(chan struct{})}
	var err error
	c.read.wake, err = windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	c.write.wake, err = windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		windows.CloseHandle(c.read.wake)
		return nil, err
	}
	return c, nil
}

// operation always reaps cancelled I/O before releasing its OVERLAPPED or buffers.
func (c *conn) operation(ctx context.Context, d *direction, submit func(*windows.Overlapped, *uint32) error) (int, error) {
	d.Lock()
	defer d.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, net.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return 0, err
	}
	if !d.deadline.IsZero() && !time.Now().Before(d.deadline) {
		c.mu.Unlock()
		return 0, os.ErrDeadlineExceeded
	}
	c.active.Add(1)
	c.mu.Unlock()
	defer c.active.Done()
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { windows.SetEvent(d.wake); close(callbackDone) })
	defer func() {
		if !stop() {
			<-callbackDone
		}
	}()
	o := windows.Overlapped{HEvent: event}
	var n, flags uint32
	err = submit(&o, &n)
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		return int(n), err
	}
	if err == nil {
		return int(n), nil
	}
	var interrupted error
	for {
		c.mu.Lock()
		closed, deadline := c.closed, d.deadline
		c.mu.Unlock()
		switch {
		case closed:
			interrupted = net.ErrClosed
		case ctx.Err() != nil:
			interrupted = ctx.Err()
		case !deadline.IsZero() && !time.Now().Before(deadline):
			interrupted = os.ErrDeadlineExceeded
		}
		if interrupted != nil {
			break
		}
		wait := uint32(windows.INFINITE)
		if !deadline.IsZero() {
			wait = uint32(min(max(time.Until(deadline).Milliseconds()+1, 1), int64(windows.INFINITE-1)))
		}
		which, waitErr := windows.WaitForMultipleObjects([]windows.Handle{event, d.wake}, false, wait)
		if waitErr != nil {
			interrupted = waitErr
			break
		}
		if which == windows.WAIT_OBJECT_0 {
			break
		}
	}
	if interrupted != nil {
		_ = windows.CancelIoEx(c.fd, &o)
		// Closing or timing out must not free memory still owned by the kernel.
		_, _ = windows.WaitForSingleObject(event, windows.INFINITE)
	}
	err = windows.WSAGetOverlappedResult(c.fd, &o, &n, false, &flags)
	if interrupted != nil {
		err = interrupted
	}
	return int(n), err
}

func (c *conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	buffer := windows.WSABuf{Len: uint32(min(len(p), 1<<30)), Buf: &p[0]}
	n, err := c.operation(context.Background(), &c.read, func(o *windows.Overlapped, n *uint32) error {
		var flags uint32
		return windows.WSARecv(c.fd, &buffer, 1, n, &flags, o, nil)
	})
	runtime.KeepAlive(p)
	if n == 0 && err == nil {
		err = io.EOF
	}
	return n, err
}

func (c *conn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	total := 0
	for total < len(p) {
		buffer := windows.WSABuf{Len: uint32(min(len(p)-total, 1<<30)), Buf: &p[total]}
		n, err := c.operation(context.Background(), &c.write, func(o *windows.Overlapped, n *uint32) error { return windows.WSASend(c.fd, &buffer, 1, n, 0, o, nil) })
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	runtime.KeepAlive(p)
	return total, nil
}

func (c *conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		<-c.done
		return nil
	}
	c.closed = true
	_ = windows.SetEvent(c.read.wake)
	_ = windows.SetEvent(c.write.wake)
	c.mu.Unlock()
	c.active.Wait()
	err := windows.Closesocket(c.fd)
	windows.CloseHandle(c.read.wake)
	windows.CloseHandle(c.write.wake)
	close(c.done)
	return err
}
func (c *conn) LocalAddr() net.Addr                { return &c.local }
func (c *conn) RemoteAddr() net.Addr               { return &c.remote }
func (c *conn) SetReadDeadline(t time.Time) error  { return c.setDeadline(&c.read, t) }
func (c *conn) SetWriteDeadline(t time.Time) error { return c.setDeadline(&c.write, t) }
func (c *conn) SetDeadline(t time.Time) error {
	return errors.Join(c.SetReadDeadline(t), c.SetWriteDeadline(t))
}
func (c *conn) setDeadline(d *direction, t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	d.deadline = t
	return windows.SetEvent(d.wake)
}
