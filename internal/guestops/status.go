package guestops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

const ProtocolVersion = 1

// Status contains public capabilities only; credentials use their existing private channel.
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
type StatusProvider func(context.Context) Status

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

func statusHandler(provider StatusProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if provider == nil {
			http.Error(w, "status unavailable", http.StatusNotImplemented)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		status := provider(ctx)
		if status.Validate() != nil {
			http.Error(w, "invalid guest status", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(status)
	}
}

func ReadStatus(ctx context.Context, client *http.Client) (Status, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://guest/v1/status", nil)
	if err != nil {
		return Status{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return Status{}, errors.New("guest status unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Status{}, errors.New("guest status unsupported or unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(data) > 4096 {
		return Status{}, errors.New("invalid guest status size")
	}
	var status Status
	if json.Unmarshal(data, &status) != nil {
		return Status{}, errors.New("invalid guest status response")
	}
	return status, status.Validate()
}
