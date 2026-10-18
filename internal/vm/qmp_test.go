package vm

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func TestQMPEventsAndError(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		encoder := json.NewEncoder(server)
		decoder := json.NewDecoder(server)
		if err := encoder.Encode(map[string]any{"QMP": map[string]any{"version": "test"}}); err != nil {
			done <- err
			return
		}
		for _, command := range []string{"qmp_capabilities", "stop", "cont"} {
			var request map[string]string
			if err := decoder.Decode(&request); err != nil {
				done <- err
				return
			}
			if request["execute"] != command {
				done <- &testError{"unexpected command"}
				return
			}
			if err := encoder.Encode(map[string]string{"event": "STOP"}); err != nil {
				done <- err
				return
			}
			reply := map[string]any{"id": request["id"], "return": map[string]any{}}
			if command == "cont" {
				delete(reply, "return")
				reply["error"] = map[string]string{"desc": "test failure"}
			}
			if err := encoder.Encode(reply); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	monitor, err := newMonitor(client)
	if err != nil {
		t.Fatal(err)
	}
	if err = monitor.execute("stop"); err != nil {
		t.Fatal(err)
	}
	if err = monitor.execute("cont"); err == nil || !strings.Contains(err.Error(), "test failure") {
		t.Fatalf("missing QMP error: %v", err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestQMPRecoversAfterTimeout(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		encoder := json.NewEncoder(server)
		decoder := json.NewDecoder(server)
		if err := encoder.Encode(map[string]any{"QMP": map[string]any{}}); err != nil {
			done <- err
			return
		}
		var previousID string
		for _, command := range []string{"qmp_capabilities", "stop", "cont"} {
			var request map[string]string
			if err := decoder.Decode(&request); err != nil {
				done <- err
				return
			}
			if request["execute"] != command || request["id"] == previousID {
				done <- &testError{"command or ID mismatch"}
				return
			}
			previousID = request["id"]
			if command == "stop" {
				<-release
			}
			if err := encoder.Encode(map[string]any{"id": request["id"], "return": map[string]any{}}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	monitor, err := newMonitor(client)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer monitor.close()
	monitor.timeout = 20 * time.Millisecond
	err = monitor.execute("stop")
	close(release)
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("expected timeout, got %v", err)
	}
	monitor.timeout = time.Second
	if err = monitor.execute("cont"); err != nil {
		t.Fatalf("late stop response must not poison next command: %v", err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

type testError struct{ message string }

func (e *testError) Error() string { return e.message }
