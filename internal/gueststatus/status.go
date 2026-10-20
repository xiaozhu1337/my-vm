// Package gueststatus defines public status messages shared by both Hvsock directions.
package gueststatus

import "errors"

const ProtocolVersion = 1

// Status contains capabilities only, never credentials, paths or command output.
type Status struct {
	Protocol int           `json:"protocol"`
	OS       string        `json:"os"`
	Desktop  DesktopStatus `json:"desktop"`
}

type DesktopStatus struct {
	Mode   string `json:"mode"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// A nil status means the agent has not reported recently; it never implies ready.
type StatusEvent struct {
	Status *Status `json:"status"`
}

func (s Status) Validate() error {
	if s.Protocol != ProtocolVersion || s.OS != "windows" && s.OS != "linux" {
		return errors.New("unsupported guest protocol")
	}
	if s.Desktop.Mode != "" && s.Desktop.Mode != "windows" && s.Desktop.Mode != "x11" && s.Desktop.Mode != "wayland" {
		return errors.New("invalid desktop mode")
	}
	if s.OS == "windows" && s.Desktop.Mode != "windows" || s.OS == "linux" && s.Desktop.Mode == "windows" {
		return errors.New("desktop OS mismatch")
	}
	switch s.Desktop.State {
	case "ready", "available":
		if s.Desktop.Mode == "" {
			return errors.New("missing desktop mode")
		}
	case "waiting", "unsupported", "unknown":
	default:
		return errors.New("invalid desktop state")
	}
	if len(s.Desktop.Reason) > 64 {
		return errors.New("invalid desktop reason")
	}
	for _, c := range s.Desktop.Reason {
		if c != '_' && (c < 'a' || c > 'z') {
			return errors.New("invalid desktop reason")
		}
	}
	return nil
}
