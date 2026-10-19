//go:build windows

package console

import (
	"errors"
	"testing"
	"time"
)

func TestDisplayUpdatesOnlyAfterStableSizeAndConnection(t *testing.T) {
	var state displayUpdate
	now := time.Now()
	settings := displaySettings{1280, 720, 96}
	calls := 0
	send := func(got displaySettings) error { calls++; return nil }
	state.update(false, settings, now, send)
	state.update(true, settings, now, send)
	if calls != 0 {
		t.Fatal("display sent before connection or while resizing")
	}
	for i := 1; i <= 30; i++ {
		state.update(true, settings, now.Add(time.Duration(i)*300*time.Millisecond), send)
	}
	if calls != 1 {
		t.Fatalf("stable desktop updated %d times", calls)
	}
	settings.dpi = 144
	state.update(true, settings, now.Add(10*time.Second), send)
	state.update(true, settings, now.Add(11*time.Second), send)
	if calls != 2 {
		t.Fatal("DPI-only change was missed")
	}
	state.update(false, settings, now.Add(12*time.Second), send)
	state.update(true, settings, now.Add(13*time.Second), send)
	state.update(true, settings, now.Add(14*time.Second), send)
	if calls != 3 {
		t.Fatal("unchanged dimensions were not sent on a new connection")
	}
}

func TestDisplayResizeDebounceAndFailedUpdateRetry(t *testing.T) {
	var state displayUpdate
	now := time.Now()
	settings := displaySettings{1280, 720, 96}
	calls := 0
	failure := errors.New("display not ready")
	send := func(got displaySettings) error { calls++; return failure }
	for i := range 10 {
		settings.width++
		state.update(true, settings, now.Add(time.Duration(i)*30*time.Millisecond), send)
	}
	if calls != 0 {
		t.Fatal("interactive resizing flooded the display channel")
	}
	_, err := state.update(true, settings, now.Add(time.Second), send)
	if err != failure || state.applied == settings {
		t.Fatal("failed update was marked successful")
	}
	state.update(true, settings, now.Add(1500*time.Millisecond), send)
	if calls != 1 {
		t.Fatal("failed update retried without backoff")
	}
	failure = nil
	state.update(true, settings, now.Add(4*time.Second), send)
	state.update(true, settings, now.Add(5*time.Second), send)
	if calls != 2 || state.applied != settings {
		t.Fatal("failed update did not recover or stable size was sent again")
	}
}
