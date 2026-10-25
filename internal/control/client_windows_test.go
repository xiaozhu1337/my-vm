//go:build windows

package control

import (
	"context"
	"crypto/rand"
	"errors"
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

func TestBusyManagerPipeHasIndependentDialTimeout(t *testing.T) {
	name := `\\.\pipe\my-vm-dial-test-` + rand.Text()
	path := windows.StringToUTF16Ptr(name)
	server, err := windows.CreateNamedPipe(path, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED, windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, 1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(server)
	client, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(client)
	started := time.Now()
	connection, err := dialManager(context.Background(), name)
	if connection != nil {
		connection.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 10*time.Second {
		t.Fatalf("busy pipe dial was not bounded: %v after %s", err, time.Since(started))
	}
}
