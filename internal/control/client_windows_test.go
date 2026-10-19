//go:build windows

package control

import (
	"context"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func TestManagerPipeIdentity(t *testing.T) {
	name, descriptor, err := pipeName()
	if err != nil {
		t.Fatal(err)
	}
	name += "-test-" + rand.Text()
	listener, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: descriptor})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, e := listener.Accept()
		if e == nil {
			defer connection.Close()
			<-time.After(100 * time.Millisecond)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := dialManager(ctx, name)
	if windows.GetCurrentProcessToken().IsElevated() {
		if err != nil {
			t.Fatal(err)
		}
		connection.Close()
	} else if err == nil {
		connection.Close()
		t.Fatal("accepted a non-elevated server")
	}
	<-done
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	if verifyManager(client) == nil {
		t.Fatal("accepted a connection without verifiable identity")
	}
}
