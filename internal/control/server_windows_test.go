//go:build windows

package control

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

func TestNamedPipeTransportLifecycle(t *testing.T) {
	name, descriptor, err := pipeName()
	if err != nil {
		t.Fatal(err)
	}
	if name == "" || descriptor == "" {
		t.Fatal("missing per-user pipe identity or access control")
	}
	// Use an isolated pipe so the test never connects to a running application.
	name += fmt.Sprintf("-test-%d", time.Now().UnixNano())
	listener, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: descriptor})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{server: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })}, done: make(chan struct{})}
	go func() { defer close(server.done); _ = server.server.Serve(listener) }()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return winio.DialPipeContext(ctx, name) }}}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://manager/", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal(response.Status)
	}
	if err = server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	case <-ctx.Done():
		t.Fatal("server did not close")
	}
}
