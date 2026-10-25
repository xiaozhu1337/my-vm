//go:build windows

package library

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"my-vm/internal/guestops"
	"my-vm/internal/hvsock"
	"my-vm/internal/sharing"
)

func newGuestTransport() *http.Transport {
	return &http.Transport{MaxIdleConns: 16, MaxIdleConnsPerHost: 4, MaxConnsPerHost: 8, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: time.Hour + time.Minute, DisableCompression: true, DialContext: func(ctx context.Context, _, endpoint string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(endpoint)
		if err != nil {
			return nil, err
		}
		id, err := guid.FromString(host)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		connection, err := hvsock.Dial(ctx, &winio.HvsockAddr{VMID: id, ServiceID: winio.VsockServiceID(guestops.Port)})
		if err != nil {
			return nil, err
		}
		return sharing.SocketConnection(connection), nil
	}}
}
