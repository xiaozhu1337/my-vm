//go:build windows

package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T, count int) (*Service, *Client) {
	t.Helper()
	s := NewService(t.TempDir(), nil)
	cfg := Config{}
	for i := range count {
		m := NewMachine()
		m.Name = fmt.Sprintf("VM %d", i)
		cfg.Machines = append(cfg.Machines, m)
	}
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	client := NewClient(&http.Client{Transport: localTransport(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		s.ServeHTTP(recorder, r)
		return recorder.Result(), nil
	})})
	return s, client
}

type localTransport func(*http.Request) (*http.Response, error)

func (f localTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicClientConfigurationAndPagination(t *testing.T) {
	_, client := fixture(t, 105)
	ctx := context.Background()
	cfg, err := client.Configuration(ctx)
	if err != nil || len(cfg.Machines) != 105 {
		t.Fatal(len(cfg.Machines), err)
	}
	if err = client.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := client.Execute(ctx, Request{Action: "list", Offset: 104, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	var result Page[Item]
	if err = json.Unmarshal(data, &result); err != nil || result.Total != 105 || len(result.Items) != 1 || result.Items[0].Name != "VM 104" {
		t.Fatal(string(data), err)
	}
	if bytes.Contains(data, []byte("driver_iso")) || bytes.Contains(data, []byte("shares")) {
		t.Fatal("list exposed detailed configuration")
	}
	if client.Active() || client.State(cfg.Machines[0].ID) != "已关机" {
		t.Fatal("read changed runtime state")
	}
	if got := page([]int{1, 2, 3}, -1, 999); len(got.Items) != 3 {
		t.Fatal(got)
	}
	if got := page([]int{}, 999, 1); got.Items == nil {
		t.Fatal("empty lists must encode as []")
	}
}

func TestConcurrentConfigurationWritesMergeAndRejectStaleDraft(t *testing.T) {
	s, client := fixture(t, 3)
	cfg, err := client.Configuration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	originals := append([]Machine{}, cfg.Machines...)
	var workers sync.WaitGroup
	failures := make(chan error, 3)
	for i, m := range cfg.Machines {
		workers.Add(1)
		go func() {
			defer workers.Done()
			m.Name = fmt.Sprintf("Changed %d", i)
			_, err := client.Execute(context.Background(), Request{Action: "update", VM: m.ID, Machine: &m, Expected: &originals[i]})
			failures <- err
		}()
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	saved, err := s.config()
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range saved.Machines {
		if m.Name != fmt.Sprintf("Changed %d", i) {
			t.Fatal("lost concurrent edit", saved)
		}
	}
	stale := originals[0]
	stale.Name = "Stale write"
	if _, err = client.Execute(context.Background(), Request{Action: "update", VM: stale.ID, Machine: &stale, Expected: &originals[0]}); err == nil {
		t.Fatal("stale editor overwrote newer config")
	}
	if _, err = client.Execute(context.Background(), Request{Action: "remove", VM: originals[1].ID}); err != nil {
		t.Fatal(err)
	}
	current := saved.Machines[0]
	current.MemoryMiB += 256
	if _, err = client.Execute(context.Background(), Request{Action: "update", VM: current.ID, Machine: &current}); err != nil {
		t.Fatal(err)
	}
	saved, err = s.config()
	if err != nil || len(saved.Machines) != 2 {
		t.Fatal("removed VM resurrected", saved, err)
	}
}

func TestServiceLocksDoNotBlockOtherMachinesOrStatus(t *testing.T) {
	s, client := fixture(t, 2)
	cfg, _ := s.config()
	first, second := cfg.Machines[0], cfg.Machines[1]
	release, err := s.reserve(context.Background(), &Request{Action: "clone", VM: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	report, finish := s.track(first.ID, "clone", second.ID)
	defer finish()
	report(4, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err = client.Execute(ctx, Request{Action: "list"}); err != nil {
		t.Fatal("long job blocked state", err)
	}
	if _, err = client.Execute(ctx, Request{Action: "memory-set", VM: second.ID, Size: 4096}); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unrelated VM was blocked", err)
	}
	if _, err = client.Execute(ctx, Request{Action: "remove", VM: first.ID}); err == nil {
		t.Fatal("same VM bypassed lock")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.reserve(canceled, &Request{Action: "remove", VM: first.ID}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = client.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	job := client.Progress(first.ID)
	if job == nil || job.Completed != 4 || job.Total != 10 {
		t.Fatal("lost task progress", job)
	}
}

func TestManagementSharePathsRoundTripAndVersionCheck(t *testing.T) {
	s, client := fixture(t, 1)
	cfg, _ := s.config()
	machine := cfg.Machines[0]
	machine.Shares = []Share{{Name: "Work", Path: t.TempDir(), ReadOnly: true}}
	if _, err := client.Execute(context.Background(), Request{Action: "update", VM: machine.ID, Machine: &machine}); err != nil {
		t.Fatal(err)
	}
	cfg, err := client.Configuration(context.Background())
	if err != nil || len(cfg.Machines[0].Shares) != 1 || cfg.Machines[0].Shares[0].Path != machine.Shares[0].Path {
		t.Fatal("management transport lost share path", cfg, err)
	}
	original := cfg.Machines[0]
	machine.Shares = []Share{{Name: "Work", Path: t.TempDir(), ReadOnly: false}}
	if _, err = client.Execute(context.Background(), Request{Action: "update", VM: machine.ID, Machine: &machine, Expected: &original}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Execute(context.Background(), Request{Action: "update", VM: machine.ID, Machine: &original, Expected: &original}); err == nil {
		t.Fatal("share path change escaped optimistic concurrency check")
	}
}

func TestReservationBindsNamesToMachineIdentity(t *testing.T) {
	s, _ := fixture(t, 2)
	cfg, _ := s.config()
	first, second := cfg.Machines[0], cfg.Machines[1]
	request := Request{Action: "remove", VM: first.Name}
	release, err := s.reserve(context.Background(), &request)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.change(func(c *Config) error {
		c.Machines[0].Name = "Renamed"
		c.Machines[1].Name = first.Name
		return nil
	}); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	if _, err = s.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.config()
	if len(cfg.Machines) != 1 || cfg.Machines[0].ID != second.ID {
		t.Fatal("name was resolved again after reservation", cfg)
	}
	// A removed ID must never fall back to an identically named machine.
	if err = s.change(func(c *Config) error { c.Machines[0].Name = first.ID; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(context.Background(), request); err == nil {
		t.Fatal("removed identity retargeted another machine")
	}
}

type reservationContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *reservationContext) Err() error {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Err()
}

func TestReservationBindsBeforeWaitingForGlobalGate(t *testing.T) {
	s, _ := fixture(t, 2)
	cfg, _ := s.config()
	first, second := cfg.Machines[0], cfg.Machines[1]
	request := Request{Action: "remove", VM: first.Name}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	watched := &reservationContext{Context: ctx, waiting: make(chan struct{})}
	s.gate.Lock()
	gateHeld := true
	defer func() {
		if gateHeld {
			s.gate.Unlock()
		}
	}()
	type reservation struct {
		release func()
		err     error
	}
	result := make(chan reservation, 1)
	go func() {
		release, err := s.reserve(watched, &request)
		result <- reservation{release, err}
	}()
	select {
	case <-watched.waiting:
	case <-ctx.Done():
		t.Fatal("reservation did not reach global gate")
	}
	if err := s.change(func(c *Config) error {
		c.Machines[0].Name = "Renamed"
		c.Machines[1].Name = first.Name
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.gate.Unlock()
	gateHeld = false
	reserved := <-result
	if reserved.err != nil {
		t.Fatal(reserved.err)
	}
	reserved.release()
	if _, err := s.Execute(ctx, request); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.config()
	if len(cfg.Machines) != 1 || cfg.Machines[0].ID != second.ID {
		t.Fatal("global gate wait changed target identity", cfg)
	}
}

func TestRegistrationKeepsLatestConfigAndReportsRecoveryPath(t *testing.T) {
	s, _ := fixture(t, 1)
	cfg, _ := s.config()
	old := cfg.Machines[0]
	if err := s.change(func(c *Config) error { c.Machines[0].Name = "Latest"; return nil }); err != nil {
		t.Fatal(err)
	}
	target := NewClone(old, cfg.Machines)
	if err := s.register(target); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.config()
	if len(cfg.Machines) != 2 || cfg.Machines[0].Name != "Latest" {
		t.Fatal("registration lost latest edits", cfg)
	}
	if err := s.register(target); err == nil || !strings.Contains(err.Error(), DiskPath(s.runtime.Root, target)) {
		t.Fatal("missing retained disk recovery path", err)
	}
	if err := s.change(func(c *Config) error { c.Machines[0].Name = target.Name; return nil }); err == nil {
		t.Fatal("duplicate names accepted")
	}
	cfg, _ = s.config()
	if cfg.Machines[0].Name != "Latest" {
		t.Fatal("failed save published mutation")
	}
}

func TestRemovalPreservesDiskAndEnhancementsUseSharedWorkflow(t *testing.T) {
	s, client := fixture(t, 2)
	cfg, _ := s.config()
	machine := cfg.Machines[0]
	disk := DiskPath(s.runtime.Root, machine)
	if err := os.MkdirAll(filepath.Dir(disk), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk, []byte("retained disk"), 0600); err != nil {
		t.Fatal(err)
	}
	s.mediaDir = t.TempDir()
	image := filepath.Join(s.mediaDir, "my-vm-guest-tools-windows.iso")
	if err := os.WriteFile(image, []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Execute(context.Background(), Request{Action: "enhancements", VM: machine.ID}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.config()
	if cfg.Machines[0].DriverISO != image {
		t.Fatal("disc not saved", cfg)
	}
	data, err := client.Execute(context.Background(), Request{Action: "remove", VM: machine.ID})
	if err != nil || !bytes.Contains(data, []byte("retained_disk")) {
		t.Fatal(string(data), err)
	}
	if data, err = os.ReadFile(disk); err != nil || string(data) != "retained disk" {
		t.Fatal("remove deleted disk", err)
	}
	if _, err = client.Execute(context.Background(), Request{Action: "enhancements", VM: cfg.Machines[1].ID}); err != nil {
		t.Fatal("disc reused across VMs", err)
	}
}
