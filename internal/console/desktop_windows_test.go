//go:build windows

package console

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"my-vm/internal/guestops"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

type desktopTransport func(*http.Request) (*http.Response, error)

func (f desktopTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestMain(m *testing.M) {
	if len(os.Args) == 5 && os.Args[1] == "--console" && os.Args[2] == "--detect-ready" {
		state := os.Args[3]
		if state == "legacy-slow" {
			if strings.HasSuffix(os.Args[4], "-status") {
				time.Sleep(2 * time.Second)
				fmt.Println("unavailable")
			} else {
				time.Sleep(1500 * time.Millisecond)
				fmt.Println("ready")
			}
			os.Exit(0)
		}
		if state == "known-unknown" {
			if strings.HasSuffix(os.Args[4], "-status") {
				fmt.Println("unknown")
			} else {
				fmt.Println("ready")
			}
			os.Exit(0)
		}
		if state == "blocked" {
			for {
				time.Sleep(time.Second)
			}
		}
		if state == "fallback" {
			state = "unknown"
			if os.Args[4] == "windows-agent" {
				state = "ready"
			}
		}
		fmt.Println(state)
		os.Exit(0)
	}
	if len(os.Args) == 5 && os.Args[1] == "--console" && os.Args[2] == "--detect-display" {
		if os.Args[3] == "blocked" {
			for {
				time.Sleep(time.Second)
			}
		}
		_ = json.NewEncoder(os.Stdout).Encode(displayCapability{Mode: os.Args[3]})
		os.Exit(0)
	}
	if len(os.Args) == 4 && os.Args[1] == "--console" && os.Args[2] == "--detect-desktop" {
		if os.Args[3] == "blocked" {
			for {
				time.Sleep(time.Second)
			}
		}
		fmt.Println(os.Args[3])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDesktopProbeProcess(t *testing.T) {
	for _, test := range []struct {
		kind string
		want bool
	}{{"wayland", true}, {"x11", false}, {"invalid", false}, {"blocked", false}} {
		t.Run(test.kind, func(t *testing.T) {
			started := time.Now()
			if got := detectLinuxWayland(test.kind, io.Discard); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
			if elapsed := time.Since(started); elapsed > 10*time.Second {
				t.Fatalf("probe was not bounded: %v", elapsed)
			}
		})
	}
}

func TestLinuxSessionTypeUsesProtocolEvenWhenLocked(t *testing.T) {
	for _, test := range []struct{ os, mode, state, want string }{
		{"linux", "wayland", "waiting", "wayland"}, {"linux", "x11", "ready", "x11"}, {"linux", "", "unsupported", ""}, {"windows", "windows", "ready", ""},
	} {
		calls := 0
		client := &http.Client{Transport: desktopTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != "GET" || r.URL.Path != "/v1/status" {
				t.Fatal("session detection used an external command")
			}
			data, _ := json.Marshal(guestops.Status{Protocol: 1, OS: test.os, Desktop: guestops.DesktopStatus{Mode: test.mode, State: test.state}})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
		})}
		got, err := linuxSessionType(client)
		if got != test.want || (err == nil) != (test.want != "") || calls != 1 {
			t.Fatalf("session type=%s err=%v calls=%d", got, err, calls)
		}
	}
}

func TestLinuxSessionType(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
		status           int
	}{
		{"wayland", `{"stdout":"d2F5bGFuZAo=","exit_code":0}`, "wayland", 200},
		{"x11", `{"stdout":"eDExCg==","exit_code":0}`, "x11", 200},
		{"unset", `{"exit_code":1,"error":"exit status 1"}`, "", 200},
		{"not-desktop", `{"stdout":"dHR5Cg==","exit_code":0}`, "", 200},
		{"truncated", `{"stdout":"d2F5bGFuZAo=","exit_code":0,"truncated":true}`, "", 200},
		{"old-agent", "not found", "", 404},
		{"invalid", "{", "", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: desktopTransport(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/v1/status" {
					return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("old agent"))}, nil
				}
				if request.Method != "POST" || request.URL.Path != "/exec" {
					t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
				}
				var command guestops.Command
				if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
					t.Fatal(err)
				}
				if strings.Join(command.Args, " ") != "/usr/bin/printenv XDG_SESSION_TYPE" || command.Timeout != 1 {
					t.Fatalf("unexpected command: %#v", command)
				}
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}
			got, err := linuxSessionType(client)
			if got != test.want || (err == nil) != (test.want != "") {
				t.Fatalf("got %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestInstalledLinuxDesktop(t *testing.T) {
	runtimeID := os.Getenv("MY_VM_LINUX_RUNTIME")
	if runtimeID == "" {
		t.Skip("MY_VM_LINUX_RUNTIME is not set")
	}
	kind, err := queryLinuxSession(runtimeID)
	if err != nil || kind != "wayland" {
		t.Fatalf("installed desktop was not detected as Wayland: %q %v", kind, err)
	}
	port, stop, err := linuxWaylandBridge(runtimeID, testOutput{t})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	connection, err := net.DialTimeout("tcp4", "127.0.0.1:"+strconv.Itoa(port), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(8 * time.Second))
	request := []byte{3, 0, 0, 19, 14, 0xe0, 0, 0, 0, 0, 0, 1, 0, 8, 0, 3, 0, 0, 0}
	if _, err = connection.Write(request); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 19)
	if _, err = io.ReadFull(connection, response); err != nil {
		t.Fatal(err)
	}
	if response[0] != 3 || response[11] != 2 || binary.LittleEndian.Uint32(response[15:]) != 2 {
		t.Fatalf("server did not negotiate TLS/CredSSP: %x", response)
	}
	// This probe checks transport only; production ActiveX handles certificate trust.
	secure := tls.Client(connection, &tls.Config{InsecureSkipVerify: true})
	if err = secure.Handshake(); err != nil {
		t.Fatal(err)
	}
	t.Logf("Wayland RDP/CredSSP transport reached guest; TLS version %#x", secure.ConnectionState().Version)
}

type testOutput struct{ t *testing.T }

func (w testOutput) Write(data []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(data)))
	return len(data), nil
}

func TestConflictingLinuxDesktopFlags(t *testing.T) {
	if err := Run([]string{"--pipe", `\\.\pipe\my-vm-test.enhanced`, "--enhanced", "--linux", "--wayland", "--x11"}, io.Discard); err == nil || err.Error() != "控制台会话参数无效" {
		t.Fatal("conflicting desktop flags were accepted")
	}
}
