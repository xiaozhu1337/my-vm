//go:build windows

package console

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"my-vm/internal/console/renderhost"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
)

var user32 = windows.NewLazySystemDLL("user32.dll")
var atl = windows.NewLazySystemDLL("atl.dll")

const dismissHeaderCommand = 100

type viewer struct {
	host                                                        *renderhost.Host
	renderer                                                    *renderhost.Session
	parent, parentPID, renderWindow                             uintptr
	renderToken                                                 string
	renderGeneration                                            uint64
	rendererMode, reportedConnected, reportedFailure            bool
	reportedLayout                                              bool
	rendererTopmost, rendererLayoutVisible, rendererLayoutKnown bool
	foregroundHook                                              uintptr
	pointerRoot                                                 uintptr
	hostTopmost, adjustingLayer                                 bool
	hwnd, header, headerClose, child                            uintptr
	headerHidden, statusError                                   bool
	hideConnectedNotice                                         bool
	toolbar                                                     *consoleToolbar
	machineID, linuxMode                                        string
	client, advanced                                            *ole.IDispatch
	context, connector, nonScriptable                           *ole.IUnknown
	displayClient                                               *ole.IUnknown
	callback                                                    *connectorCallback
	events                                                      *sessionEvents
	eventPoint                                                  *ole.IConnectionPoint
	eventCookie                                                 uint32
	loggedIn                                                    bool
	generation                                                  uintptr
	loginTarget, loginNotice                                    string
	pendingLogin, automaticLogin                                *loginCredential
	automatic, fallbackPending                                  bool
	fallbackReason                                              string
	enhanced, clipboard, linux, wayland, closed                 bool
	dynamicResolution                                           bool
	stopBridge                                                  func()
	rdpPort                                                     int
	pipe, vmid, title, status                                   string
	log                                                         io.Writer
	layout                                                      displaySettings
	display                                                     displayUpdate
	displayError                                                string
}

var current *viewer

func (v *viewer) fail(err error) {
	fmt.Fprintln(v.log, err)
	v.clearPrompt()
	v.setStatus(err.Error())
	v.statusError = true
	v.renderStatus()
}
func (v *viewer) setStatus(status string) {
	if v.toolbar != nil && v.toolbar.prompt != promptNone {
		status, _, _ = v.toolbar.prompt.labels()
	}
	if v.loginNotice != "" && strings.HasPrefix(status, "已连接") {
		status += " · " + v.loginNotice
	}
	if v.toolbar != nil && v.toolbar.notice != "" && strings.HasPrefix(status, "已连接") {
		status += " · " + v.toolbar.notice
	}
	if status == v.status {
		return
	}
	v.status = status
	v.statusError = strings.Contains(status, "失败") || v.toolbar != nil && v.toolbar.noticeError && strings.Contains(status, v.toolbar.notice)
	v.renderStatus()
}

func (v *viewer) renderStatus() {
	if v.header != 0 {
		status := v.status
		if v.toolbar != nil && v.toolbar.guestNotice != "" && strings.HasPrefix(status, "已连接") {
			status += " · " + v.toolbar.guestNotice
		}
		user32.NewProc("SetWindowTextW").Call(v.header, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(status))))
		user32.NewProc("InvalidateRect").Call(v.header, 0, 0)
	}
	if v.hideConnectedNotice && v.canHideConnectedNotice() {
		v.dismissHeader()
	}
}

type message struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}
type windowClass struct {
	Size, Style                        uint32
	Procedure                          uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}

var procedure = syscall.NewCallback(func(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	v := current
	switch msg {
	case 0x8005:
		if v != nil {
			v.rendererCommands()
		}
		return 0
	case 0x2b:
		if v != nil && v.paintControl(lparam) {
			return 1
		}
	case 0x14:
		if v != nil && v.toolbar != nil && v.toolbar.painter != nil {
			v.toolbar.painter.Background(wparam, hwnd)
			return 1
		}
	case 0x15, 0x1a, 0x31a:
		if v != nil && v.toolbar != nil && v.toolbar.painter != nil {
			v.toolbar.painter.SettingsChanged()
			user32.NewProc("RedrawWindow").Call(hwnd, 0, 0, 0x85)
		}
	case 0x8002:
		if v != nil && v.acceptFallback(wparam) {
			v.fallbackBasic()
		}
		return 0
	case 0x8006:
		if v != nil {
			v.guestStatusResult()
		}
		return 0
	case 0x8004:
		if v != nil {
			v.enhancedAvailabilityResult()
		}
		return 0
	case 0x8003:
		if v != nil {
			v.finishToolbarAction()
		}
		return 0
	case 0x8001:
		if v != nil && v.nonScriptable != nil {
			up := [2]int16{0, -1}
			keys := [2]uint32{uint32(wparam) | 1, uint32(wparam) | 0xC0000001}
			if err := call(v.nonScriptable, 14, 2, uintptr(unsafe.Pointer(&up[0])), uintptr(unsafe.Pointer(&keys[0]))); err != nil {
				v.fail(err)
			}
		}
		return 0
	case 0x47, 0x6, 0x232:
		// DefWindowProc must still deliver WM_MOVE/WM_SIZE and activation handling.
		if v != nil && v.host != nil {
			v.resizeRenderer()
			if msg == 0x6 && wparam == 1 { // WA_ACTIVE, not mouse activation or minimization.
				v.focusRenderer()
			}
		}
	case 3, 5, 0x18, 0x113:
		if v != nil && v.hwnd != 0 {
			v.resize()
			if msg == 0x113 {
				v.syncPointerFocus(true)
				v.pollRenderer()
				v.checkEnhancedAvailability()
			}
		}
		return 0
	case 0x7:
		// Mouse activation must leave the host free to drag its title bar or resize its frame.
		if v != nil && v.child != 0 {
			user32.NewProc("SetFocus").Call(v.child)
		}
		return 0
	case 0x24:
		if v != nil && !v.rendererMode {
			var limits [10]int32
			if err := windows.ReadProcessMemory(windows.CurrentProcess(), lparam, (*byte)(unsafe.Pointer(&limits)), unsafe.Sizeof(limits), nil); err == nil {
				dpi, _, _ := user32.NewProc("GetDpiForWindow").Call(hwnd)
				if dpi == 0 {
					dpi = 96
				}
				limits[6], limits[7] = max(limits[6], int32(660*dpi/96)), max(limits[7], int32(320*dpi/96))
				_ = windows.WriteProcessMemory(windows.CurrentProcess(), lparam, (*byte)(unsafe.Pointer(&limits)), unsafe.Sizeof(limits), nil)
			}
		}
		return 0
	case 0x2e0:
		var rect [4]int32
		if v != nil && v.toolbar != nil {
			v.toolbar.painter.Font(wparam & 0xffff)
		}
		if err := windows.ReadProcessMemory(windows.CurrentProcess(), lparam, (*byte)(unsafe.Pointer(&rect)), unsafe.Sizeof(rect), nil); err == nil {
			user32.NewProc("SetWindowPos").Call(hwnd, 0, uintptr(rect[0]), uintptr(rect[1]), uintptr(rect[2]-rect[0]), uintptr(rect[3]-rect[1]), 0x14)
		}
		return 0
	case 0x111:
		if v != nil && v.toolbar != nil && v.toolbar.prompt != promptNone && wparam == 2 && lparam == 0 {
			v.finishPrompt(false)
			return 0
		}
		if v != nil && v.toolbarCommand(wparam&0xffff, lparam) {
			return 0
		}
		if v != nil && v.headerClose != 0 && lparam == v.headerClose && wparam == dismissHeaderCommand {
			v.dismissHeader()
			return 0
		}
	case 0x112:
		if v != nil && (v.wayland || v.host != nil && v.linux) && wparam&0xfff0 == forgetLoginCommand {
			v.forgetLogin()
			return 0
		}
	case 0x10:
		if v != nil {
			v.close()
		}
		user32.NewProc("DestroyWindow").Call(hwnd)
		return 0
	case 2:
		user32.NewProc("PostQuitMessage").Call(0)
		return 0
	}
	result, _, _ := user32.NewProc("DefWindowProcW").Call(hwnd, uintptr(msg), wparam, lparam)
	return result
})

func Run(args []string, log io.Writer) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	flags := flag.NewFlagSet("console", flag.ContinueOnError)
	v := &viewer{log: log}
	var x11 bool
	var probe, displayProbe, readinessProbe string
	current = v
	defer func() { current = nil }()
	flags.StringVar(&v.pipe, "pipe", "", "")
	flags.StringVar(&v.vmid, "vmid", "", "")
	flags.StringVar(&v.machineID, "machine-id", "", "")
	flags.StringVar(&v.title, "title", "虚拟机", "")
	flags.BoolVar(&v.enhanced, "enhanced", false, "")
	flags.BoolVar(&v.clipboard, "clipboard", false, "")
	flags.BoolVar(&v.linux, "linux", false, "")
	flags.BoolVar(&v.wayland, "wayland", false, "")
	flags.BoolVar(&x11, "x11", false, "")
	flags.StringVar(&displayProbe, "detect-display", "", "")
	flags.StringVar(&readinessProbe, "detect-ready", "", "")
	flags.BoolVar(&v.automatic, "auto", false, "")
	flags.BoolVar(&v.rendererMode, "renderer", false, "")
	flags.Uint64Var(&v.renderGeneration, "render-generation", 0, "")
	flags.StringVar(&v.renderToken, "render-token", "", "")
	var parent, parentPID uint64
	flags.Uint64Var(&parent, "parent", 0, "")
	flags.Uint64Var(&parentPID, "parent-pid", 0, "")
	flags.StringVar(&probe, "detect-desktop", "", "")
	if err := flags.Parse(args); err != nil {
		return err
	}
	v.parent, v.parentPID = uintptr(parent), uintptr(parentPID)
	if v.rendererMode {
		if parent == 0 || parentPID == 0 || !v.validParent() || v.automatic {
			return fmt.Errorf("控制台渲染参数无效")
		}
		session, err := renderhost.Open(os.Stdin, os.Stdout, v.renderToken, v.renderGeneration)
		if err != nil {
			return err
		}
		v.renderer = session
	} else if parent != 0 || parentPID != 0 || v.renderToken != "" || v.renderGeneration != 0 {
		return fmt.Errorf("控制台渲染参数无效")
	}
	if readinessProbe != "" && probe == "" && displayProbe == "" && flags.NArg() == 1 && v.pipe == "" && !v.enhanced && !v.automatic && !v.linux && !v.wayland && !x11 {
		kind := flags.Arg(0)
		if base := strings.TrimSuffix(kind, "-status"); base != "windows" && base != "windows-agent" && base != "linux" && base != "wayland" && base != "x11" {
			return fmt.Errorf("控制台会话参数无效")
		}
		fmt.Fprintln(os.Stdout, queryEnhancedAvailability(readinessProbe, kind))
		return nil
	}
	if probe != "" && displayProbe == "" && readinessProbe == "" && !v.automatic && flags.NArg() == 0 && v.pipe == "" && !v.enhanced && !v.linux && !v.wayland && !x11 {
		kind, err := queryLinuxSession(probe)
		if err == nil {
			fmt.Fprintln(os.Stdout, kind)
		}
		return err
	}
	if displayProbe != "" && probe == "" && readinessProbe == "" && flags.NArg() == 1 && v.pipe == "" && !v.enhanced && !v.automatic && !v.linux && !v.wayland && !x11 {
		kind := flags.Arg(0)
		if kind != "windows" && kind != "linux" && kind != "wayland" && kind != "x11" && kind != "wayland-login" {
			return fmt.Errorf("控制台会话参数无效")
		}
		capability, err := queryDisplay(displayProbe, kind)
		defer clear(capability.Password)
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(capability)
		}
		return err
	}
	if flags.NArg() != 0 || readinessProbe != "" || displayProbe != "" || probe != "" || v.automatic && v.enhanced || (v.wayland || x11) && !v.linux || v.wayland && x11 {
		return fmt.Errorf("控制台会话参数无效")
	}
	if !strings.HasPrefix(v.pipe, `\\.\pipe\my-vm-`) {
		return fmt.Errorf("控制台仅接受 My VM 本地管道")
	}
	if v.wayland {
		v.linuxMode = "wayland"
	} else if x11 {
		v.linuxMode = "x11"
	}
	if v.machineID == "" {
		v.machineID = machineIDFromPipe(v.pipe)
	}
	v.enhanced = renderhost.InitialEnhanced(v.automatic, v.enhanced)
	if !v.rendererMode {
		v.wayland = false
	}
	if v.rendererMode && v.enhanced && v.linux {
		kind := v.linuxMode
		if kind == "" {
			kind = "linux"
		}
		capability := probeDisplay(v.vmid, kind, log)
		if capability.Mode != "wayland" {
			v.connectionFailed(fmt.Errorf("客户机增强画面尚未就绪，仅支持现有 GNOME/KDE Wayland 桌面，保留当前画面"))
			return nil
		}
		v.wayland = capability.Mode == "wayland"
		v.dynamicResolution = capability.DynamicResolution
		v.automaticLogin = capability.login()
		defer v.automaticLogin.clear()
	}
	if !v.enhanced {
		v.wayland = false
	}
	if v.rendererMode && v.wayland {
		port, stop, err := linuxWaylandBridge(v.vmid, log)
		if err != nil {
			return err
		}
		v.rdpPort = port
		v.stopBridge = stop
		defer stop()
	} else if v.rendererMode && v.enhanced && v.linux {
		v.pipe += fmt.Sprintf(".linux-%d", os.Getpid())
		stop, err := linuxBridge(v.pipe, v.vmid, log)
		if err != nil {
			return err
		}
		v.stopBridge = stop
		defer stop()
	}
	if v.rendererMode {
		ole32 := windows.NewLazySystemDLL("ole32.dll")
		hr, _, _ := ole32.NewProc("OleInitialize").Call(0)
		if int32(hr) < 0 {
			v.connectionFailed(fmt.Errorf("初始化 OLE 失败: 0x%08X", uint32(hr)))
			return nil
		}
		defer ole32.NewProc("OleUninitialize").Call()
	}
	user32.NewProc("SetProcessDpiAwarenessContext").Call(^uintptr(3))
	class := windowClass{Size: uint32(unsafe.Sizeof(windowClass{})), Procedure: procedure, Background: 6, Name: windows.StringToUTF16Ptr("MyVM.HCS.Console")}
	class.Instance, _, _ = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	class.Icon, _, _ = user32.NewProc("LoadIconW").Call(class.Instance, 1)
	class.SmallIcon = class.Icon
	class.Cursor, _, _ = user32.NewProc("LoadCursorW").Call(0, 32512)
	atom, _, _ := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return fmt.Errorf("注册控制台窗口失败")
	}
	mode := "基础控制台"
	if v.enhanced {
		mode = "增强会话"
	}
	style, extendedStyle := uintptr(0x02cf0000), uintptr(0)
	width, height := uintptr(1400), uintptr(900)
	if v.rendererMode {
		// Independent popup and input queue: the host is only an authenticated IPC destination.
		style, extendedStyle, width, height = 0x86000000, 0x80, 1, 1
	}
	v.hwnd, _, _ = user32.NewProc("CreateWindowExW").Call(extendedStyle, uintptr(unsafe.Pointer(class.Name)), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("VM · "+v.title+" · "+mode))), style, 100, 100, width, height, 0, 0, 0, 0)
	if v.hwnd == 0 {
		return fmt.Errorf("创建控制台窗口失败")
	}
	if !v.rendererMode {
		v.header, _, _ = user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("STATIC"))), 0, 0x5000000d, 12, 10, 1300, 32, v.hwnd, 0, 0, 0)
		v.headerClose, _, _ = user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("BUTTON"))), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("关闭提示"))), 0x5001000b, 0, 0, 24, 24, v.hwnd, dismissHeaderCommand, 0, 0)
		if v.header == 0 || v.headerClose == 0 {
			return fmt.Errorf("创建控制台提示栏失败")
		}
		if err := v.createToolbar(); err != nil {
			return err
		}
		defer v.toolbar.painter.Close()
		defer v.toolbar.cancel()
	}
	defer func() {
		v.close()
		if v.host != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
			defer cancel()
			v.host.Wait(ctx)
		}
	}()
	v.setStatus(mode + " · 正在连接客户机…")
	if v.wayland {
		v.setStatus("正在连接 Linux 桌面 · 请完成凭据和证书确认，窗口将保持打开")
	}
	hook, _, hookErr := user32.NewProc("SetWinEventHook").Call(3, 3, 0, rendererForegroundCallback, 0, 0, 0)
	v.foregroundHook = hook
	if hook == 0 {
		return fmt.Errorf("监听控制台前台切换失败: %w", hookErr)
	}
	if !v.rendererMode {
		user32.NewProc("ShowWindow").Call(v.hwnd, 5)
		if v.linux {
			v.addLoginMenu()
		}
		v.startRenderHost()
		v.startGuestStatus()
	} else {
		// A visible, tiny renderer owns authentication UI without covering the working basic view.
		user32.NewProc("ShowWindow").Call(v.hwnd, 4)
		v.renderer.Report("window", v.hwnd)
		v.renderer.Listen(func() { user32.NewProc("PostMessageW").Call(v.hwnd, 0x8005, 0, 0) })
		if err := v.initialize(); err != nil {
			v.connectionFailed(err)
		}
	}
	user32.NewProc("SetTimer").Call(v.hwnd, 1, 300, 0)
	var msg message
	for {
		ok, _, _ := user32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ok) == -1 {
			return fmt.Errorf("控制台消息循环失败")
		}
		if ok == 0 {
			break
		}
		if msg.Message == 0x200 || msg.Message == 0xa0 || msg.Message == 0x2a3 {
			v.syncPointerFocus(false)
		}
		guestInput := uintptr(0)
		if v.rendererMode {
			guestInput, _, _ = user32.NewProc("IsChild").Call(v.child, msg.Window)
		}
		if v.rendererMode && (msg.Window == v.child || guestInput != 0) && ((msg.Message >= 0x100 && msg.Message <= 0x109) || (msg.Message >= 0x200 && msg.Message <= 0x20e)) {
			handled, _, _ := user32.NewProc("SendMessageW").Call(v.child, 0x37f, 0, uintptr(unsafe.Pointer(&msg)))
			if handled != 0 {
				continue
			}
		}
		if v.host != nil && v.toolbarNavigation(msg) {
			continue
		}
		if v.host == nil && guestInput == 0 && msg.Window != v.child && msg.Message >= 0x100 && msg.Message <= 0x109 {
			handled, _, _ := user32.NewProc("IsDialogMessageW").Call(v.hwnd, uintptr(unsafe.Pointer(&msg)))
			if handled != 0 {
				continue
			}
		}
		user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
	}
	return nil
}
func (v *viewer) initialize() error {
	initialized, _, _ := atl.NewProc("AtlAxWinInit").Call()
	if initialized == 0 {
		return fmt.Errorf("AtlAxWinInit 失败")
	}
	v.child, _, _ = user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("AtlAxWin"))), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("{A0C63C30-F08D-4AB4-907C-34905D770C7D}"))), 0x56000000, 0, 60, 640, 420, v.hwnd, 0, 0, 0)
	if v.child == 0 {
		return fmt.Errorf("创建 RDP ActiveX 窗口失败")
	}
	var unknown *ole.IUnknown
	hr, _, _ := atl.NewProc("AtlAxGetControl").Call(v.child, uintptr(unsafe.Pointer(&unknown)))
	if int32(hr) < 0 || unknown == nil {
		return fmt.Errorf("取得 RDP ActiveX 失败: 0x%08X", uint32(hr))
	}
	defer unknown.Release()
	var err error
	v.client, err = unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	if v.enhanced {
		if err = v.client.PutQueryInterface(ole.NewGUID("{28904001-04B6-436C-A55B-0AF1A0883DC9}"), &v.displayClient); err != nil {
			return err
		}
	}
	if err = v.initializeEvents(); err != nil {
		return err
	}
	v.advanced, err = property(v.client, "AdvancedSettings9")
	if err != nil {
		return err
	}
	auth := 0
	if v.wayland {
		auth = 2
	}
	for _, setting := range []struct {
		name  string
		value any
	}{{"RelativeMouseMode", !v.wayland}, {"EnableAutoReconnect", false}, {"AuthenticationLevel", auth}, {"EnableCredSspSupport", v.wayland}, {"NegotiateSecurityLayer", true}, {"RedirectClipboard", v.enhanced && v.clipboard}, {"RedirectDrives", false}, {"SmartSizing", !v.wayland || !v.dynamicResolution}} {
		if err = put(v.advanced, setting.name, setting.value); err != nil {
			return err
		}
	}
	server := v.vmid
	if v.wayland {
		if !v.dynamicResolution {
			if err = extended(v.client, "EnableZoom", ole.VARIANT{VT: ole.VT_BOOL, Val: -1}); err != nil {
				return fmt.Errorf("启用 Linux 桌面画面缩放: %w", err)
			}
		}
		server = "127.0.0.1"
		if err = put(v.advanced, "RDPPort", v.rdpPort); err != nil {
			return err
		}
	}
	if err = put(v.client, "Server", server); err != nil {
		return err
	}
	initial := v.initialDesktop()
	if err = put(v.client, "DesktopWidth", initial.width); err != nil {
		return err
	}
	if err = put(v.client, "DesktopHeight", initial.height); err != nil {
		return err
	}
	if v.wayland && v.dynamicResolution {
		if err = extended(v.client, "DesktopScaleFactor", ole.VARIANT{VT: ole.VT_UI4, Val: int64(min(initial.dpi*100/96, 500))}); err != nil {
			return fmt.Errorf("设置 Linux 桌面缩放比例: %w", err)
		}
	}
	_ = extended(v.client, "AllowAxToContainerEvents", ole.VARIANT{VT: ole.VT_BOOL, Val: -1})
	_ = extended(v.client, "EnableHardwareMode", ole.VARIANT{VT: ole.VT_BOOL, Val: -1})
	if !v.wayland {
		_ = extended(v.client, "EnableFrameBufferRedirection", ole.VARIANT{VT: ole.VT_BOOL, Val: -1})
	}
	if !v.wayland {
		if err = v.initializePipe(); err != nil {
			return err
		}
	}
	if err = v.client.PutQueryInterface(ole.NewGUID("{71B4A60A-FE21-46D8-A39B-8E32BA0C5ECC}"), &v.nonScriptable); err != nil {
		return err
	}
	if err = call(v.nonScriptable, 15, v.owner()); err != nil {
		return err
	}
	if err = call(v.nonScriptable, 39, 0xffff); err != nil {
		return err
	}
	if v.wayland {
		if err = v.configureLogin(); err != nil {
			return err
		}
		if err = call(v.nonScriptable, 63, 0); err != nil {
			return err
		}
		if err = call(v.nonScriptable, 47, 0); err != nil {
			return err
		}
	}
	if err = v.configureInput(); err != nil {
		return err
	}
	if err = v.configureClipboard(); err != nil {
		return err
	}
	_ = extended(v.client, "AllowRelativeMouseMode", ole.VARIANT{VT: ole.VT_BOOL, Val: -1})
	v.resize()
	if v.wayland {
		result, err := oleutil.CallMethod(v.client, "Connect")
		if result != nil {
			_ = result.Clear()
		}
		return err
	}
	return call(v.connector, 5, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(v.pipe))))
}
func (v *viewer) resize() {
	if v.host != nil {
		v.resizeRenderer()
		return
	}
	if v.closed || v.child == 0 {
		return
	}
	var rect struct{ Left, Top, Right, Bottom int32 }
	user32.NewProc("GetClientRect").Call(v.hwnd, uintptr(unsafe.Pointer(&rect)))
	dpi, _, _ := user32.NewProc("GetDpiForWindow").Call(v.hwnd)
	if dpi == 0 {
		dpi = 96
	}
	top, settings := v.contentLayout(rect.Right, rect.Bottom, dpi)
	width, height := settings.width, settings.height
	if v.client == nil {
		return
	}
	result, err := oleutil.GetProperty(v.client, "Connected")
	connected := err == nil && result.Val == 1
	if result != nil {
		_ = result.Clear()
	}
	if connected && v.renderer != nil && !v.reportedConnected {
		v.reportedConnected = true
		v.renderer.Report("connected", v.hwnd)
	}
	if width < 200 || height < 200 {
		return
	}
	if v.layout != settings {
		barHeight := v.toolbarHeight(dpi)
		v.layoutToolbar(dpi)
		v.layoutNotice(width, top, barHeight, dpi)
		moved, _, moveErr := user32.NewProc("MoveWindow").Call(v.child, 0, uintptr(top), uintptr(width), uintptr(height), 1)
		if moved == 0 {
			v.connectionFailed(fmt.Errorf("调整客户机画面布局失败: %w", moveErr))
			return
		}
		v.layout = settings
	}
	if connected && !v.display.connected {
		v.setStatus(connectedNotice)
	}
	if !v.enhanced || v.wayland && !v.dynamicResolution {
		v.display.connected = connected
		return
	}
	if connected && !v.loggedIn && !v.wayland {
		v.display.connected = true
		v.setStatus("已连接 · 登录客户机后自动调整分辨率")
		return
	}
	sent, err := v.display.update(connected, settings, time.Now(), func(s displaySettings) error {
		scale := min(s.dpi*100/96, 500)
		return call(v.displayClient, 69, uintptr(s.width), uintptr(s.height), uintptr(s.width)*254/(s.dpi*10), uintptr(s.height)*254/(s.dpi*10), 0, scale, 100)
	})
	if err != nil {
		if err.Error() != v.displayError {
			v.fail(fmt.Errorf("调整增强会话分辨率失败: %w", err))
			v.displayError = err.Error()
		}
	} else if v.display.waiting {
		v.displayError = ""
		v.setStatus("已连接 · 客户机显示尚未就绪，稍后自动调整分辨率")
	} else if sent || v.display.applied == settings {
		v.displayError = ""
		v.setStatus(connectedNotice)
	}
}
func (v *viewer) close() {
	if v.closed {
		return
	}
	v.closed = true
	v.generation++
	if v.foregroundHook != 0 {
		user32.NewProc("UnhookWinEvent").Call(v.foregroundHook)
		v.foregroundHook = 0
	}
	if v.host != nil {
		if v.toolbar != nil {
			v.toolbar.cancel()
		}
		v.host.Close()
		return
	}
	if v.stopBridge != nil {
		v.stopBridge()
		v.stopBridge = nil
	}
	v.automaticLogin.clear()
	v.pendingLogin.clear()
	v.pendingLogin = nil
	user32.NewProc("KillTimer").Call(v.hwnd, 1)
	v.closeEvents()
	if v.client != nil {
		_, _ = oleutil.CallMethod(v.client, "Disconnect")
	}
	if v.connector != nil {
		_ = call(v.connector, 4)
		v.connector.Release()
		v.connector = nil
	}
	if v.child != 0 {
		user32.NewProc("DestroyWindow").Call(v.child)
		v.child = 0
	}
	if v.nonScriptable != nil {
		v.nonScriptable.Release()
		v.nonScriptable = nil
	}
	if v.displayClient != nil {
		v.displayClient.Release()
		v.displayClient = nil
	}
	if v.advanced != nil {
		v.advanced.Release()
		v.advanced = nil
	}
	if v.client != nil {
		v.client.Release()
		v.client = nil
	}
	if v.context != nil {
		_ = call(v.context, 13)
		v.context.Release()
		v.context = nil
	}
	if v.callback != nil {
		releaseCallback(v.callback, &v.callback.refs)
		v.callback = nil
	}
}
