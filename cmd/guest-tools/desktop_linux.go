//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Keep the desktop's authenticated RDP stream on the VM-specific Hyper-V socket.
func runDesktop(ctx context.Context, log io.Writer) {
	if os.Getenv("XDG_SESSION_TYPE") != "wayland" {
		return
	}
	if err := serveDesktop(ctx, log); err != nil && ctx.Err() == nil {
		fmt.Fprintln(log, "Wayland desktop:", err)
	}
}

func serveDesktop(ctx context.Context, log io.Writer) error {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err = unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: 3390}); err != nil {
		return err
	}
	if err = unix.Listen(fd, 4); err != nil {
		return err
	}
	active := make(chan struct{}, 4)
	for ctx.Err() == nil {
		polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err = unix.Poll(polls, 100); errors.Is(err, unix.EINTR) {
			continue
		} else if err != nil {
			return err
		}
		if polls[0].Revents == 0 {
			continue
		}
		client, address, err := unix.Accept4(fd, unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		peer, ok := address.(*unix.SockaddrVM)
		if !ok || peer.CID != unix.VMADDR_CID_HOST {
			unix.Close(client)
			continue
		}
		select {
		case active <- struct{}{}:
			incoming := os.NewFile(uintptr(client), "my-vm-wayland")
			go func() {
				defer func() { <-active }()
				defer incoming.Close()
				address, err := desktopAddress(ctx)
				if err == nil {
					err = connectDesktop(ctx, incoming, address)
				}
				if err != nil && ctx.Err() == nil {
					fmt.Fprintln(log, "Wayland RDP:", err)
				}
			}()
		default:
			unix.Close(client)
		}
	}
	return nil
}

func desktopAddress(ctx context.Context) (string, error) {
	kind, err := waylandDesktop(os.Getenv("XDG_CURRENT_DESKTOP"))
	if err != nil {
		return "", err
	}
	if kind == "kde" {
		return kdeAddress(ctx)
	}
	// GNOME publishes the negotiated listener port on the desktop user's bus.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "gdbus", "call", "--session",
		"--dest", "org.gnome.RemoteDesktop.User", "--object-path", "/org/gnome/RemoteDesktop/Rdp/Server",
		"--method", "org.freedesktop.DBus.Properties.Get", "org.gnome.RemoteDesktop.Rdp.Server", "Port").Output()
	if err != nil {
		return "", fmt.Errorf("读取 GNOME 桌面共享端口失败，请在当前 Wayland 会话启用桌面共享: %w", err)
	}
	return desktopPort(data)
}

func desktopPort(data []byte) (string, error) {
	value := strings.Trim(strings.TrimSpace(string(data)), "()<> ,\n\r\t")
	fields := strings.Fields(value)
	if len(fields) == 2 && (fields[0] == "uint16" || fields[0] == "int32" || fields[0] == "uint32") {
		fields = fields[1:]
	}
	if len(fields) == 1 {
		if port, err := strconv.Atoi(fields[0]); err == nil && port > 0 && port <= 65535 {
			return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
		}
	}
	return "", errors.New("GNOME 桌面共享没有有效监听端口，请确认服务已启用且桌面已登录并解锁")
}

func connectDesktop(ctx context.Context, incoming io.ReadWriteCloser, address string) error {
	outgoing, err := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, "tcp4", address)
	if err != nil {
		return err
	}
	defer outgoing.Close()
	stop := context.AfterFunc(ctx, func() { incoming.Close(); outgoing.Close() })
	defer stop()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(outgoing, incoming)
		incoming.Close()
		outgoing.Close()
		close(done)
	}()
	_, err = io.Copy(incoming, outgoing)
	incoming.Close()
	outgoing.Close()
	<-done
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}
