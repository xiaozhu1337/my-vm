//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"my-vm/internal/logging"
	"my-vm/internal/vm"
)

type application struct {
	root                                    string
	logger                                  *logging.Logger
	manager                                 *vm.Manager
	window                                  *mygo.Window
	config                                  vm.Config
	loadError                               error
	selected, page, query, message, qemuDir string
	busy                                    bool
	listPage                                int
	draft                                   vm.Machine
	creating                                bool
	cpu, memory, disk                       float64
	snapshots                               []vm.Snapshot
	snapshotName                            string
	snapshotPage                            int
}

func (a *application) report(err error) { a.message = err.Error(); _, _ = fmt.Fprintln(a.logger, err) }

func (a *application) run(label string, work func() error, done func()) {
	if a.busy || a.loadError != nil {
		return
	}
	a.busy = true
	a.message = label + "…"
	go func() {
		err := work()
		mygo.RunOnMain(func() {
			a.busy = false
			if err != nil {
				a.report(err)
			} else {
				a.message = label + "完成"
				if done != nil {
					done()
				}
			}
			a.window.Invalidate()
		})
	}()
}

func (a *application) machine() (vm.Machine, bool) {
	for _, m := range a.config.Machines {
		if m.ID == a.selected {
			return m, true
		}
	}
	return vm.Machine{}, false
}

func (a *application) edit(m vm.Machine, creating bool) {
	a.draft = m
	a.creating = creating
	a.cpu = float64(m.CPUs)
	a.memory = float64(m.MemoryMiB)
	a.disk = float64(m.DiskGiB)
	a.page = "edit"
}

func (a *application) saveDraft() {
	m := a.draft
	m.Name = strings.TrimSpace(m.Name)
	m.CPUs = int(a.cpu)
	m.MemoryMiB = int(a.memory)
	m.DiskGiB = int(a.disk)
	if err := m.Validate(); err != nil {
		a.report(err)
		return
	}
	if a.manager.State(m.ID) != "已关机" {
		a.message = "请先关闭虚拟机再修改配置。"
		return
	}
	cfg := a.config
	cfg.Machines = slices.Clone(cfg.Machines)
	if a.creating {
		cfg.Machines = append(cfg.Machines, m)
	} else {
		for i := range cfg.Machines {
			if cfg.Machines[i].ID == m.ID {
				cfg.Machines[i] = m
			}
		}
	}
	creating := a.creating
	dir := cfg.QEMUDir
	a.run("保存虚拟机", func() error {
		if creating {
			if err := a.manager.CreateDisk(dir, m); err != nil {
				return err
			}
		}
		if err := vm.Save(filepath.Join(a.root, "library.yaml"), cfg); err != nil {
			if creating {
				return fmt.Errorf("配置保存失败，磁盘保留在 %s: %w", vm.DiskPath(a.root, m), err)
			}
			return err
		}
		return nil
	}, func() { a.config = cfg; a.selected = m.ID; a.page = ""; a.snapshots = nil })
}

func (a *application) confirm(message, detail string) bool {
	a.busy = true
	result, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: a.window, Type: mygo.MessageWarning, Title: "确认操作", Message: message, Detail: detail, Buttons: []string{"取消", "确认"}, DefaultButton: 0, CancelButton: 0})
	a.busy = false
	if err != nil {
		a.report(err)
		return false
	}
	return result.Button == 1
}

func (a *application) remove(m vm.Machine) {
	if a.manager.State(m.ID) != "已关机" {
		a.message = "请先关闭虚拟机。"
		return
	}
	if !a.confirm("从列表移除 "+m.Name+"？", "磁盘和快照将保留在 "+vm.DiskPath(a.root, m)) {
		return
	}
	cfg := a.config
	cfg.Machines = slices.DeleteFunc(slices.Clone(cfg.Machines), func(item vm.Machine) bool { return item.ID == m.ID })
	a.run("移除虚拟机", func() error { return vm.Save(filepath.Join(a.root, "library.yaml"), cfg) }, func() { a.config = cfg; a.selected = ""; a.snapshots = nil })
}

func (a *application) refreshSnapshots(m vm.Machine) {
	var snapshots []vm.Snapshot
	dir := a.config.QEMUDir
	a.run("读取快照", func() error { var err error; snapshots, err = a.manager.Snapshots(dir, m); return err }, func() { a.snapshots = snapshots; a.snapshotPage = 0 })
}

func (a *application) snapshot(m vm.Machine, action, name string) {
	if action != "-c" && !a.confirm("确认对快照 "+name+" 执行操作？", map[string]string{"-a": "恢复会覆盖当前磁盘内容，请先为当前状态创建快照。", "-d": "删除后无法再恢复到此快照。"}[action]) {
		return
	}
	dir := a.config.QEMUDir
	var snapshots []vm.Snapshot
	a.run("操作快照", func() error {
		if err := a.manager.Snapshot(dir, m, action, name); err != nil {
			return err
		}
		var err error
		snapshots, err = a.manager.Snapshots(dir, m)
		return err
	}, func() { a.snapshots = snapshots; a.snapshotPage = 0 })
}

func (a *application) pickISO() {
	a.busy = true
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.window, Title: "选择安装光盘", Filters: []mygo.FileFilter{{Name: "ISO 镜像", Extensions: []string{"iso"}}}})
	a.busy = false
	if err != nil {
		a.report(err)
	} else if len(paths) > 0 {
		a.draft.ISO = paths[0]
	}
}
