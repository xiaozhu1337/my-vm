//go:build linux

package main

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestDesktopPortFromNegotiatedGNOMEProperty(t *testing.T) {
	for _, data := range []string{"(<3390>,)", "(<int32 3390>,)", "(<uint16 3390>,)\n"} {
		address, err := desktopPort([]byte(data))
		if err != nil || address != "127.0.0.1:3390" {
			t.Fatalf("negotiated port did not override configured 3389: %q, %v", address, err)
		}
	}
	for _, data := range []string{"(<-1>,)", "(<0>,)", "(<65536>,)", "", "(<true>,)", "(<3390>, <3389>)"} {
		if address, err := desktopPort([]byte(data)); err == nil || address != "" {
			t.Fatalf("invalid port was accepted: %q => %q, %v", data, address, err)
		}
	}
	t.Setenv("PATH", t.TempDir())
	if address, err := desktopAddress(context.Background()); err == nil || address != "" {
		t.Fatalf("unavailable GNOME service guessed a port: %q, %v", address, err)
	}
}

func TestDesktopRelayAndCancellation(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, incoming := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() { defer incoming.Close(); done <- connectDesktop(ctx, incoming, listener.Addr().String()) }()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client.SetDeadline(time.Now().Add(time.Second))
	server.SetDeadline(time.Now().Add(time.Second))
	for _, pair := range []struct {
		from, to net.Conn
		data     string
	}{{client, server, "client channel bytes"}, {server, client, "GNOME channel bytes"}} {
		written := make(chan error, 1)
		go func() { _, err := io.WriteString(pair.from, pair.data); written <- err }()
		data := make([]byte, len(pair.data))
		if _, err := io.ReadFull(pair.to, data); err != nil || string(data) != pair.data {
			t.Fatalf("corrupted stream: %q, %v", data, err)
		}
		if err := <-written; err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("guest relay did not close on cancellation")
	}
}
