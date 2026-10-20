package gnomedisplay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const displayInterface = "org.gnome.Mutter.DisplayConfig"

type monitorSpec struct{ Connector, Vendor, Product, Serial string }
type mode struct {
	ID                      string
	Width, Height           int32
	Refresh, PreferredScale float64
	Scales                  []float64
	Properties              map[string]dbus.Variant
}
type monitor struct {
	Spec       monitorSpec
	Modes      []mode
	Properties map[string]dbus.Variant
}
type logicalMonitor struct {
	X, Y       int32
	Scale      float64
	Transform  uint32
	Primary    bool
	Monitors   []monitorSpec
	Properties map[string]dbus.Variant
}
type monitorConfig struct {
	Connector, Mode string
	Properties      map[string]dbus.Variant
}
type logicalConfig struct {
	X, Y      int32
	Scale     float64
	Transform uint32
	Primary   bool
	Monitors  []monitorConfig
}
type state struct {
	serial     uint32
	monitors   []monitor
	logical    []logicalMonitor
	properties map[string]dbus.Variant
}

type displayCaller interface {
	CallWithContext(context.Context, string, dbus.Flags, ...any) *dbus.Call
}

func readState(ctx context.Context, object displayCaller) (s state, err error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err = object.CallWithContext(ctx, displayInterface+".GetCurrentState", 0).Store(&s.serial, &s.monitors, &s.logical, &s.properties)
	return
}

func currentMode(m monitor) (mode, bool) {
	for _, mode := range m.Modes {
		if mode.Properties["is-current"].Value() == true {
			return mode, true
		}
	}
	return mode{}, false
}

func (s state) configuration() ([]logicalConfig, error) {
	var result []logicalConfig
	for _, logical := range s.logical {
		config := logicalConfig{logical.X, logical.Y, logical.Scale, logical.Transform, logical.Primary, nil}
		for _, spec := range logical.Monitors {
			found := false
			for _, monitor := range s.monitors {
				if monitor.Spec != spec {
					continue
				}
				mode, ok := currentMode(monitor)
				if !ok {
					break
				}
				config.Monitors = append(config.Monitors, monitorConfig{spec.Connector, mode.ID, map[string]dbus.Variant{}})
				found = true
				break
			}
			if !found {
				return nil, errors.New("cannot snapshot the current GNOME monitor mode")
			}
		}
		result = append(result, config)
	}
	if len(result) == 0 {
		return nil, errors.New("GNOME has no active desktop monitor")
	}
	return result, nil
}

func apply(ctx context.Context, object displayCaller, s state, config []logicalConfig, properties map[string]dbus.Variant) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	options := map[string]dbus.Variant{}
	if layout, ok := properties["layout-mode"]; ok {
		options["layout-mode"] = layout
	}
	// Method 1 changes only the live session, never the user's saved monitors.xml.
	return object.CallWithContext(ctx, displayInterface+".ApplyMonitorsConfig", 0, s.serial, uint32(1), config, options).Err
}

func remoteMonitor(s, original state) (monitor, bool) {
	for _, candidate := range s.monitors {
		if candidate.Spec.Vendor != "Meta" || !strings.HasPrefix(candidate.Spec.Connector, "Meta-") {
			continue
		}
		old := false
		for _, existing := range original.monitors {
			old = old || candidate.Spec == existing.Spec
		}
		if !old {
			return candidate, true
		}
	}
	return monitor{}, false
}

func remoteConfig(remote monitor, scale float64) ([]logicalConfig, error) {
	mode, ok := currentMode(remote)
	if !ok {
		return nil, errors.New("GNOME remote monitor has no current mode")
	}
	chosen := mode.PreferredScale
	for _, supported := range mode.Scales {
		if supported == scale && math.Mod(float64(mode.Width)/scale, 1) == 0 && math.Mod(float64(mode.Height)/scale, 1) == 0 {
			chosen = scale
			break
		}
	}
	if chosen <= 0 {
		return nil, errors.New("GNOME remote monitor has no valid scale")
	}
	return []logicalConfig{{0, 0, chosen, 0, true, []monitorConfig{{remote.Spec.Connector, mode.ID, map[string]dbus.Variant{}}}}}, nil
}

// WithRemoteMonitor keeps the existing desktop on the resizable RDP monitor and restores it on exit.
func WithRemoteMonitor(ctx context.Context, log io.Writer, relay func(context.Context) error) error {
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer bus.Close()
	object := bus.Object(displayInterface, "/org/gnome/Mutter/DisplayConfig")
	return withRemoteMonitor(ctx, object, log, relay)
}

func withRemoteMonitor(ctx context.Context, object displayCaller, log io.Writer, relay func(context.Context) error) error {
	original, err := readState(ctx, object)
	if err != nil {
		return err
	}
	config, err := original.configuration()
	if err != nil {
		return err
	}
	for _, monitor := range original.monitors {
		if strings.HasPrefix(monitor.Spec.Connector, "Meta-") {
			return errors.New("another GNOME virtual display is already active")
		}
	}
	scale := 1.0
	for _, monitor := range original.logical {
		if monitor.Primary {
			scale = monitor.Scale
		}
	}
	connection, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	changed := false
	go func() {
		defer cancel()
		// Authentication and certificate confirmation may wait for user input.
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-connection.Done():
				done <- nil
				return
			case <-ticker.C:
			}
			s, err := readState(connection, object)
			if err != nil {
				done <- err
				return
			}
			remote, found := remoteMonitor(s, original)
			if !found {
				if changed {
					done <- nil
					return
				}
				continue
			}
			if changed {
				continue
			}
			layout, err := remoteConfig(remote, scale)
			if err == nil {
				// Restore even if the compositor applies the change but the reply is lost.
				changed = true
				err = apply(connection, object, s, layout, s.properties)
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	err = relay(connection)
	cancel()
	watchError := <-done
	if changed {
		restoreCtx, stop := context.WithTimeout(context.Background(), 4*time.Second)
		defer stop()
		s, restoreError := readState(restoreCtx, object)
		if restoreError == nil {
			restoreError = apply(restoreCtx, object, s, config, original.properties)
		}
		if restoreError != nil {
			fmt.Fprintln(log, "Restore GNOME desktop:", restoreError)
			if err == nil {
				err = restoreError
			}
		}
	}
	if watchError != nil {
		return fmt.Errorf("configure GNOME remote desktop: %w", watchError)
	}
	return err
}
