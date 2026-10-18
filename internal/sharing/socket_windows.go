//go:build windows

package sharing

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

type socketListener struct{ net.Listener }

func (l socketListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return newSocketConn(connection), nil
}

type socketConn struct {
	net.Conn
	reader, feed net.Conn
	writeMu      sync.Mutex
	closeOnce    sync.Once
	closeError   error
}

func newSocketConn(connection net.Conn) *socketConn {
	reader, feed := net.Pipe()
	result := &socketConn{Conn: connection, reader: reader, feed: feed}
	// HTTP cancels read-ahead using deadlines; CancelIoEx on Hvsock also lost queued response bytes.
	go func() { _, _ = io.Copy(feed, connection); _ = feed.Close() }()
	return result
}
func (c *socketConn) Read(data []byte) (int, error)      { return c.reader.Read(data) }
func (c *socketConn) SetReadDeadline(t time.Time) error  { return c.reader.SetReadDeadline(t) }
func (c *socketConn) SetWriteDeadline(t time.Time) error { return c.Conn.SetWriteDeadline(t) }
func (c *socketConn) SetDeadline(t time.Time) error {
	return errors.Join(c.SetReadDeadline(t), c.SetWriteDeadline(t))
}
func (c *socketConn) Write(data []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.Write(data)
}
func (c *socketConn) Close() error {
	c.closeOnce.Do(func() { _ = c.reader.Close(); c.closeError = c.Conn.Close(); _ = c.feed.Close() })
	return c.closeError
}
