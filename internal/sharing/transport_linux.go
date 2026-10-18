//go:build linux

package sharing

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"time"
)

type vsockConn struct{ *os.File }
type vsockAddress string

func (a vsockAddress) Network() string                 { return "vsock" }
func (a vsockAddress) String() string                  { return string(a) }
func (c vsockConn) LocalAddr() net.Addr                { return vsockAddress("guest") }
func (c vsockConn) RemoteAddr() net.Addr               { return vsockAddress("host:19027") }
func (c vsockConn) SetDeadline(t time.Time) error      { return c.File.SetDeadline(t) }
func (c vsockConn) SetReadDeadline(t time.Time) error  { return c.File.SetReadDeadline(t) }
func (c vsockConn) SetWriteDeadline(t time.Time) error { return c.File.SetWriteDeadline(t) }
func dialHost(ctx context.Context) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			unix.Close(fd)
		}
	}()
	err = unix.Connect(fd, &unix.SockaddrVM{CID: 2, Port: Port})
	if err != nil && !errors.Is(err, unix.EINPROGRESS) {
		return nil, err
	}
	if err != nil {
		for {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
			_, err = unix.Poll(polls, 100)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if polls[0].Revents == 0 {
				continue
			}
			status, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
			if err != nil {
				return nil, err
			}
			if status != 0 {
				return nil, unix.Errno(status)
			}
			break
		}
	}
	file := os.NewFile(uintptr(fd), "my-vm-hvsock")
	if file == nil {
		return nil, errors.New("cannot open Hyper-V Socket")
	}
	failed = false
	return vsockConn{file}, nil
}
