//go:build windows

package sharing

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
)

type Service struct {
	server     *http.Server
	listener   *winio.HvsockListener
	closeRoots func()
	mu         sync.Mutex
	requests   sync.WaitGroup
	closed     bool
	Seen       atomic.Bool
}

func ServiceGUID() string { return winio.VsockServiceID(Port).String() }
func Start(runtimeID string, shares []Share, log io.Writer) (*Service, error) {
	if log == nil {
		log = io.Discard
	}
	handler, closeRoots, err := Open(shares)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			closeRoots()
		}
	}()
	id, err := guid.FromString(strings.Trim(runtimeID, "{}"))
	if err != nil {
		return nil, err
	}
	if id == (guid.GUID{}) {
		return nil, errors.New("共享文件夹不能绑定全部虚拟机")
	}
	serviceID := winio.VsockServiceID(Port)
	listener, err := winio.ListenHvsock(&winio.HvsockAddr{VMID: id, ServiceID: serviceID})
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(shares)
	})
	mux.Handle("/", handler)
	server := &http.Server{ErrorLog: stdlog.New(log, "Shared folders: ", 0), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Minute, WriteTimeout: 10 * time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32768}
	result := &Service{server: server, listener: listener, closeRoots: closeRoots}
	failed = false
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result.mu.Lock()
		if result.closed {
			result.mu.Unlock()
			http.Error(w, "虚拟机正在关闭", 503)
			return
		}
		result.requests.Add(1)
		result.mu.Unlock()
		defer result.requests.Done()
		result.Seen.Store(true)
		mux.ServeHTTP(w, r)
	})
	go func() {
		if err := server.Serve(socketListener{listener}); err != nil && !errors.Is(err, http.ErrServerClosed) && log != nil {
			fmt.Fprintln(log, "Shared folders:", err)
		}
	}()
	return result, nil
}
func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	err := s.server.Close()
	s.requests.Wait()
	s.closeRoots()
	return err
}
