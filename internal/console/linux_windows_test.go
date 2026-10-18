//go:build windows

package console

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestRelayBothDirectionsAndDisconnect(t *testing.T) {
	client, left := net.Pipe()
	right, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan struct{})
	go func() { relay(left, right); close(done) }()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	_ = server.SetDeadline(time.Now().Add(time.Second))
	for _, pair := range []struct {
		from, to net.Conn
		data     string
	}{{client, server, "client request"}, {server, client, "server framebuffer"}} {
		write := make(chan error, 1)
		go func() { _, err := io.WriteString(pair.from, pair.data); write <- err }()
		data := make([]byte, len(pair.data))
		if _, err := io.ReadFull(pair.to, data); err != nil {
			t.Fatal(err)
		}
		if string(data) != pair.data {
			t.Fatal(string(data))
		}
		if err := <-write; err != nil {
			t.Fatal(err)
		}
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay leaked after disconnect")
	}
}
