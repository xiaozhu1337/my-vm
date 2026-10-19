//go:build windows

package console

import "time"

type displaySettings struct {
	width, height int32
	dpi           uintptr
}

type displayUpdate struct {
	connected        bool
	pending, applied displaySettings
	changed, retry   time.Time
}

func (d *displayUpdate) update(connected bool, settings displaySettings, now time.Time, send func(displaySettings) error) (bool, error) {
	if !connected {
		*d = displayUpdate{}
		return false, nil
	}
	d.connected = true
	if d.pending != settings {
		d.pending, d.changed = settings, now
		d.retry = time.Time{}
	}
	// Let interactive resizing settle; a stable desktop needs no display update.
	if settings == d.applied || now.Sub(d.changed) < 200*time.Millisecond || now.Before(d.retry) {
		return false, nil
	}
	if err := send(settings); err != nil {
		d.retry = now.Add(2 * time.Second)
		return true, err
	}
	d.applied = settings
	return true, nil
}
