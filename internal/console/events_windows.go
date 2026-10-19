//go:build windows

package console

import (
	"fmt"
	"sync/atomic"
	"syscall"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

var sessionEventIID = ole.NewGUID("{336D5562-EFA8-482E-8CB3-C5C0FC7A7DB6}")

type sessionEvents struct {
	table      *[7]uintptr
	refs       atomic.Int32
	viewer     *viewer
	generation uintptr
}

type eventArguments struct {
	values            *ole.VARIANT
	named             *int32
	count, namedCount uint32
}

var sessionEventTable = [7]uintptr{
	syscall.NewCallback(func(this *sessionEvents, iid *ole.GUID, out **sessionEvents) uintptr {
		if out == nil {
			return 0x80004003
		}
		*out = nil
		if ole.IsEqualGUID(iid, ole.IID_IUnknown) || ole.IsEqualGUID(iid, ole.IID_IDispatch) || ole.IsEqualGUID(iid, sessionEventIID) {
			*out = this
			this.refs.Add(1)
			return 0
		}
		return 0x80004002
	}),
	syscall.NewCallback(func(this *sessionEvents) uintptr { return uintptr(this.refs.Add(1)) }),
	syscall.NewCallback(func(this *sessionEvents) uintptr { return releaseCallback(this, &this.refs) }),
	syscall.NewCallback(func(_ *sessionEvents, count *uint32) uintptr {
		if count == nil {
			return 0x80004003
		}
		*count = 0
		return 0
	}),
	syscall.NewCallback(func(_ *sessionEvents, _, _ uint32, out *uintptr) uintptr {
		if out != nil {
			*out = 0
		}
		return 0x80004001
	}),
	syscall.NewCallback(func(_ *sessionEvents, _, _, _, _, _ uintptr) uintptr { return 0x80020006 }),
	syscall.NewCallback(func(this *sessionEvents, id int32, _ uintptr, _ uint32, _ uint16, args *eventArguments, _, _, _ uintptr) uintptr {
		if !this.viewer.activeSession(this.generation) {
			return 0
		}
		if id == 15 && args != nil && args.count == 1 && args.values != nil && args.values.VT == ole.VT_BYREF|ole.VT_BOOL && args.values.Val != 0 {
			allow := *(**int16)(unsafe.Pointer(&args.values.Val))
			*allow = -1
		}
		this.viewer.sessionEvent(id)
		if id == 4 {
			reason := int32(0)
			if args != nil && args.count == 1 && args.values != nil && args.values.VT == ole.VT_I4 {
				reason = int32(args.values.Val)
			}
			this.viewer.disconnected(reason)
			this.viewer.requestBasic()
		}
		return 0
	}),
}

func (v *viewer) sessionEvent(id int32) {
	if v.closed {
		return
	}
	switch id {
	case 1, 4, 17, 34:
		v.loggedIn = false
		v.display = displayUpdate{}
		if id == 1 && v.wayland {
			v.setStatus("正在连接 Linux 桌面 · 请完成凭据和证书确认，窗口将保持打开")
		}
	case 18:
		if v.wayland {
			v.setStatus("请核对并确认 Linux 远程桌面的证书 · 窗口将保持打开")
		}
	case 19:
		if v.wayland {
			v.setStatus("正在连接 Linux 桌面 · 请完成剩余的登录提示")
		}
	case 3, 33:
		v.loggedIn = true
		v.display = displayUpdate{}
		if id == 3 && v.wayland {
			if err := v.saveLogin(writeLogin); err != nil {
				v.fail(err)
			}
		}
	}
}

func (v *viewer) disconnected(reason int32) {
	extended := int32(0)
	if v.client != nil {
		if value, err := oleutil.GetProperty(v.client, "ExtendedDisconnectReason"); err == nil && value != nil {
			extended = int32(value.Val)
			_ = value.Clear()
		}
	}
	description := ""
	if v.client != nil {
		if value, err := oleutil.CallMethod(v.client, "GetErrorDescription", uint32(reason), uint32(extended)); err == nil && value != nil {
			description = value.ToString()
			_ = value.Clear()
		}
	}
	hint := ""
	if v.wayland {
		hint = " 若使用 GNOME，请先在基础控制台登录并解锁桌面；密码变更时，从窗口左上角菜单选择“忘记 Linux 登录凭据”，再重新打开增强会话。"
	}
	mode := "基础控制台"
	if v.enhanced {
		mode = "增强会话"
	}
	v.fail(fmt.Errorf("%s已断开（原因 %d，扩展 %d）: %s%s", mode, reason, extended, description, hint))
}

func (v *viewer) initializeEvents() error {
	unknown, err := v.client.QueryInterface(ole.IID_IConnectionPointContainer)
	if err != nil {
		return err
	}
	defer unknown.Release()
	container := (*ole.IConnectionPointContainer)(unsafe.Pointer(unknown))
	var point *ole.IConnectionPoint
	if err := container.FindConnectionPoint(sessionEventIID, &point); err != nil {
		return err
	}
	sink := &sessionEvents{table: &sessionEventTable, viewer: v, generation: v.generation}
	registerCallback(sink, &sink.refs)
	cookie, err := point.Advise((*ole.IUnknown)(unsafe.Pointer(sink)))
	if err != nil {
		releaseCallback(sink, &sink.refs)
		point.Release()
		return err
	}
	v.events, v.eventPoint, v.eventCookie = sink, point, cookie
	return nil
}

func (v *viewer) closeEvents() {
	if v.eventPoint != nil {
		if err := v.eventPoint.Unadvise(v.eventCookie); err != nil && v.log != nil {
			fmt.Fprintln(v.log, "解除 RDP 事件订阅:", err)
		}
		v.eventPoint.Release()
		v.eventPoint = nil
	}
	if v.events != nil {
		releaseCallback(v.events, &v.events.refs)
		v.events = nil
	}
}
