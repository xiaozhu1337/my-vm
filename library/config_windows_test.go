//go:build windows

package library

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigurationRevisionAndIsolation(t *testing.T) {
	s, c := fixture(t, 2)
	ctx := context.Background()
	cfg, err := c.Configuration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	original := cfg.Machines[0].Name
	cfg.Machines[0].Name = "caller mutation"
	cached, err := c.Configuration(ctx)
	if err != nil || cached.Machines[0].Name != original {
		t.Fatal("client cache escaped", cached, err)
	}
	baseline := s.revision
	value, err := s.Execute(ctx, Request{Action: "config", Revision: baseline})
	if err != nil || !value.(ConfigurationPage).Unchanged {
		t.Fatal(value, err)
	}
	counts := map[string]int{}
	transport := c.http.Transport
	c.http.Transport = localTransport(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		var input Request
		if err = json.Unmarshal(body, &input); err != nil {
			return nil, err
		}
		counts[input.Action]++
		r.Body = io.NopCloser(bytes.NewReader(body))
		return transport.RoundTrip(r)
	})
	if _, err = c.RefreshConfiguration(ctx); err != nil {
		t.Fatal(err)
	}
	if counts["list"] != 1 || counts["config"] != 0 {
		t.Fatal("unchanged polling reloaded configuration", counts)
	}
	cfg = cached
	cfg.Machines[0].Name = "external edit"
	if err = Save(filepath.Join(s.runtime.Root, "library.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	updated, err := c.RefreshConfiguration(ctx)
	if err != nil || updated.Machines[0].Name != "external edit" || s.revision == baseline {
		t.Fatal("external edit was not observed", updated, err)
	}
	if counts["config"] != 1 {
		t.Fatal(counts)
	}
	// Invalid external edits must never be hidden behind stale cached data.
	if err = os.WriteFile(filepath.Join(s.runtime.Root, "library.yaml"), []byte("machines: [invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = c.RefreshConfiguration(ctx); err == nil {
		t.Fatal("invalid configuration silently ignored")
	}
}

func TestCloneConfigurationCopiesNestedSlices(t *testing.T) {
	cfg := Config{Machines: []Machine{{Shares: []Share{{Name: "original"}}, NetworkAdapters: []NetworkAdapter{{}}, COMPorts: []COMPort{{}}}}}
	copied := cloneConfig(cfg)
	copied.Machines[0].Shares[0].Name = "mutated"
	if cfg.Machines[0].Shares[0].Name != "original" || &copied.Machines[0].NetworkAdapters[0] == &cfg.Machines[0].NetworkAdapters[0] || &copied.Machines[0].COMPorts[0] == &cfg.Machines[0].COMPorts[0] {
		t.Fatal("mutable slices escaped configuration cache")
	}
}
