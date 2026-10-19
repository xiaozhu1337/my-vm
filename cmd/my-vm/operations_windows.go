//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/egoist/mygo"
	"my-vm/internal/vm"
)

type configurationSave struct {
	id, label string
	update    func(*vm.Config) error
	done      func()
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
	a.runConfig(id, label, work, nil, done)
}

func (a *application) runConfig(id, label string, work func() error, update func(*vm.Config) error, done func()) {
	if !a.beginOperation(id, label) {
		return
	}
	go func() {
		err := work()
		mygo.RunOnMain(func() {
			if err != nil || update == nil {
				a.finishOperation(id, label, err, done)
				return
			}
			a.queueConfiguration(configurationSave{id: id, label: label, update: update, done: done})
		})
	}()
}

func (a *application) queueConfiguration(save configurationSave) {
	if a.operations == nil {
		a.operations = map[string]string{}
	}
	a.operations[save.id] = save.label
	a.configurationSaves = append(a.configurationSaves, save)
	if len(a.configurationSaves) == 1 {
		a.writeConfiguration()
	}
}

func (a *application) configurationFor(save configurationSave) (vm.Config, error) {
	cfg := a.config
	cfg.Machines = slices.Clone(cfg.Machines)
	err := save.update(&cfg)
	return cfg, err
}

// Build each queued save from the latest committed configuration, not a worker's old copy.
func (a *application) writeConfiguration() {
	save := a.configurationSaves[0]
	cfg, err := a.configurationFor(save)
	if err != nil {
		a.acceptConfiguration(cfg, err)
		if len(a.configurationSaves) > 0 {
			a.writeConfiguration()
		}
		return
	}
	go func() {
		err := vm.Save(filepath.Join(a.root, "library.yaml"), cfg)
		mygo.RunOnMain(func() {
			a.acceptConfiguration(cfg, err)
			if len(a.configurationSaves) > 0 {
				a.writeConfiguration()
			}
		})
	}()
}

func (a *application) acceptConfiguration(cfg vm.Config, err error) {
	save := a.configurationSaves[0]
	a.configurationSaves = a.configurationSaves[1:]
	if err == nil {
		a.config = cfg
	} else if !slices.ContainsFunc(a.config.Machines, func(machine vm.Machine) bool { return machine.ID == save.id }) {
		for _, machine := range cfg.Machines {
			if machine.ID == save.id {
				err = fmt.Errorf("配置保存失败；磁盘保留在 %s: %w", vm.DiskPath(a.root, machine), err)
				break
			}
		}
	}
	a.finishOperation(save.id, save.label, err, save.done)
}
