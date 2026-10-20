package gnomedisplay

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func fixture() state {
	physical := monitor{Spec: monitorSpec{"Virtual-1", "Microsoft", "Hyper-V", "1"}, Modes: []mode{{ID: "1280x720", Width: 1280, Height: 720, PreferredScale: 1, Scales: []float64{1}, Properties: map[string]dbus.Variant{"is-current": dbus.MakeVariant(true)}}}}
	return state{serial: 7, monitors: []monitor{physical}, logical: []logicalMonitor{{Scale: 1, Primary: true, Monitors: []monitorSpec{physical.Spec}}}, properties: map[string]dbus.Variant{"layout-mode": dbus.MakeVariant(uint32(1))}}
}

func virtual() monitor {
	return monitor{Spec: monitorSpec{"Meta-0", "Meta", "Remote desktop", "1"}, Modes: []mode{{ID: "1720x1200", Width: 1720, Height: 1200, PreferredScale: 1, Scales: []float64{1, 1.5, 2}, Properties: map[string]dbus.Variant{"is-current": dbus.MakeVariant(true)}}}}
}

func TestDisplayConfigWireSignatures(t *testing.T) {
	if got := dbus.SignatureOf([]monitor{}).String(); got != "a((ssss)a(siiddada{sv})a{sv})" {
		t.Fatal(got)
	}
	if got := dbus.SignatureOf([]logicalMonitor{}).String(); got != "a(iiduba(ssss)a{sv})" {
		t.Fatal(got)
	}
	if got := dbus.SignatureOf([]logicalConfig{}).String(); got != "a(iiduba(ssa{sv}))" {
		t.Fatal(got)
	}
}

func TestOnlyNewRemoteMonitorIsSelected(t *testing.T) {
	original := fixture()
	current := fixture()
	other := virtual()
	other.Spec.Connector, other.Spec.Vendor = "HDMI-1", "Dell"
	current.monitors = append(current.monitors, other)
	if _, ok := remoteMonitor(current, original); ok {
		t.Fatal("selected a physical hotplug")
	}
	current.monitors = append(current.monitors, virtual())
	remote, ok := remoteMonitor(current, original)
	if !ok || remote.Spec.Connector != "Meta-0" {
		t.Fatal(remote, ok)
	}
	if _, ok := remoteMonitor(current, current); ok {
		t.Fatal("selected another session's virtual monitor")
	}
	layout, err := remoteConfig(remote, 1.5)
	if err != nil || len(layout) != 1 || !layout[0].Primary || len(layout[0].Monitors) != 1 || layout[0].Monitors[0].Connector != "Meta-0" || layout[0].Scale != 1 {
		t.Fatal(layout, err)
	}
	layout, err = remoteConfig(remote, 2)
	if err != nil || layout[0].Scale != 2 {
		t.Fatal(layout, err)
	}
	remote.Modes[0].Properties = nil
	if _, err := remoteConfig(remote, 1); err == nil {
		t.Fatal("accepted a monitor without an active mode")
	}
}

type fakeDisplay struct {
	mu             sync.Mutex
	current        state
	calls          int
	configurations [][]logicalConfig
	fail           bool
	applied        chan struct{}
}

func (f *fakeDisplay) CallWithContext(_ context.Context, method string, _ dbus.Flags, args ...any) *dbus.Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	if method == displayInterface+".GetCurrentState" {
		f.calls++
		s := f.current
		if f.calls > 1 {
			s.monitors = append(append([]monitor(nil), s.monitors...), virtual())
		}
		s.serial = uint32(f.calls)
		return &dbus.Call{Body: []any{s.serial, s.monitors, s.logical, s.properties}}
	}
	if method != displayInterface+".ApplyMonitorsConfig" || args[1] != uint32(1) {
		panic("expected temporary ApplyMonitorsConfig")
	}
	f.configurations = append(f.configurations, args[2].([]logicalConfig))
	if len(f.configurations) == 1 {
		close(f.applied)
		if f.fail {
			return &dbus.Call{Err: errors.New("compositor rejected change")}
		}
	}
	return &dbus.Call{}
}

func TestDesktopMovesAndRestoresOnDisconnectAndConfigurationError(t *testing.T) {
	for _, fail := range []bool{false, true} {
		f := &fakeDisplay{current: fixture(), fail: fail, applied: make(chan struct{})}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := withRemoteMonitor(ctx, f, io.Discard, func(connection context.Context) error {
			if fail {
				<-connection.Done()
				return nil
			}
			select {
			case <-f.applied:
				return nil
			case <-connection.Done():
				return connection.Err()
			}
		})
		cancel()
		if (err != nil) != fail {
			t.Fatal(err)
		}
		if len(f.configurations) != 2 {
			t.Fatal("desktop was not restored", f.configurations)
		}
		if f.configurations[0][0].Monitors[0].Connector != "Meta-0" {
			t.Fatal("did not select remote display")
		}
		original, _ := fixture().configuration()
		if !reflect.DeepEqual(f.configurations[1], original) {
			t.Fatal("lost original monitor configuration", f.configurations[1])
		}
	}
}

func TestMissingCurrentModeDoesNotTouchDesktop(t *testing.T) {
	s := fixture()
	s.monitors[0].Modes = nil
	f := &fakeDisplay{current: s}
	called := false
	err := withRemoteMonitor(context.Background(), f, io.Discard, func(context.Context) error { called = true; return nil })
	if err == nil || called || len(f.configurations) != 0 {
		t.Fatal("connected without a restorable desktop", err)
	}
}
