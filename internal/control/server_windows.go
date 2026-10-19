//go:build windows

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"my-vm/internal/guestops"
	"my-vm/internal/sharing"
	"my-vm/internal/vm"
)

type Server struct {
	manager *vm.Manager
	server  *http.Server
	active  atomic.Int64
	Allow   func(string, string) error
	Changed func(string)
}

func (s *Server) Active() bool { return s.active.Load() > 0 }
func (s *Server) Close() error { return s.server.Close() }
func Listen(manager *vm.Manager, allow func(string, string) error, changed func(string)) (*Server, error) {
	name, descriptor, err := pipeName()
	if err != nil {
		return nil, err
	}
	listener, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: descriptor, InputBufferSize: 65536, OutputBufferSize: 65536})
	if err != nil {
		return nil, err
	}
	s := &Server{manager: manager, Allow: allow, Changed: changed}
	s.server = &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(manager.Log, "命令行接口:", err)
		}
	}()
	return s, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.active.Add(1)
	defer s.active.Add(-1)
	if r.URL.Path != "/v1/action" && !strings.HasPrefix(r.URL.Path, "/v1/guest/") {
		http.NotFound(w, r)
		return
	}
	cfg, err := vm.Load(filepath.Join(s.manager.Root, "library.yaml"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if r.URL.Path == "/v1/action" && r.Method == http.MethodGet && r.URL.Query().Get("action") == "list" {
		s.list(w, r, cfg.Machines)
		return
	}
	machine, err := selectMachine(cfg.Machines, r.URL.Query().Get("vm"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	action := r.URL.Query().Get("action")
	if r.URL.Path == "/v1/action" && r.Method == http.MethodGet && action == "state" {
		writeJSON(w, s.manager.State(machine.ID))
		return
	}
	if s.Allow != nil {
		if err = s.Allow(machine.ID, action); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
	}
	if s.Changed != nil {
		defer s.Changed(machine.ID)
	}
	cfg, err = vm.Load(filepath.Join(s.manager.Root, "library.yaml"))
	if err == nil {
		machine, err = selectMachine(cfg.Machines, machine.ID)
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/guest/") {
		s.guest(w, r, machine.ID)
		return
	}
	if r.Method == http.MethodGet && action == "snapshots" {
		snapshots, err := s.manager.Snapshots("", machine)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, snapshots)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	switch action {
	case "start":
		err = s.manager.Start("", machine)
	case "stop":
		err = s.manager.Control(machine.ID, "system_powerdown")
	case "kill":
		err = s.manager.Control(machine.ID, "kill")
	case "pause":
		err = s.manager.Control(machine.ID, "stop")
	case "resume":
		err = s.manager.Control(machine.ID, "cont")
	case "snapshot-create", "snapshot-restore", "snapshot-delete":
		operation := map[string]string{"snapshot-create": "-c", "snapshot-restore": "-a", "snapshot-delete": "-d"}[action]
		err = s.manager.SnapshotContext(r.Context(), machine, operation, r.URL.Query().Get("name"), nil)
	default:
		err = errors.New("unknown management action")
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func selectMachine(machines []vm.Machine, selector string) (vm.Machine, error) {
	for _, machine := range machines {
		if machine.ID == selector {
			return machine, nil
		}
	}
	var found *vm.Machine
	for _, machine := range machines {
		if strings.EqualFold(machine.Name, selector) {
			if found != nil {
				return vm.Machine{}, errors.New("虚拟机名称重复，请使用 ID")
			}
			value := machine
			found = &value
		}
	}
	if found == nil {
		return vm.Machine{}, errors.New("找不到指定虚拟机")
	}
	return *found, nil
}
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}
func (s *Server) list(w http.ResponseWriter, r *http.Request, machines []vm.Machine) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if offset < 0 {
		offset = 0
	}
	if offset > len(machines) {
		offset = len(machines)
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	end := offset + limit
	if end > len(machines) {
		end = len(machines)
	}
	type item struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		State string `json:"state"`
		Guest string `json:"guest"`
	}
	items := make([]item, 0, end-offset)
	for _, machine := range machines[offset:end] {
		items = append(items, item{machine.ID, machine.Name, s.manager.State(machine.ID), machine.GuestName()})
	}
	writeJSON(w, struct {
		Total int    `json:"total"`
		Items []item `json:"items"`
	}{len(machines), items})
}
func (s *Server) guest(w http.ResponseWriter, r *http.Request, id string) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/guest")
	if path != "/exec" && path != "/file" {
		http.NotFound(w, r)
		return
	}
	runtimeID, err := s.manager.GuestRuntime(id)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	vmid, err := guid.FromString(runtimeID)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	transport := &http.Transport{DisableKeepAlives: true, ResponseHeaderTimeout: time.Hour + time.Minute,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
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
		http.Error(w, err.Error(), 400)
		return
	}
	request.ContentLength = r.ContentLength
	response, err := transport.RoundTrip(request)
	if err != nil {
		http.Error(w, "客户机工具未就绪或操作失败: "+err.Error(), 502)
		return
	}
	defer response.Body.Close()
	for _, key := range []string{"Content-Type", "Content-Length"} {
		if value := response.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	io.Copy(w, response.Body)
}
