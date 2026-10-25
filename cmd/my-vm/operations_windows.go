//go:build windows

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/egoist/mygo"
	vm "my-vm/library"
)

func (a *application) action(request vm.Request) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	_, err := a.manager.Execute(ctx, request)
	return err
}

func (a *application) refresh() (vm.Config, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg, err := a.manager.Configuration(ctx)
	if err == nil {
		err = a.manager.Refresh(ctx)
	}
	return cfg, err
}

func (a *application) beginOperation(id, label string) bool {
	if a.busy || a.loadError != nil || a.diskBusy(id) || (id != "host-network-change" && a.diskBusy("host-network-change")) {
		return false
	}
	if a.operations == nil {
		a.operations = map[string]string{}
	}
	a.operations[id] = label
	a.message, a.messageError = label+"…", false
	return true
}

func (a *application) finishOperation(id, label string, err error, done func()) {
	delete(a.operations, id)
	if err != nil {
		a.report(fmt.Errorf("%s: %w", label, err))
	} else {
		a.message, a.messageError = label+"完成", false
		if done != nil {
			done()
		}
	}
	if a.window != nil {
		a.window.Invalidate()
	}
}

func (a *application) run(id, label string, work func() error, done func()) {
	if !a.beginOperation(id, label) {
		return
	}
	go func() {
		err := work()
		cfg, refreshError := a.refresh()
		if err == nil && refreshError != nil {
			err = fmt.Errorf("操作已完成，但刷新失败，请刷新后核对: %w", refreshError)
		}
		mygo.RunOnMain(func() {
			if refreshError == nil {
				a.config = cfg
			}
			a.finishOperation(id, label, err, done)
		})
	}()
}
