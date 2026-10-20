package sharing

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"my-vm/internal/gueststatus"
)

func testStatus() gueststatus.Status {
	return gueststatus.Status{Protocol: 1, OS: "windows", Desktop: gueststatus.DesktopStatus{Mode: "windows", State: "ready", Reason: "services_running"}}
}

func TestStatusUploadValidationAndExpiration(t *testing.T) {
	feed := NewStatusFeed()
	defer feed.Close()
	data, _ := json.Marshal(testStatus())
	for _, test := range []struct {
		method, body string
		code         int
	}{
		{"POST", string(data), 204}, {"GET", string(data), 405}, {"POST", string(data) + "{}", 400},
		{"POST", `{"protocol":1,"os":"windows","desktop":{"mode":"windows","state":"wrong"}}`, 400},
		{"POST", `{"protocol":1,"os":"windows","desktop":{"mode":"windows","state":"ready"},"vm":"other"}`, 400},
		{"POST", strings.Repeat(" ", 4097) + string(data), 400},
	} {
		r := httptest.NewRecorder()
		feed.handler().ServeHTTP(r, httptest.NewRequest(test.method, "/v1/status", strings.NewReader(test.body)))
		if r.Code != test.code {
			t.Fatalf("%s => %d want %d", test.method, r.Code, test.code)
		}
	}
	fresh, _, _, _ := feed.snapshot(time.Now())
	if fresh.Status == nil || *fresh.Status != testStatus() {
		t.Fatal("valid status missing")
	}
	stale, _, _, _ := feed.snapshot(time.Now().Add(statusLifetime))
	if stale.Status != nil {
		t.Fatal("expired ready remained valid")
	}
	other, _, _, _ := NewStatusFeed().snapshot(time.Now())
	if other.Status != nil {
		t.Fatal("a new VM inherited another VM status")
	}
}

func TestStatusStreamChangesExpiryAndClose(t *testing.T) {
	feed := NewStatusFeed()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events := make(chan gueststatus.StatusEvent, 4)
	done := make(chan error, 1)
	go func() {
		done <- feed.Stream(ctx, func(event gueststatus.StatusEvent) error { events <- event; return nil })
	}()
	next := func() gueststatus.StatusEvent {
		select {
		case event := <-events:
			return event
		case <-ctx.Done():
			t.Fatal("missing stream update")
			return gueststatus.StatusEvent{}
		}
	}
	if next().Status != nil {
		t.Fatal("unreported agent ready")
	}
	feed.publish(testStatus(), time.Now().Add(-statusLifetime+100*time.Millisecond))
	if next().Status == nil {
		t.Fatal("status change not delivered")
	}
	if next().Status != nil {
		t.Fatal("heartbeat timeout not delivered")
	}
	feed.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("close leaked subscriber")
	}
}

func TestSlowStatusSubscriberDoesNotBlockUpload(t *testing.T) {
	feed := NewStatusFeed()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = feed.Stream(ctx, func(gueststatus.StatusEvent) error { close(entered); <-release; return context.Canceled })
	}()
	<-entered
	published := make(chan struct{})
	go func() { feed.publish(testStatus(), time.Now()); close(published) }()
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Error("slow subscriber blocked guest publisher")
	}
	close(release)
	<-done
	feed.Close()
}
