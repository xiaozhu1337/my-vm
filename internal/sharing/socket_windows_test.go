//go:build windows

package sharing

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type deadlineCounter struct {
	net.Conn
	reads atomic.Int32
}

func (c *deadlineCounter) SetReadDeadline(t time.Time) error {
	c.reads.Add(1)
	return c.Conn.SetReadDeadline(t)
}
func TestSocketReadCancellationPreservesConnection(t *testing.T) {
	native, peer := net.Pipe()
	defer peer.Close()
	counter := &deadlineCounter{Conn: native}
	connection := newSocketConn(counter)
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Read(make([]byte, 1)); err == nil {
		t.Fatal("expired read must fail")
	}
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	writes := make(chan error, 1)
	go func() { _, err := io.WriteString(peer, "retained bytes"); writes <- err }()
	data := make([]byte, len("retained bytes"))
	if _, err := io.ReadFull(connection, data); err != nil {
		t.Fatal(err)
	}
	if string(data) != "retained bytes" {
		t.Fatal(string(data))
	}
	if err := <-writes; err != nil {
		t.Fatal(err)
	}
	if counter.reads.Load() != 0 {
		t.Fatal("read-ahead cancellation reached native Hvsock")
	}
}
