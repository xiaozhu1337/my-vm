//go:build windows

package guestops

import (
	"github.com/Microsoft/go-winio"
	"my-vm/internal/sharing"
	"net"
)

type parentListener struct{ *winio.HvsockListener }

func listen() (net.Listener, error) {
	listener, err := winio.ListenHvsock(&winio.HvsockAddr{VMID: winio.HvsockGUIDWildcard(), ServiceID: winio.VsockServiceID(Port)})
	if err != nil {
		return nil, err
	}
	return parentListener{listener}, nil
}
func (l parentListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.HvsockListener.Accept()
		if err != nil {
			return nil, err
		}
		address, ok := connection.RemoteAddr().(*winio.HvsockAddr)
		if !ok || address.VMID != winio.HvsockGUIDParent() {
			connection.Close()
			continue
		}
		return sharing.SocketConnection(connection), nil
	}
}
