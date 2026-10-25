//go:build windows

package console

import (
	"context"
	"fmt"
	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"golang.org/x/sys/windows"
	"io"
	"my-vm/internal/hvsock"
	"net"
)

// Linux xrdp speaks RDP on vsock:3389, rather than Windows' enhanced-video pipe.
func linuxBridge(pipe, runtimeID string, log io.Writer) (func(), error) {
	id, err := guid.FromString(runtimeID)
	if err != nil {
		return nil, err
	}
	var token windows.Token
	if err = windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	listener, err := winio.ListenPipe(pipe, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;" + user.User.Sid.String() + ")", InputBufferSize: 65536, OutputBufferSize: 65536})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		incoming, err := listener.Accept()
		_ = listener.Close()
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintln(log, err)
			}
			return
		}
		defer incoming.Close()
		closeIncoming := context.AfterFunc(ctx, func() { _ = incoming.Close() })
		defer closeIncoming()
		outgoing, err := hvsock.Dial(ctx, &winio.HvsockAddr{VMID: id, ServiceID: winio.VsockServiceID(3389)})
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintln(log, "连接 Linux xrdp/vsock:", err)
			}
			return
		}
		defer outgoing.Close()
		closeOutgoing := context.AfterFunc(ctx, func() { _ = outgoing.Close() })
		defer closeOutgoing()
		relay(incoming, outgoing)
	}()
	return func() { cancel(); _ = listener.Close() }, nil
}
func relay(a, b net.Conn) {
	done := make(chan struct{})
	go func() { _, _ = io.Copy(b, a); _ = a.Close(); _ = b.Close(); close(done) }()
	_, _ = io.Copy(a, b)
	_ = a.Close()
	_ = b.Close()
	<-done
}
