//go:build windows

package main

import (
	"errors"
	"golang.org/x/sys/windows/svc"
	"my-vm/internal/guestops"
	"testing"
	"time"
)

func TestWindowsDesktopServiceStates(t *testing.T) {
	for _, test := range []struct {
		rdv, terminal svc.State
		state         string
	}{{svc.Running, svc.Running, "ready"}, {svc.Stopped, svc.Running, "available"}, {svc.Stopped, svc.Stopped, "available"}, {svc.StartPending, svc.Running, "waiting"}, {svc.Paused, svc.Running, "waiting"}} {
		desktop := windowsDesktopStatus(test.rdv, test.terminal)
		if desktop.State != test.state {
			t.Fatalf("trigger-start service readiness: %+v", desktop)
		}
		if err := (guestops.Status{Protocol: 1, OS: "windows", Desktop: desktop}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWaitServiceRunningAndFailure(t *testing.T) {
	count := 0
	err := waitService("test", func() (svc.Status, error) {
		count++
		state := svc.StartPending
		if count == 2 {
			state = svc.Running
		}
		return svc.Status{State: state}, nil
	}, time.Second)
	if err != nil || count != 2 {
		t.Fatalf("pending to running: %v", err)
	}
	failure := errors.New("query failed")
	if err = waitService("test", func() (svc.Status, error) { return svc.Status{}, failure }, time.Second); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err = waitService("test", func() (svc.Status, error) { return svc.Status{State: svc.Stopped, Win32ExitCode: 5}, nil }, time.Second); err == nil {
		t.Fatal("stopped service accepted")
	}
	if err = waitService("test", func() (svc.Status, error) { return svc.Status{State: svc.StartPending}, nil }, 0); err == nil {
		t.Fatal("pending service did not time out")
	}
}
