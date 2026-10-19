//go:build windows

package main

import (
	"errors"
	"github.com/egoist/mygo"
)

func (a *application) beginExternal(id, action string) error {
	var err error
	mygo.RunOnMain(func() {
		if a.loadError != nil {
			err = a.loadError
			return
		}
		if a.busy || a.externalID != "" || a.diskBusy(id) {
			err = errors.New("管理器正在操作此虚拟机或保存配置，请稍后重试")
			return
		}
		a.externalID = id
		a.messageError = false
		a.message = "命令行操作进行中：" + action
		if a.window != nil {
			a.window.Invalidate()
		}
	})
	return err
}
func (a *application) finishExternal() {
	mygo.RunOnMain(func() {
		if a.externalID == a.selected {
			a.resetSnapshots()
		}
		a.externalID = ""
		a.message = "命令行操作结束"
		if a.window != nil {
			a.window.Invalidate()
		}
	})
}
