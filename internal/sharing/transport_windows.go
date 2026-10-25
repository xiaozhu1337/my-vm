//go:build windows

package sharing

import (
	"context"
	"github.com/Microsoft/go-winio"
	"my-vm/internal/hvsock"
	"net"
	"time"
)

func dialHost(ctx context.Context) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	connection, err := hvsock.Dial(ctx, &winio.HvsockAddr{VMID: winio.HvsockGUIDParent(), ServiceID: winio.VsockServiceID(Port)})
	if err != nil {
		return nil, err
	}
	return SocketConnection(connection), nil
}
