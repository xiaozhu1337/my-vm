//go:build windows

package console

import (
	"fmt"
	"sync/atomic"
	"syscall"
	"unsafe"

	ole "github.com/go-ole/go-ole"
)

var sessionEventIID = ole.NewGUID("{336D5562-EFA8-482E-8CB3-C5C0FC7A7DB6}")

type sessionEvents struct {
	table  *[7]uintptr
	refs   atomic.Int32
	viewer *viewer
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
	syscall.NewCallback(func(this *sessionEvents) uintptr { return uintptr(this.refs.Add(-1)) }),
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
		if id == 15 && args != nil && args.count == 1 && args.values != nil && args.values.VT == ole.VT_BYREF|ole.VT_BOOL && args.values.Val != 0 {
			allow := *(**int16)(unsafe.Pointer(&args.values.Val))
			*allow = -1
		}
		this.viewer.sessionEvent(id)
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
	case 3, 33:
		v.loggedIn = true
		v.display = displayUpdate{}
	}
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
	sink := &sessionEvents{table: &sessionEventTable, viewer: v}
	sink.refs.Store(1)
	cookie, err := point.Advise((*ole.IUnknown)(unsafe.Pointer(sink)))
	if err != nil {
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
}
