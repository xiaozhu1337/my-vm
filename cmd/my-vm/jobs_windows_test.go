//go:build windows

package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"my-vm/internal/vm"
)

func TestDiskJobAllowsNavigationAndBlocksSourceActions(t *testing.T) {
	a := testApplication(t, 2)
	source := a.config.Machines[0]
	job := &diskJob{machineID: source.ID, name: source.Name, label: "创建快照", copied: 1, total: 2}
	a.diskJobs = map[string]*diskJob{source.ID: job}
	tt := testView(a, 1080, 720)
	click(t, tt, "启动虚拟机")
	click(t, tt, "编辑配置")
	if a.busy || a.page != "" || a.manager.Active() {
		t.Fatal("source accepted a conflicting operation")
	}
	if !tt.HasText(job.status()) {
		t.Fatal("copy progress is not visible")
	}
	click(t, tt, a.config.Machines[1].Name)
	click(t, tt, "编辑配置")
	if a.page != "edit" || a.draft.ID != a.config.Machines[1].ID {
		t.Fatal("background disk job blocked other machine navigation or editing")
	}
	click(t, tt, "取消")
	click(t, tt, "Hyper-V 设置")
	if a.page != "settings" || a.busy {
		t.Fatal("background disk job blocked settings")
	}
}

func TestSnapshotResultsBelongToMachineAndRequest(t *testing.T) {
	a := testApplication(t, 2)
	first := a.selected
	request := a.snapshotRequest
	snapshots := []vm.Snapshot{{Name: "original"}}
	a.selected = a.config.Machines[1].ID
	if a.acceptSnapshots(first, request, snapshots) || len(a.snapshots) != 0 {
		t.Fatal("first machine result replaced another machine's list")
	}
	a.selected = first
	a.resetSnapshots()
	if a.acceptSnapshots(first, request, snapshots) {
		t.Fatal("outdated request replaced a new page")
	}
	if !a.acceptSnapshots(first, a.snapshotRequest, snapshots) || !a.snapshotsLoaded {
		t.Fatal("current snapshot result rejected")
	}
}

func TestCloneCompletionWaitsForLatestConfiguration(t *testing.T) {
	a := testApplication(t, 2)
	target := vm.NewClone(a.config.Machines[0], a.config.Machines)
	secondID := a.config.Machines[1].ID
	first := configurationSave{id: secondID, label: "保存", update: func(cfg *vm.Config) error {
		cfg.Machines[1].Name = "Latest saved name"
		third := vm.NewMachine()
		third.Name = target.Name
		cfg.Machines = append(cfg.Machines, third)
		return nil
	}}
	// Hold the queue as though the first asynchronous save is still writing.
	a.configurationSaves = []configurationSave{first}
	a.finishClone(target)
	if len(a.config.Machines) != 2 || len(a.configurationSaves) != 2 || !a.diskBusy(target.ID) {
		t.Fatal("clone bypassed the configuration queue")
	}
	path := filepath.Join(a.root, "library.yaml")
	for len(a.configurationSaves) > 0 {
		cfg, err := a.configurationFor(a.configurationSaves[0])
		if err != nil {
			t.Fatal(err)
		}
		if err = vm.Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		a.acceptConfiguration(cfg, nil)
	}
	cfg, err := vm.Load(path)
	if err != nil || len(cfg.Machines) != 4 || cfg.Machines[1].Name != "Latest saved name" || cfg.Machines[3].ID != target.ID {
		t.Fatal("clone completion lost another save or failed to persist clone", cfg, err)
	}
	if cfg.Machines[2].Name == cfg.Machines[3].Name || a.diskBusy(target.ID) {
		t.Fatal("completion retained a duplicate name or left the target blocked")
	}
}

func TestMachineOperationAllowsNavigationAndOtherOperations(t *testing.T) {
	for _, label := range []string{"启动虚拟机", "暂停虚拟机", "打开增强会话", "调整运行内存", "读取快照", "插入增强光盘", "保存虚拟机"} {
		t.Run(label, func(t *testing.T) {
			a := testApplication(t, 2)
			first, second := a.config.Machines[0], a.config.Machines[1]
			if !a.beginOperation(first.ID, label) || a.busy || a.beginOperation(first.ID, "重复操作") {
				t.Fatal("operation did not exclusively reserve its machine")
			}
			tt := testView(a, 1080, 720)
			click(t, tt, "启动虚拟机")
			click(t, tt, "编辑配置")
			if a.page != "" || a.manager.Active() {
				t.Fatal("busy machine accepted a conflicting UI action")
			}
			click(t, tt, second.Name)
			click(t, tt, "编辑配置")
			if a.page != "edit" || a.draft.ID != second.ID {
				t.Fatal("other machine editor was disabled")
			}
			click(t, tt, "取消")
			click(t, tt, "Hyper-V 设置")
			if a.page != "settings" || !a.beginOperation(second.ID, "启动另一台虚拟机") {
				t.Fatal("operation blocked settings or another machine")
			}
			a.finishOperation(first.ID, label, nil, nil)
			if a.diskBusy(first.ID) || !a.diskBusy(second.ID) {
				t.Fatal("completion released the wrong machine")
			}
		})
	}
}

func TestConfigurationFailureKeepsLatestStateAndReleasesOnlyOwner(t *testing.T) {
	a := testApplication(t, 2)
	first, second := a.config.Machines[0].ID, a.config.Machines[1].ID
	a.beginOperation(first, "保存配置")
	a.beginOperation(second, "启动虚拟机")
	a.configurationSaves = []configurationSave{{id: first, label: "保存配置", update: func(cfg *vm.Config) error {
		cfg.Machines[0].Name = "Uncommitted"
		return nil
	}}}
	cfg, err := a.configurationFor(a.configurationSaves[0])
	if err != nil {
		t.Fatal(err)
	}
	a.acceptConfiguration(cfg, errors.New("disk full"))
	if a.config.Machines[0].Name == "Uncommitted" || !a.messageError || a.diskBusy(first) || !a.diskBusy(second) {
		t.Fatal("failed save published an uncommitted configuration or released another operation")
	}
}

func TestDiskAndExternalJobsOnlyReserveTheirMachines(t *testing.T) {
	a := testApplication(t, 4)
	first, second, third, fourth := a.config.Machines[0].ID, a.config.Machines[1].ID, a.config.Machines[2].ID, a.config.Machines[3].ID
	a.diskJobs = map[string]*diskJob{first: {machineID: first, targetID: second}, third: {machineID: third}}
	for _, id := range []string{first, second, third} {
		if !a.diskBusy(id) || a.beginOperation(id, "冲突操作") {
			t.Fatal("disk job failed to reserve an involved machine", id)
		}
	}
	if !a.beginOperation(fourth, "启动另一台虚拟机") {
		t.Fatal("disk jobs blocked an unrelated machine")
	}
	delete(a.diskJobs, first)
	if a.diskBusy(first) || a.diskBusy(second) || !a.diskBusy(third) || !a.diskBusy(fourth) {
		t.Fatal("disk completion released unrelated jobs")
	}
	a.externalJobs = map[string]bool{first: true}
	if a.beginOperation(first, "GUI 冲突") || !a.beginOperation(second, "其他 VM") {
		t.Fatal("CLI reservation was not per machine")
	}
}

func TestConfigurationMergeDoesNotResurrectRemovedMachine(t *testing.T) {
	a := testApplication(t, 2)
	saved := a.config.Machines[0]
	saved.Name = "Renamed"
	a.configurationSaves = []configurationSave{
		{id: a.config.Machines[1].ID, label: "移除", update: func(cfg *vm.Config) error { cfg.Machines = cfg.Machines[:1]; return nil }},
		{id: saved.ID, label: "保存", update: func(cfg *vm.Config) error { cfg.Machines[0] = saved; return nil }},
	}
	for len(a.configurationSaves) > 0 {
		cfg, err := a.configurationFor(a.configurationSaves[0])
		if err != nil {
			t.Fatal(err)
		}
		a.acceptConfiguration(cfg, nil)
	}
	if len(a.config.Machines) != 1 || a.config.Machines[0].Name != saved.Name {
		t.Fatal("stale configuration resurrected a removed machine")
	}
}

func TestSavedEditorDoesNotCloseAnotherEditingSession(t *testing.T) {
	a := testApplication(t, 2)
	first, second := a.config.Machines[0], a.config.Machines[1]
	a.edit(first, false)
	version := a.editVersion
	a.edit(second, false)
	a.finishEditing(first, version)
	if a.page != "edit" || a.draft.ID != second.ID {
		t.Fatal("save completion closed another machine's editor")
	}
	a.edit(first, false)
	a.finishEditing(first, version)
	if a.page != "edit" {
		t.Fatal("old completion closed a reopened editor for the same machine")
	}
	a.finishEditing(first, a.editVersion)
	if a.page != "" || a.selected != first.ID {
		t.Fatal("current editing session did not complete")
	}
}

func TestSaveCompletedDuringModalKeepsEditorAndMarksDraftSaved(t *testing.T) {
	a := testApplication(t, 1)
	machine := vm.NewMachine()
	machine.Name = "New VM"
	a.edit(machine, true)
	a.draft.Name = "New name"
	a.cpu = 4
	machine = a.draft
	machine.CPUs = 4
	a.busy = true
	a.finishEditing(machine, a.editVersion)
	if a.page != "edit" || a.creating || a.hasUnsavedChanges() {
		t.Fatal("save completion disturbed the modal editor or left a saved machine marked new")
	}
	a.busy = false
	if !a.leavePage() || a.page != "edit" {
		t.Fatal("modal cancellation did not preserve the saved editor")
	}
	if a.draft.CPUs != 4 {
		t.Fatal("save completion lost normalized hardware settings")
	}
}

func TestFailedNewMachineRegistrationReportsRetainedDisk(t *testing.T) {
	for _, label := range []string{"保存新虚拟机", "登记虚拟机副本"} {
		t.Run(label, func(t *testing.T) {
			a := testApplication(t, 1)
			target := vm.NewMachine()
			a.configurationSaves = []configurationSave{{id: target.ID, label: label}}
			cfg := a.config
			cfg.Machines = append(append([]vm.Machine(nil), cfg.Machines...), target)
			a.acceptConfiguration(cfg, errors.New("permission denied"))
			if len(a.config.Machines) != 1 || !a.messageError || !strings.Contains(a.message, vm.DiskPath(a.root, target)) || !strings.Contains(a.message, "permission denied") {
				t.Fatal("failed registration lost its disk recovery location", a.message)
			}
		})
	}
}

func TestCloneOpensEditorWithIndependentDraft(t *testing.T) {
	a := testApplication(t, 1)
	source := a.config.Machines[0]
	tt := testView(a, 1080, 720)
	click(t, tt, "复制虚拟机")
	if a.cloneSource == nil || a.cloneSource.ID != source.ID || a.draft.ID == source.ID || a.draft.Network != "none" || !tt.HasText("创建独立副本") {
		t.Fatal("clone draft did not preserve source and isolate new identity")
	}
	click(t, tt, "取消")
	click(t, tt, "编辑配置")
	if a.cloneSource != nil || a.draft.ID != source.ID || a.creating {
		t.Fatal("clone mode leaked into regular editing")
	}
}
