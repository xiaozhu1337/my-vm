//go:build linux

package guestops

import (
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type socketAddress string

func (a socketAddress) Network() string { return "vsock" }
func (a socketAddress) String() string  { return string(a) }

type socketConnection struct{ *os.File }

func (c socketConnection) LocalAddr() net.Addr                { return socketAddress("guest") }
func (c socketConnection) RemoteAddr() net.Addr               { return socketAddress("host") }
func (c socketConnection) SetDeadline(t time.Time) error      { return c.File.SetDeadline(t) }
func (c socketConnection) SetReadDeadline(t time.Time) error  { return c.File.SetReadDeadline(t) }
func (c socketConnection) SetWriteDeadline(t time.Time) error { return c.File.SetWriteDeadline(t) }

type socketListener struct {
	fd     int
	once   sync.Once
	closed chan struct{}
}

func listen() (net.Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err = unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: Port}); err == nil {
		err = unix.Listen(fd, 8)
	}
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &socketListener{fd: fd, closed: make(chan struct{})}, nil
}
func (l *socketListener) Addr() net.Addr { return socketAddress("guest:19028") }
func (l *socketListener) Close() error {
	l.once.Do(func() { close(l.closed); unix.Close(l.fd) })
	return nil
}
func (l *socketListener) Accept() (net.Conn, error) {
	for {
		select {
		case <-l.closed:
			return nil, net.ErrClosed
		default:
		}
		polls := []unix.PollFd{{Fd: int32(l.fd), Events: unix.POLLIN}}
		_, err := unix.Poll(polls, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if polls[0].Revents == 0 {
			continue
		}
		fd, peer, err := unix.Accept4(l.fd, unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, err
		}
		address, ok := peer.(*unix.SockaddrVM)
		if !ok || address.CID != unix.VMADDR_CID_HOST {
			unix.Close(fd)
			continue
		}
		return socketConnection{os.NewFile(uintptr(fd), "my-vm-guest-control")}, nil
	}
}
