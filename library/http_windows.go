//go:build windows

package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"my-vm/internal/control"
	"my-vm/internal/guestops"
	"my-vm/internal/sharing"
)

type Server = control.Server

func (s *Service) Listen() (*Server, error) { return control.Listen(s, s.log) }

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
}

// ServeHTTP is a transport adapter over Execute, plus streamed guest I/O.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/guest-status" {
		s.guestStatus(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/guest/") {
		s.guest(w, r)
		return
	}
	if r.URL.Path != "/v1/action" {
		http.NotFound(w, r)
		return
	}
	var input Request
	if r.Method == http.MethodPost && r.ContentLength != 0 {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, err, 400)
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			writeError(w, errors.New("只接受一份 JSON 请求"), 400)
			return
		}
	} else if r.Method == http.MethodGet || r.Method == http.MethodPost {
		input.Action, input.VM, input.Name = r.URL.Query().Get("action"), r.URL.Query().Get("vm"), r.URL.Query().Get("name")
		input.Offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
		input.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
		if r.Method == http.MethodGet {
			switch input.Action {
			case "list", "state", "snapshots", "get", "settings", "networks", "adapters", "config", "memory", "runtime", "status":
			default:
				writeError(w, errors.New("此操作需要 POST"), 405)
				return
			}
		}
	} else {
		writeError(w, errors.New("需要 GET 或 POST"), 405)
		return
	}
	value, err := s.Execute(r.Context(), input)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	writeJSON(w, value)
}

func (s *Service) guestStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, errors.New("需要 GET"), 405)
		return
	}
	feed, err := s.runtime.GuestStatus(r.URL.Query().Get("vm"), r.URL.Query().Get("runtime"))
	if err != nil {
		writeError(w, err, 409)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	controller := http.NewResponseController(w)
	_ = feed.Stream(r.Context(), func(event guestops.StatusEvent) error {
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := json.NewEncoder(w).Encode(event); err != nil {
			return err
		}
		return controller.Flush()
	})
}

func (s *Service) guest(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/guest")
	if path != "/exec" && path != "/file" {
		http.NotFound(w, r)
		return
	}
	if path == "/exec" && r.Method != http.MethodPost || path == "/file" && r.Method != http.MethodGet && r.Method != http.MethodPut {
		writeError(w, errors.New("不支持的客户机请求方法"), 405)
		return
	}
	runtime, err := s.Execute(r.Context(), Request{Action: "runtime", VM: r.URL.Query().Get("vm")})
	if err != nil {
		writeError(w, err, 400)
		return
	}
	vmid, err := guid.FromString(runtime.(string))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	transport := &http.Transport{DisableKeepAlives: true, ResponseHeaderTimeout: time.Hour + time.Minute, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		connection, err := winio.Dial(ctx, &winio.HvsockAddr{VMID: vmid, ServiceID: winio.VsockServiceID(guestops.Port)})
		if err != nil {
			return nil, err
		}
		return sharing.SocketConnection(connection), nil
	}}
	defer transport.CloseIdleConnections()
	destination := &url.URL{Scheme: "http", Host: "guest", Path: path, RawQuery: r.URL.RawQuery}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, destination.String(), r.Body)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	request.ContentLength = r.ContentLength
	response, err := transport.RoundTrip(request)
	if err != nil {
		fmt.Fprintln(s.log, err)
		writeError(w, fmt.Errorf("客户机工具未就绪或操作失败: %w", err), 502)
		return
	}
	defer response.Body.Close()
	for _, key := range []string{"Content-Type", "Content-Length"} {
		if value := response.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if _, err := io.Copy(w, response.Body); err != nil {
		fmt.Fprintln(s.log, "客户机传输:", err)
	}
}
