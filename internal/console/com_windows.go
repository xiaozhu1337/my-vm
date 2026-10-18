//go:build windows

package console

import (
	"fmt"
	"sync/atomic"
	"syscall"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
)

//go:uintptrescapes
func call(object *ole.IUnknown, slot int, args ...uintptr) error {
	table := (*[160]uintptr)(unsafe.Pointer(object.RawVTable))
	params := append([]uintptr{uintptr(unsafe.Pointer(object))}, args...)
	hr, _, _ := syscall.SyscallN(table[slot], params...)
	if int32(hr) < 0 {
		return fmt.Errorf("COM slot %d: HRESULT 0x%08X", slot, uint32(hr))
	}
	return nil
}
func createRuntime(context *ole.IUnknown, class, iface string) (*ole.IUnknown, error) {
	var result *ole.IUnknown
	hr, _, _ := windows.NewLazySystemDLL("RdpBase.dll").NewProc("RDPBASE_CreateInstance").Call(uintptr(unsafe.Pointer(context)), uintptr(unsafe.Pointer(ole.NewGUID(class))), uintptr(unsafe.Pointer(ole.NewGUID(iface))), uintptr(unsafe.Pointer(&result)))
	if int32(hr) < 0 {
		return nil, fmt.Errorf("RDPBASE_CreateInstance: HRESULT 0x%08X", uint32(hr))
	}
	return result, nil
}
func put(object *ole.IDispatch, name string, value any) error {
	result, err := oleutil.PutProperty(object, name, value)
	if result != nil {
		_ = result.Clear()
	}
	if err != nil {
		return fmt.Errorf("RDP %s: %w", name, err)
	}
	return nil
}
func property(object *ole.IDispatch, name string) (*ole.IDispatch, error) {
	result, err := oleutil.GetProperty(object, name)
	if err != nil {
		return nil, err
	}
	p := result.ToIDispatch()
	if p == nil {
		_ = result.Clear()
		return nil, fmt.Errorf("RDP 属性 %s 无效", name)
	}
	p.AddRef()
	_ = result.Clear()
	return p, nil
}
func extended(client *ole.IDispatch, name string, value ole.VARIANT) error {
	var settings *ole.IUnknown
	if err := client.PutQueryInterface(ole.NewGUID("{302D8188-0052-4807-806A-362B628F9AC5}"), &settings); err != nil {
		return err
	}
	defer settings.Release()
	label := ole.SysAllocString(name)
	defer ole.SysFreeString(label)
	return call(settings, 3, uintptr(unsafe.Pointer(label)), uintptr(unsafe.Pointer(&value)))
}

type connectorCallback struct {
	table  *[5]uintptr
	refs   atomic.Int32
	viewer *viewer
}

var connectorIID = ole.NewGUID("{D923EFE9-0A6D-4344-92B3-164229DB8D2D}")
var callbackTable = [5]uintptr{
	syscall.NewCallback(func(this *connectorCallback, iid *ole.GUID, out **connectorCallback) uintptr {
		*out = nil
		if ole.IsEqualGUID(iid, ole.IID_IUnknown) || ole.IsEqualGUID(iid, connectorIID) {
			*out = this
			return uintptr(this.refs.Add(1)) & 0
		}
		return 0x80004002
	}),
	syscall.NewCallback(func(this *connectorCallback) uintptr { return uintptr(this.refs.Add(1)) }),
	syscall.NewCallback(func(this *connectorCallback) uintptr { return uintptr(this.refs.Add(-1)) }),
	syscall.NewCallback(func(this *connectorCallback, stream *ole.IUnknown) uintptr {
		v := this.viewer
		if v.closed {
			return 0
		}
		endpoint := ole.VARIANT{VT: ole.VT_UNKNOWN, Val: int64(uintptr(unsafe.Pointer(stream)))}
		err := call(&v.advanced.IUnknown, 135, uintptr(unsafe.Pointer(&endpoint)))
		if err == nil {
			_, err = oleutil.CallMethod(v.client, "Connect")
		}
		if err == nil {
			user32.NewProc("SetFocus").Call(v.child)
		}
		if err != nil {
			v.fail(err)
		}
		return 0
	}),
	syscall.NewCallback(func(this *connectorCallback, hr uintptr) uintptr {
		if !this.viewer.closed {
			this.viewer.fail(fmt.Errorf("控制台管道连接失败: HRESULT 0x%08X", uint32(hr)))
		}
		return 0
	}),
}
