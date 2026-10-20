package guestops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"my-vm/internal/gueststatus"
)

const ProtocolVersion = gueststatus.ProtocolVersion

type Status = gueststatus.Status
type DesktopStatus = gueststatus.DesktopStatus
type StatusEvent = gueststatus.StatusEvent
type StatusProvider func(context.Context) Status

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
