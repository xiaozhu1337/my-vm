package gnomedisplay

import (
	"context"
	"github.com/godbus/dbus/v5"
	"io"
	"testing"
	"time"
)

func TestRemoteMonitorUsesSignalsWithoutIdlePolling(t *testing.T) {
	f := &fakeDisplay{current: fixture(), applied: make(chan struct{})}
	idle := make(chan struct{})
	f.getState = func(s state) state {
		s.monitors = f.current.monitors
		if f.calls == 2 {
			close(idle)
		}
		return s
	}
	changes := make(chan *dbus.Signal, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := withRemoteMonitor(ctx, f, changes, nil, io.Discard, func(connection context.Context) error {
		select {
		case <-idle:
		case <-connection.Done():
			return connection.Err()
		}
		changes <- &dbus.Signal{Name: "unrelated.Signal"}
		<-time.After(100 * time.Millisecond)
		f.mu.Lock()
		calls := f.calls
		f.current.monitors = append(f.current.monitors, virtual())
		f.mu.Unlock()
		if calls != 2 {
			t.Errorf("idle compositor polled %d times", calls)
		}
		changes <- &dbus.Signal{Name: displayInterface + ".MonitorsChanged", Path: "/org/gnome/Mutter/DisplayConfig"}
		select {
		case <-f.applied:
			return nil
		case <-connection.Done():
			return connection.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.configurations) != 2 {
		t.Fatal("remote monitor was not applied and restored", f.configurations)
	}
}
