package guestagent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"my-vm/internal/guestops"
)

func TestStatusPublisherChangesHeartbeatRetryAndCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var state atomic.Int32
	published := make(chan string, 12)
	done := make(chan struct{})
	calls := 0
	go func() {
		defer close(done)
		monitorStatus(ctx, io.Discard, func(context.Context) guestops.Status {
			status := guestops.Status{Protocol: 1, OS: "windows", Desktop: guestops.DesktopStatus{Mode: "windows", State: "waiting"}}
			if state.Load() == 1 {
				status.Desktop.State = "ready"
			}
			return status
		}, func(_ context.Context, status guestops.Status) error {
			calls++
			if calls == 1 {
				return errors.New("temporarily offline")
			}
			published <- status.Desktop.State
			return nil
		}, 5*time.Millisecond, 100*time.Millisecond)
	}()
	next := func(want string) {
		select {
		case got := <-published:
			if got != want {
				t.Fatalf("got %s want %s", got, want)
			}
		case <-ctx.Done():
			t.Fatal("publisher stalled")
		}
	}
	next("waiting")
	// No unchanged upload before its heartbeat is due.
	select {
	case <-published:
		t.Fatal("duplicate upload before heartbeat")
	case <-time.After(20 * time.Millisecond):
	}
	state.Store(1)
	next("ready")
	next("ready")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher did not cancel")
	}
}

func TestStatusPublisherHTTPContract(t *testing.T) {
	code := http.StatusNoContent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("wrong status request")
		}
		w.WriteHeader(code)
	}))
	defer server.Close()
	status := guestops.Status{Protocol: 1, OS: "windows", Desktop: guestops.DesktopStatus{Mode: "windows", State: "available"}}
	if err := publishStatus(context.Background(), server.Client(), server.URL, status); err != nil {
		t.Fatal(err)
	}
	code = 400
	if err := publishStatus(context.Background(), server.Client(), server.URL, status); err == nil {
		t.Fatal("host rejection was ignored")
	}
	status.Protocol = 900
	if err := publishStatus(context.Background(), server.Client(), server.URL, status); err == nil {
		t.Fatal("invalid status published")
	}
}
