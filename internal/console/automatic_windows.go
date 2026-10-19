//go:build windows

package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"my-vm/internal/guestops"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type displayCapability struct {
	Mode     string `json:"mode"`
	Username string `json:"username,omitempty"`
	Password []byte `json:"password,omitempty"`
}

// All commands are fixed read-only queries, executed as the already logged-in desktop user.
const linuxDisplayQuery = `set -eu
case "$XDG_SESSION_TYPE" in
 x11) systemctl is-active --quiet xrdp; printf 'x11\n';;
 wayland)
  locked=$(loginctl show-session auto -p LockedHint --value) || exit 1
  [ "$locked" = no ] || exit 1
  case ":$XDG_CURRENT_DESKTOP:" in
   *:GNOME:*|*:gnome:*|*:ubuntu:*)
    systemctl --user is-active --quiet gnome-remote-desktop.service
    printf 'wayland\n'
    LC_ALL=C grdctl status --show-credentials;;
   *:KDE:*|*:kde:*|*:plasma:*)
    systemctl --user is-active --quiet app-org.kde.krdpserver.service
    printf 'wayland\n';;
   *) exit 1;;
  esac;;
 *) exit 1;;
esac`

func queryDisplay(runtimeID, kind string) (displayCapability, error) {
	// Windows enhanced transport reports its own availability; vmicrdv can be trigger-started.
	if kind == "windows" {
		return displayCapability{Mode: "windows"}, nil
	}
	client, err := guestClient(runtimeID)
	if err != nil {
		return displayCapability{}, err
	}
	defer client.CloseIdleConnections()
	return displayFromGuest(client, kind)
}

func displayFromGuest(client *http.Client, kind string) (displayCapability, error) {
	args := []string{"/bin/sh", "-c", linuxDisplayQuery}
	data, err := json.Marshal(guestops.Command{Args: args, Timeout: 2})
	if err != nil {
		return displayCapability{}, err
	}
	response, err := client.Post("http://guest/exec", "application/json", bytes.NewReader(data))
	if err != nil {
		return displayCapability{}, errors.New("guest display query unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return displayCapability{}, errors.New("guest display query rejected")
	}
	var result guestops.Result
	if err = json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&result); err != nil {
		return displayCapability{}, errors.New("invalid guest display response")
	}
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if result.ExitCode != 0 || result.Error != "" || result.Truncated {
		return displayCapability{}, errors.New("guest desktop is not ready")
	}
	capability, err := parseDisplay(result.Stdout)
	if err != nil {
		return displayCapability{}, err
	}
	if (kind == "windows") != (capability.Mode == "windows") || kind == "x11" && capability.Mode != "x11" || kind == "wayland" && capability.Mode != "wayland" {
		clear(capability.Password)
		return displayCapability{}, errors.New("guest desktop mode differs from configured mode")
	}
	return capability, nil
}

func parseDisplay(data []byte) (displayCapability, error) {
	first, body, _ := bytes.Cut(data, []byte{'\n'})
	result := displayCapability{Mode: strings.TrimSpace(string(first))}
	if result.Mode != "windows" && result.Mode != "wayland" && result.Mode != "x11" {
		return displayCapability{}, errors.New("unknown guest desktop mode")
	}
	if result.Mode != "wayland" {
		return result, nil
	}
	inRDP, sawRDP, enabled := false, false, false
	for line := range bytes.SplitSeq(body, []byte{'\n'}) {
		if len(line) > 0 && line[0] != '\t' && line[0] != ' ' {
			inRDP = bytes.Equal(bytes.TrimSpace(line), []byte("RDP:"))
			sawRDP = sawRDP || inRDP
		}
		if !inRDP {
			continue
		}
		line = bytes.TrimSuffix(bytes.TrimLeft(line, "\t "), []byte{'\r'})
		if bytes.Equal(line, []byte("Status: enabled")) {
			enabled = true
		}
		if value, ok := bytes.CutPrefix(line, []byte("Username: ")); ok {
			result.Username = string(value)
		}
		if value, ok := bytes.CutPrefix(line, []byte("Password: ")); ok {
			result.Password = append(result.Password[:0], value...)
		}
	}
	if !sawRDP {
		return result, nil
	} // KRdp uses its existing saved/manual login.
	if !enabled || result.Username == "" || len(result.Username) > 256 || len(result.Password) == 0 || len(result.Password) > 1024 || bytes.ContainsAny(result.Password, "\x00\r\n") {
		clear(result.Password)
		return displayCapability{}, errors.New("GNOME desktop credentials unavailable")
	}
	return result, nil
}

func probeDisplay(runtimeID, kind string, log io.Writer) displayCapability {
	if kind == "windows" {
		return displayCapability{Mode: "windows"}
	}
	executable, err := os.Executable()
	if err != nil {
		return displayCapability{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "--console", "--detect-display", runtimeID, kind)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	output, err := command.Output()
	defer clear(output)
	var result displayCapability
	if err == nil && len(output) <= 8192 {
		err = json.Unmarshal(output, &result)
	}
	if err != nil || (result.Mode != "windows" && result.Mode != "wayland" && result.Mode != "x11") {
		clear(result.Password)
		if log != nil {
			fmt.Fprintln(log, "增强桌面尚未就绪，使用基础控制台")
		}
		return displayCapability{}
	}
	return result
}

func (d *displayCapability) login() *loginCredential {
	defer clear(d.Password)
	if d.Username == "" || len(d.Password) == 0 {
		return nil
	}
	// The response is bounded and never written to logs or the host credential store.
	password := utf16.Encode([]rune(string(d.Password)))
	if len(password) == 0 || len(password) > 256 {
		clear(password)
		return nil
	}
	return &loginCredential{username: d.Username, password: password}
}

func (v *viewer) connectionFailed(err error) {
	v.fail(err)
	v.requestBasic()
}

func (v *viewer) requestBasic() {
	if !v.automatic || !v.enhanced || v.closed || v.fallbackPending {
		return
	}
	v.fallbackPending = true
	// Do not dismantle COM while processing one of its callbacks, or time out authentication UI.
	if v.hwnd != 0 {
		user32.NewProc("PostMessageW").Call(v.hwnd, 0x8002, 0, 0)
	}
}

func basicDisplayPipe(pipe string) string {
	if index := strings.Index(pipe, ".enhanced.linux-"); index >= 0 {
		return pipe[:index] + ".basic"
	}
	return strings.TrimSuffix(strings.TrimSuffix(pipe, ".enhanced"), ".basic") + ".basic"
}

func (v *viewer) fallbackBasic() {
	if !v.automatic || !v.enhanced || v.closed {
		return
	}
	v.switchDisplay(false, true)
}

func (v *viewer) switchDisplay(enhanced, automatic bool) {
	if v.closed {
		return
	}
	hwnd, header, headerClose := v.hwnd, v.header, v.headerClose
	headerHidden, toolbar := v.headerHidden, v.toolbar
	v.clearPrompt()
	pipe, runtimeID, title, log := basicDisplayPipe(v.pipe), v.vmid, v.title, v.log
	linux, clipboard, linuxMode, machineID := v.linux, v.clipboard, v.linuxMode, v.machineID
	v.close()
	if enhanced {
		pipe = strings.TrimSuffix(pipe, ".basic") + ".enhanced"
	}
	*v = viewer{hwnd: hwnd, header: header, headerClose: headerClose, headerHidden: headerHidden, toolbar: toolbar,
		pipe: pipe, vmid: runtimeID, title: title, log: log, automatic: automatic, enhanced: enhanced, linux: linux, clipboard: clipboard, linuxMode: linuxMode, machineID: machineID}
	if enhanced && linux {
		v.wayland = linuxMode == "wayland" || linuxMode == "" && detectLinuxWayland(runtimeID, log)
	}
	if v.wayland {
		capability := probeDisplay(runtimeID, "wayland", log)
		v.automaticLogin = capability.login()
		port, stop, err := linuxWaylandBridge(runtimeID, log)
		if err != nil {
			v.connectionFailed(err)
			return
		}
		v.stopBridge, v.rdpPort = stop, port
	} else if enhanced && linux {
		v.pipe += fmt.Sprintf(".linux-%d", os.Getpid())
		stop, err := linuxBridge(v.pipe, runtimeID, log)
		if err != nil {
			v.connectionFailed(err)
			return
		}
		v.stopBridge = stop
	}
	menu, _, _ := user32.NewProc("GetSystemMenu").Call(hwnd, 0)
	user32.NewProc("DeleteMenu").Call(menu, forgetLoginCommand, 0)
	if v.wayland {
		label := windows.StringToUTF16Ptr("忘记 Linux 登录凭据")
		user32.NewProc("AppendMenuW").Call(menu, 0, forgetLoginCommand, uintptr(unsafe.Pointer(label)))
	}
	mode := "基础控制台"
	if enhanced {
		mode = "增强会话"
	}
	caption := windows.StringToUTF16Ptr("VM · " + title + " · " + mode)
	user32.NewProc("SetWindowTextW").Call(hwnd, uintptr(unsafe.Pointer(caption)))
	v.setStatus("正在连接" + mode)
	if log != nil {
		fmt.Fprintln(log, "切换画面:", mode)
	}
	if err := v.initialize(); err != nil {
		v.connectionFailed(err)
	}
	user32.NewProc("SetTimer").Call(hwnd, 1, 300, 0)
}
