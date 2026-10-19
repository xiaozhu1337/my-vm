//go:build windows

package console

import (
	"flag"
	"fmt"
	"io"
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

type viewer struct {
	hwnd, header, child                         uintptr
	client, advanced                            *ole.IDispatch
	context, connector, nonScriptable           *ole.IUnknown
	callback                                    *connectorCallback
	enhanced, clipboard, linux, wayland, closed bool
	rdpPort                                     int
	pipe, vmid, title, status                   string
	log                                         io.Writer
	layout                                      displaySettings
	display                                     displayUpdate
	displayError                                string
}

var current *viewer

func (v *viewer) fail(err error) { fmt.Fprintln(v.log, err); v.setStatus(err.Error()) }
func (v *viewer) setStatus(status string) {
	if status == v.status {
		return
	}
	v.status = status
	if v.header != 0 {
		user32.NewProc("SetWindowTextW").Call(v.header, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(status))))
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
	case 0x8001:
		if v != nil && v.nonScriptable != nil {
			up := [2]int16{0, -1}
			keys := [2]uint32{uint32(wparam) | 1, uint32(wparam) | 0xC0000001}
			if err := call(v.nonScriptable, 14, 2, uintptr(unsafe.Pointer(&up[0])), uintptr(unsafe.Pointer(&keys[0]))); err != nil {
				v.fail(err)
			}
		}
		return 0
	case 5, 0x113:
		if v != nil && v.hwnd != 0 {
			v.resize()
		}
		return 0
	case 0x7:
		if v != nil && v.child != 0 {
			user32.NewProc("SetFocus").Call(v.child)
		}
		return 0
	case 0x2e0:
		var rect [4]int32
		if err := windows.ReadProcessMemory(windows.CurrentProcess(), lparam, (*byte)(unsafe.Pointer(&rect)), unsafe.Sizeof(rect), nil); err == nil {
			user32.NewProc("SetWindowPos").Call(hwnd, 0, uintptr(rect[0]), uintptr(rect[1]), uintptr(rect[2]-rect[0]), uintptr(rect[3]-rect[1]), 0x14)
		}
		return 0
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
	current = v
	defer func() { current = nil }()
	flags.StringVar(&v.pipe, "pipe", "", "")
	flags.StringVar(&v.vmid, "vmid", "", "")
	flags.StringVar(&v.title, "title", "虚拟机", "")
	flags.BoolVar(&v.enhanced, "enhanced", false, "")
	flags.BoolVar(&v.clipboard, "clipboard", false, "")
	flags.BoolVar(&v.linux, "linux", false, "")
	flags.BoolVar(&v.wayland, "wayland", false, "")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || v.wayland && (!v.enhanced || !v.linux) {
		return fmt.Errorf("控制台会话参数无效")
	}
	if !strings.HasPrefix(v.pipe, `\\.\pipe\my-vm-`) {
		return fmt.Errorf("控制台仅接受 My VM 本地管道")
	}
	if v.wayland {
		port, stop, err := linuxWaylandBridge(v.vmid, log)
		if err != nil {
			return err
		}
		v.rdpPort = port
		defer stop()
	} else if v.enhanced && v.linux {
		v.pipe += fmt.Sprintf(".linux-%d", os.Getpid())
		stop, err := linuxBridge(v.pipe, v.vmid, log)
		if err != nil {
			return err
		}
		defer stop()
	}
	ole32 := windows.NewLazySystemDLL("ole32.dll")
	hr, _, _ := ole32.NewProc("OleInitialize").Call(0)
	if int32(hr) < 0 {
		return fmt.Errorf("初始化 OLE 失败: 0x%08X", uint32(hr))
	}
	defer ole32.NewProc("OleUninitialize").Call()
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
	v.hwnd, _, _ = user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class.Name)), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("VM · "+v.title+" · "+mode))), 0x02cf0000, 100, 100, 1400, 900, 0, 0, 0, 0)
	if v.hwnd == 0 {
		return fmt.Errorf("创建控制台窗口失败")
	}
	v.header, _, _ = user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("STATIC"))), 0, 0x50000000, 12, 10, 1300, 32, v.hwnd, 0, 0, 0)
	font, _, _ := windows.NewLazySystemDLL("gdi32.dll").NewProc("GetStockObject").Call(17)
	user32.NewProc("SendMessageW").Call(v.header, 0x30, font, 1)
	defer v.close()
	if err := v.initialize(); err != nil {
		v.fail(err)
	} else {
		v.setStatus(mode + " · 正在连接客户机…")
	}
	user32.NewProc("ShowWindow").Call(v.hwnd, 5)
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
		if (msg.Message >= 0x100 && msg.Message <= 0x109) || (msg.Message >= 0x200 && msg.Message <= 0x20e) {
			handled, _, _ := user32.NewProc("SendMessageW").Call(v.child, 0x37f, 0, uintptr(unsafe.Pointer(&msg)))
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
	}{{"RelativeMouseMode", !v.wayland}, {"EnableAutoReconnect", false}, {"AuthenticationLevel", auth}, {"EnableCredSspSupport", v.wayland}, {"NegotiateSecurityLayer", true}, {"RedirectClipboard", v.clipboard}, {"RedirectDrives", false}, {"SmartSizing", true}} {
		if err = put(v.advanced, setting.name, setting.value); err != nil {
			return err
		}
	}
	server := v.vmid
	if v.wayland {
		server = "127.0.0.1"
		if err = put(v.advanced, "RDPPort", v.rdpPort); err != nil {
			return err
		}
	}
	if err = put(v.client, "Server", server); err != nil {
		return err
	}
	_ = put(v.client, "DesktopWidth", 1280)
	_ = put(v.client, "DesktopHeight", 720)
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
	if err = call(v.nonScriptable, 15, v.hwnd); err != nil {
		return err
	}
	if err = call(v.nonScriptable, 39, 0xffff); err != nil {
		return err
	}
	if v.wayland {
		if err = call(v.nonScriptable, 63, 0xffff); err != nil {
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
	if v.closed || v.child == 0 {
		return
	}
	var rect struct{ Left, Top, Right, Bottom int32 }
	user32.NewProc("GetClientRect").Call(v.hwnd, uintptr(unsafe.Pointer(&rect)))
	dpi, _, _ := user32.NewProc("GetDpiForWindow").Call(v.hwnd)
	if dpi == 0 {
		dpi = 96
	}
	top := int32(32 * dpi / 96)
	width, height := rect.Right, rect.Bottom-top
	if width < 200 || height < 200 {
		return
	}
	settings := displaySettings{width: width, height: height, dpi: dpi}
	if v.layout != settings {
		user32.NewProc("MoveWindow").Call(v.header, 12, 12, uintptr(width-24), uintptr(top-12), 1)
		user32.NewProc("MoveWindow").Call(v.child, 0, uintptr(top), uintptr(width), uintptr(height), 1)
		v.layout = settings
	}
	if v.client == nil {
		return
	}
	result, err := oleutil.GetProperty(v.client, "Connected")
	connected := err == nil && result.Val == 1
	if result != nil {
		_ = result.Clear()
	}
	if connected && !v.display.connected {
		v.setStatus("已连接 · 关闭控制台后虚拟机继续运行")
	}
	if !v.enhanced {
		v.display.connected = connected
		return
	}
	sent, err := v.display.update(connected, settings, time.Now(), func(s displaySettings) error {
		scale := min(s.dpi*100/96, 500)
		return call(&v.client.IUnknown, 69, uintptr(s.width), uintptr(s.height), uintptr(s.width)*254/(s.dpi*10), uintptr(s.height)*254/(s.dpi*10), 0, scale, 100)
	})
	if err != nil {
		if err.Error() != v.displayError {
			v.fail(fmt.Errorf("调整增强会话分辨率失败: %w", err))
			v.displayError = err.Error()
		}
	} else if sent {
		v.displayError = ""
		v.setStatus("已连接 · 关闭控制台后虚拟机继续运行")
	}
}
func (v *viewer) close() {
	if v.closed {
		return
	}
	v.closed = true
	user32.NewProc("KillTimer").Call(v.hwnd, 1)
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
}
