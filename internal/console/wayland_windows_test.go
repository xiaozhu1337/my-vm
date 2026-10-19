//go:build windows

package console

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestWaylandBridgeReconnectAndCleanup(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := serveWaylandBridge(listener, func(context.Context) (net.Conn, error) {
		left, right := net.Pipe()
		go func() { defer right.Close(); _, _ = io.Copy(right, right) }()
		return left, nil
	}, io.Discard)
	t.Cleanup(stop)
	var last net.Conn
	for range 3 {
		client, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		last = client
		t.Cleanup(func() { client.Close() })
		client.SetDeadline(time.Now().Add(time.Second))
		if _, err = io.WriteString(client, "RDP clipboard and display channels"); err != nil {
			t.Fatal(err)
		}
		data := make([]byte, len("RDP clipboard and display channels"))
		if _, err = io.ReadFull(client, data); err != nil || string(data) != "RDP clipboard and display channels" {
			t.Fatalf("corrupted RDP stream: %q, %v", data, err)
		}
	}
	stop()
	if _, err := last.Read(make([]byte, 1)); err == nil {
		t.Fatal("bridge kept a session alive after close")
	}
	if client, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second); err == nil {
		client.Close()
		t.Fatal("bridge listener survived close")
	}
}

func TestWaylandBridgeCancellationDuringDial(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	stop := serveWaylandBridge(listener, func(ctx context.Context) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, io.Discard)
	t.Cleanup(stop)
	client, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("bridge did not accept a connection")
	}
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge did not cancel its pending VM connection")
	}
}
