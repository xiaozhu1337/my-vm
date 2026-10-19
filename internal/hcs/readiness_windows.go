//go:build windows

package hcs

import (
	"fmt"
	"runtime"

	"github.com/Microsoft/go-winio/pkg/guid"
	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// EnhancedSessionState is optional: some HCS guests are not registered with VMMS WMI.
// Run it in a bounded helper process because a WMI provider call may block.
func EnhancedSessionState(runtimeID string) (known, available bool) {
	id, err := guid.FromString(runtimeID)
	if err != nil {
		return false, false
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err = ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		return false, false
	}
	defer ole.CoUninitialize()
	unknown, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		return false, false
	}
	defer unknown.Release()
	locator, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return false, false
	}
	defer locator.Release()
	service, err := oleutil.CallMethod(locator, "ConnectServer", ".", `root\virtualization\v2`)
	if err != nil || service == nil {
		return false, false
	}
	defer service.Clear()
	query := fmt.Sprintf("SELECT EnhancedSessionModeState FROM Msvm_ComputerSystem WHERE Name = '%s'", id.String())
	rows, err := oleutil.CallMethod(service.ToIDispatch(), "ExecQuery", query, "WQL", 48)
	if err != nil || rows == nil {
		return false, false
	}
	defer rows.Clear()
	err = oleutil.ForEach(rows.ToIDispatch(), func(row *ole.VARIANT) error {
		defer row.Clear()
		state, err := oleutil.GetProperty(row.ToIDispatch(), "EnhancedSessionModeState")
		if err != nil {
			return err
		}
		defer state.Clear()
		if state.VT == ole.VT_I4 || state.VT == ole.VT_UI2 || state.VT == ole.VT_UI4 || state.VT == ole.VT_I2 {
			switch state.Val {
			case 2, 3, 6:
				known, available = true, state.Val == 2
			}
		}
		return nil
	})
	if err != nil {
		return false, false
	}
	return known, available
}
