//go:build windows

package control

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Microsoft/go-winio"
)

// Server only supplies an authenticated local transport; business code is a handler.
type Server struct {
	server *http.Server
	done   chan struct{}
}

func (s *Server) Close() error          { return s.server.Close() }
func (s *Server) Done() <-chan struct{} { return s.done }

func Listen(handler http.Handler, log io.Writer) (*Server, error) {
	name, descriptor, err := pipeName()
	if err != nil {
		return nil, err
	}
	listener, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: descriptor, InputBufferSize: 65536, OutputBufferSize: 65536})
	if err != nil {
		return nil, err
	}
	s := &Server{server: &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && log != nil {
			fmt.Fprintln(log, "管理接口:", err)
		}
	}()
	return s, nil
}
