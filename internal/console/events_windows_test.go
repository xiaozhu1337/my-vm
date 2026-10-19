//go:build windows

package console

import "testing"

func TestSessionEventsRequireLoginBeforeWindowsDisplayUpdates(t *testing.T) {
	v := &viewer{enhanced: true}
	v.sessionEvent(1)
	v.sessionEvent(2)
	if v.loggedIn {
		t.Fatal("connection establishment was mistaken for login completion")
	}
	v.sessionEvent(3)
	if !v.loggedIn {
		t.Fatal("login completion was ignored")
	}
	v.display.applied = displaySettings{1280, 720, 96}
	for _, event := range []int32{4, 17, 34, 1} {
		v.sessionEvent(event)
		if v.loggedIn || v.display.applied != (displaySettings{}) {
			t.Fatal("disconnect or reconnect retained the previous display state", event)
		}
		v.sessionEvent(33)
		if !v.loggedIn || v.display.applied != (displaySettings{}) {
			t.Fatal("reconnection did not invalidate applied display settings")
		}
		v.display.applied = displaySettings{1280, 720, 96}
	}
	v.closed = true
	v.loggedIn = false
	v.sessionEvent(3)
	if v.loggedIn {
		t.Fatal("closed viewer accepted a late event")
	}
}
