//go:build windows

package console

import (
	"bytes"
	"encoding/json"
	"my-vm/internal/console/renderhost"
	"os"
	"strings"
	"testing"
)

func TestSessionEventsRequireLoginBeforeWindowsDisplayUpdates(t *testing.T) {
	v := &viewer{enhanced: true}
	v.sessionEvent(1)
	v.sessionEvent(2)
	if v.loggedIn {
		t.Fatal("connection establishment was mistaken for login completion")
	}
	v.sessionEvent(3)
	if !v.loggedIn {
		t.Fatal("login completion was ignored")
	}
	v.display.applied = displaySettings{1280, 720, 96}
	for _, event := range []int32{4, 17, 34, 1} {
		v.sessionEvent(event)
		if v.loggedIn || v.display.applied != (displaySettings{}) {
			t.Fatal("disconnect or reconnect retained the previous display state", event)
		}
		v.sessionEvent(33)
		if !v.loggedIn || v.display.applied != (displaySettings{}) {
			t.Fatal("reconnection did not invalidate applied display settings")
		}
		v.display.applied = displaySettings{1280, 720, 96}
	}
	v.closed = true
	v.loggedIn = false
	v.sessionEvent(3)
	if v.loggedIn {
		t.Fatal("closed viewer accepted a late event")
	}
}

func TestCertificateConfirmationSuspendsAutomaticConnectionDeadline(t *testing.T) {
	var input, output bytes.Buffer
	identity := renderhost.Packet{Kind: "start", Token: strings.Repeat("a", 64), Generation: 1, PID: os.Getpid()}
	if err := json.NewEncoder(&input).Encode(identity); err != nil {
		t.Fatal(err)
	}
	session, err := renderhost.Open(&input, &output, identity.Token, identity.Generation)
	if err != nil {
		t.Fatal(err)
	}
	v := &viewer{renderer: session, hwnd: 1234, enhanced: true}
	v.sessionEvent(18)
	var packet renderhost.Packet
	if err := json.NewDecoder(&output).Decode(&packet); err != nil {
		t.Fatal(err)
	}
	if packet.Kind != "authenticating" || packet.Window != 1234 || packet.Token != identity.Token || packet.Generation != identity.Generation || packet.PID != identity.PID {
		t.Fatal("certificate confirmation was not reported to the authenticated host", packet)
	}
}

func TestWaylandAuthenticationEventsKeepConsoleOpen(t *testing.T) {
	v := &viewer{enhanced: true, linux: true, wayland: true}
	for _, event := range []int32{1, 18, 19} {
		v.sessionEvent(event)
		if v.closed || v.status == "" || v.loggedIn {
			t.Fatalf("authentication event %d closed or completed the console: %+v", event, v)
		}
	}
	if v.status != "正在连接 Linux 桌面 · 请完成剩余的登录提示" {
		t.Fatal("certificate dismissal was treated as login completion", v.status)
	}
	v.sessionEvent(3)
	if !v.loggedIn || v.closed {
		t.Fatal("login did not keep the console available")
	}
	v.setStatus("增强会话已断开：客户机桌面已锁定")
	v.sessionEvent(4)
	if v.closed || v.loggedIn || v.status != "增强会话已断开：客户机桌面已锁定" {
		t.Fatal("disconnect closed the console or hid the error", v.status)
	}
}
