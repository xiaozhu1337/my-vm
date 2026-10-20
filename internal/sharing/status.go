package sharing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"my-vm/internal/gueststatus"
)

const statusLifetime = 15 * time.Second

// StatusFeed belongs to one VM listener and is discarded when that VM stops.
type StatusFeed struct {
	mu       sync.Mutex
	status   gueststatus.Status
	received time.Time
	changed  chan struct{}
	closed   bool
}

func NewStatusFeed() *StatusFeed { return &StatusFeed{changed: make(chan struct{})} }

func (s *StatusFeed) publish(status gueststatus.Status, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.status, s.received = status, now
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *StatusFeed) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.changed)
	}
}

func (s *StatusFeed) snapshot(now time.Time) (gueststatus.StatusEvent, <-chan struct{}, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wait := statusLifetime
	var event gueststatus.StatusEvent
	if !s.received.IsZero() && now.Before(s.received.Add(statusLifetime)) {
		status := s.status
		event.Status = &status
		wait = s.received.Add(statusLifetime).Sub(now)
	}
	return event, s.changed, wait, s.closed
}

// Stream forwards changes and heartbeats; a missing heartbeat invalidates ready.
func (s *StatusFeed) Stream(ctx context.Context, emit func(gueststatus.StatusEvent) error) error {
	for {
		event, changed, wait, closed := s.snapshot(time.Now())
		if closed {
			return nil
		}
		if err := emit(event); err != nil {
			return err
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (s *StatusFeed) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		var status gueststatus.Status
		err := decoder.Decode(&status)
		if err == nil && decoder.Decode(new(any)) != io.EOF {
			err = errors.New("trailing status data")
		}
		if err != nil || status.Validate() != nil {
			http.Error(w, "invalid guest status", http.StatusBadRequest)
			return
		}
		// VM identity comes from the bound Hvsock listener, never from this body.
		s.publish(status, time.Now())
		w.WriteHeader(http.StatusNoContent)
	})
}
