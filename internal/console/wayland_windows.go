//go:build windows

package console

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
)

func linuxWaylandBridge(runtimeID string, log io.Writer) (int, func(), error) {
	id, err := guid.FromString(runtimeID)
	if err != nil {
		return 0, nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, nil, err
	}
	stop := serveWaylandBridge(listener, func(ctx context.Context) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		return winio.Dial(ctx, &winio.HvsockAddr{VMID: id, ServiceID: winio.VsockServiceID(3390)})
	}, log)
	return listener.Addr().(*net.TCPAddr).Port, stop, nil
}

func serveWaylandBridge(listener net.Listener, dial func(context.Context) (net.Conn, error), log io.Writer) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var workers sync.WaitGroup
	active := make(chan struct{}, 4)
	go func() {
		defer close(done)
		for {
			incoming, err := listener.Accept()
			if err != nil {
				if ctx.Err() == nil {
					fmt.Fprintln(log, "Wayland bridge:", err)
				}
				return
			}
			select {
			case active <- struct{}{}:
				workers.Go(func() {
					defer func() { <-active }()
					defer incoming.Close()
					closeIncoming := context.AfterFunc(ctx, func() { incoming.Close() })
					defer closeIncoming()
					outgoing, err := dial(ctx)
					if err != nil {
						if ctx.Err() == nil {
							fmt.Fprintln(log, "连接 Linux Wayland/vsock:", err)
						}
						return
					}
					defer outgoing.Close()
					closeOutgoing := context.AfterFunc(ctx, func() { outgoing.Close() })
					defer closeOutgoing()
					relay(incoming, outgoing)
				})
			default:
				incoming.Close()
			}
		}
	}()
	return func() {
		cancel()
		listener.Close()
		<-done
		workers.Wait()
	}
}
