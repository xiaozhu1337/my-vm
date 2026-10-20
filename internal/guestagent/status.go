package guestagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"my-vm/internal/guestops"
	"my-vm/internal/sharing"
)

func publishStatus(ctx context.Context, client *http.Client, address string, status guestops.Status) error {
	if err := status.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(data))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := client.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("status publish: HTTP %d", response.StatusCode)
	}
	return nil
}

func runStatusPublisher(ctx context.Context, log io.Writer, provider guestops.StatusProvider) {
	if provider == nil {
		return
	}
	// Reuse the local proxy so uncancellable native Hvsock dials cannot hold agent shutdown.
	transport := &http.Transport{MaxConnsPerHost: 1, ResponseHeaderTimeout: 3 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	monitorStatus(ctx, log, provider, func(ctx context.Context, status guestops.Status) error {
		return publishStatus(ctx, client, "http://"+sharing.ProxyAddress+"/v1/status", status)
	}, time.Second, 5*time.Second)
}

// shortcut: sample existing bounded providers once per second; add OS event sources if subsecond detection is needed.
func monitorStatus(ctx context.Context, log io.Writer, provider guestops.StatusProvider, publish func(context.Context, guestops.Status) error, interval, heartbeat time.Duration) {
	var last guestops.Status
	var sent time.Time
	failed := false
	for ctx.Err() == nil {
		query, cancel := context.WithTimeout(ctx, 2*time.Second)
		status := provider(query)
		cancel()
		if status != last || sent.IsZero() || time.Since(sent) >= heartbeat {
			err := status.Validate()
			if err == nil {
				err = publish(ctx, status)
			}
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				last, sent = status, time.Now()
				if failed && log != nil {
					fmt.Fprintln(log, "Guest status: host connection restored")
				}
				failed = false
			} else if !failed {
				if log != nil {
					fmt.Fprintln(log, "Guest status: host connection unavailable:", err)
				}
				failed = true
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
