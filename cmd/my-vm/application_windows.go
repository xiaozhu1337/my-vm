//go:build windows

package main

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"my-vm/internal/logging"

	vm "my-vm/library"
)

type application struct {
	runtimeMemory                           float64
	runtimeMemoryID                         string
	networks                                []vm.Switch
	root                                    string
	logger                                  *logging.Logger
	manager                                 *vm.Client
	window                                  *mygo.Window
	config                                  vm.Config
	loadError                               error
	stateRevision                           uint64
	selected, page, query, message, qemuDir string
	busy, messageError                      bool
	closing, closeReady                     bool
	operations                              map[string]string
	displayLock                             sync.Mutex
	displays                                map[string]*displayWindow
	diskJobs                                map[string]*diskJob
	externalJobs                            map[string]bool
	editVersion                             int
	deviceOnly                              bool
	settingsTab                             int
	networkName, networkCIDR                string
	managedNetworks                         []vm.NetworkRecord
	hostAdapters                            []hostAdapter
	networkDetails                          []vm.Network
	cloneSource                             *vm.Machine
	cloneSnapshot                           string
	cloneSnapshots                          []vm.Snapshot
	cloneSnapshotsLoaded                    bool
	listPage, filter, detailTab             int
	draft, savedDraft, persistedDraft       vm.Machine
	creating                                bool
	cpu, memory, maxMemory, disk            float64
	snapshots                               []vm.Snapshot
	snapshotsLoaded                         bool
	snapshotName, snapshotSelected          string
	snapshotTree                            ui.ScrollState
	snapshotRequest                         int
}

func (a *application) report(err error) {
	a.message, a.messageError = err.Error(), true
	if a.logger != nil {
		_, _ = fmt.Fprintln(a.logger, err)
	}
}

func (a *application) machine() (vm.Machine, bool) {
	for _, m := range a.config.Machines {
		if m.ID == a.selected {
			return m, true
		}
	}
	return vm.Machine{}, false
}

func (a *application) selectMachine(m vm.Machine, page string, tab int) bool {
	if !a.leavePage() {
		return false
	}
	if a.selected != m.ID || a.page != page || a.detailTab != tab {
		a.resetSnapshots()
	}
	a.selected, a.page, a.detailTab = m.ID, page, tab
	return true
}

func (a *application) edit(m vm.Machine, creating bool) {
	a.persistedDraft = m
	a.editVersion++
	a.cloneSource = nil
	a.cloneSnapshot, a.cloneSnapshots, a.cloneSnapshotsLoaded = "", nil, false
	a.deviceOnly = false
	if m.GuestOS == "" {
		m.GuestOS = "windows"
	}
	if m.Display == "" {
		m.Display = "std"
	}
	m.Shares = slices.Clone(m.Shares)
	m.NetworkAdapters = slices.Clone(m.NetworkAdapters)
	m.COMPorts = slices.Clone(m.COMPorts)
	a.draft = m
	m.Shares = slices.Clone(m.Shares)
	m.NetworkAdapters = slices.Clone(m.NetworkAdapters)
	m.COMPorts = slices.Clone(m.COMPorts)
	a.savedDraft = m
	a.creating = creating
	a.cpu = float64(m.CPUs)
	a.memory = float64(m.MemoryMiB)
	a.maxMemory = float64(m.MemoryLimitMiB())
	a.disk = float64(m.DiskGiB)
	a.page = "edit"
}

func (a *application) saveDraft() {
	if a.diskBusy("host-network-change") {
		a.report(fmt.Errorf("全局网络正在变更，请完成后再保存虚拟机配置"))
		return
	}
	m := a.draft
	m.Name = strings.TrimSpace(m.Name)
	m.CPUs = int(a.cpu)
	m.MemoryMiB = int(a.memory)
	m.MaxMemoryMiB = int(a.maxMemory)
	m.DiskGiB = int(a.disk)
	if err := m.Validate(); err != nil {
		a.report(err)
		return
	}
	if a.manager.State(m.ID) != "已关机" {
		a.message = "请先关闭虚拟机再修改配置。"
		return
	}
	if a.diskBusy(m.ID) {
		a.message = "请等待这台虚拟机的磁盘操作完成。"
		return
	}
	if a.cloneSource != nil {
		a.saveClone(*a.cloneSource, m)
		return
	}
	creating, version := a.creating, a.editVersion
	original := a.persistedDraft
	action := "update"
	if creating {
		action = "create"
	}
	a.run(m.ID, "保存虚拟机「"+m.Name+"」", func() error {
		request := vm.Request{Action: action, VM: m.ID, Machine: &m}
		if !creating {
			request.Expected = &original
		}
		return a.action(request)
	}, func() { a.finishEditing(m, version) })
}

func (a *application) finishEditing(machine vm.Machine, version int) {
	if a.page == "edit" && a.draft.ID == machine.ID && a.editVersion == version {
		if a.busy {
			a.draft, a.savedDraft, a.persistedDraft, a.creating = machine, machine, machine, false
			a.draft.Shares = slices.Clone(machine.Shares)
			a.draft.NetworkAdapters = slices.Clone(machine.NetworkAdapters)
			a.draft.COMPorts = slices.Clone(machine.COMPorts)
			a.savedDraft.Shares = slices.Clone(machine.Shares)
			a.savedDraft.NetworkAdapters = slices.Clone(machine.NetworkAdapters)
			a.savedDraft.COMPorts = slices.Clone(machine.COMPorts)
			return
		}
		a.selected, a.page, a.detailTab = machine.ID, "", 0
		a.resetSnapshots()
	}
}

func (a *application) hasUnsavedChanges() bool {
	switch a.page {
	case "edit":
		return !reflect.DeepEqual(a.draft, a.savedDraft) || a.cpu != float64(a.savedDraft.CPUs) || a.memory != float64(a.savedDraft.MemoryMiB) || a.maxMemory != float64(a.savedDraft.MemoryLimitMiB()) || a.disk != float64(a.savedDraft.DiskGiB)
	case "settings":
		return false
	}
	return false
}

func (a *application) leavePage() bool {
	return !a.hasUnsavedChanges() || a.confirm("放弃更改", "离开当前页面？", "当前修改尚未保存。选择取消可以继续编辑。")
}

func (a *application) confirm(action, message, detail string) bool {
	a.busy = true
	result, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: a.window, Type: mygo.MessageWarning, Title: action, Message: message, Detail: detail, Buttons: []string{"取消", action}, DefaultButton: 0, CancelButton: 0})
	a.busy = false
	if err != nil {
		a.report(err)
		return false
	}
	return result.Button == 1
}

func (a *application) remove(m vm.Machine) {
	if a.diskBusy(m.ID) {
		return
	}
	if a.manager.State(m.ID) != "已关机" {
		a.message = "请先关闭虚拟机。"
		return
	}
	if !a.confirm("从列表移除", "从列表移除「"+m.Name+"」？", "磁盘和快照将保留在 "+vm.DiskPath(a.root, m)) {
		return
	}
	a.run(m.ID, "移除虚拟机「"+m.Name+"」", func() error {
		return a.action(vm.Request{Action: "remove", VM: m.ID})
	}, func() {
		if a.selected != m.ID {
			return
		}
		a.selected = ""
		if len(a.config.Machines) > 0 {
			a.selected = a.config.Machines[0].ID
		}
		a.detailTab = 0
		a.resetSnapshots()
	})
}

func (a *application) resetSnapshots() {
	a.snapshotRequest++
	a.snapshots = nil
	a.snapshotsLoaded = false
	a.snapshotName, a.snapshotSelected = "", ""
	a.snapshotTree = ui.ScrollState{}
}

func (a *application) acceptSnapshots(id string, request int, snapshots []vm.Snapshot) bool {
	if a.selected != id || a.snapshotRequest != request {
		return false
	}
	a.snapshots, a.snapshotsLoaded = snapshots, true
	selectedExists := a.snapshotSelected == snapshotCurrent || a.snapshotSelected == snapshotRoot
	for _, snapshot := range snapshots {
		selectedExists = selectedExists || snapshot.ID != "" && snapshot.ID == a.snapshotSelected
	}
	if !selectedExists {
		a.snapshotSelected = snapshotCurrent
	}
	return true
}

func (a *application) refreshSnapshots(m vm.Machine) {
	if a.diskBusy(m.ID) {
		return
	}
	request := a.snapshotRequest
	var snapshots []vm.Snapshot
	ctx := context.Background()
	a.run(m.ID, "读取快照「"+m.Name+"」", func() error { var err error; snapshots, err = a.manager.Snapshots(ctx, m.ID); return err }, func() {
		a.acceptSnapshots(m.ID, request, snapshots)
	})
}

func (a *application) snapshot(m vm.Machine, action, name string) {
	if a.diskBusy(m.ID) {
		return
	}
	label := map[string]string{"-c": "创建快照", "-a": "恢复快照", "-d": "删除快照"}[action]
	if action != "-c" && !a.confirm(label, label+"「"+name+"」？", "虚拟机："+m.Name+"\n"+map[string]string{"-a": "恢复会放弃当前位置尚未保存的修改，回到快照时的磁盘、固件及运行状态。旧版磁盘快照不含内存，只能重启客户机；请先保存当前需要保留的状态。", "-d": "删除后无法再恢复到此节点；其他分支和当前虚拟机保留。仍被引用的父盘会继续保留。"}[action]) {
		return
	}
	request := a.snapshotRequest
	ctx := context.Background()
	var snapshots []vm.Snapshot
	a.runDisk(label, m, "", func(progress func(int64, int64)) error {
		if err := a.action(vm.Request{Action: map[string]string{"-c": "snapshot-create", "-a": "snapshot-restore", "-d": "snapshot-delete"}[action], VM: m.ID, Name: name}); err != nil {
			return err
		}
		var err error
		snapshots, err = a.manager.Snapshots(ctx, m.ID)
		return err
	}, func() {
		if a.acceptSnapshots(m.ID, request, snapshots) {
			if action == "-c" {
				a.snapshotName = ""
			}
			if action == "-c" || action == "-a" {
				a.snapshotSelected = snapshotCurrent
			}
		}
	})
}

func (a *application) pickISO(title string, target *string) {
	a.busy = true
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.window, Title: title, Filters: []mygo.FileFilter{{Name: "ISO 镜像", Extensions: []string{"iso"}}}})
	a.busy = false
	if err != nil {
		a.report(err)
	} else if len(paths) > 0 {
		*target = paths[0]
	}
}
