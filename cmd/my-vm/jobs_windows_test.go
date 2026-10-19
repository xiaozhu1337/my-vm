//go:build windows

package main

import (
	"path/filepath"
	"testing"

	"my-vm/internal/vm"
)

func TestDiskJobAllowsNavigationAndBlocksSourceActions(t *testing.T) {
	a := testApplication(t, 2)
	source := a.config.Machines[0]
	a.diskJob = &diskJob{machineID: source.ID, name: source.Name, label: "创建快照", copied: 1, total: 2}
	tt := testView(a, 1080, 720)
	click(t, tt, "启动虚拟机")
	click(t, tt, "编辑配置")
	if a.busy || a.page != "" || a.manager.Active() {
		t.Fatal("source accepted a conflicting operation")
	}
	if !tt.HasText(a.diskJob.status()) {
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
	a.busy = true
	a.diskDone = func() { a.finishClone(target) }
	a.flushDisk()
	if len(a.config.Machines) != 2 || a.diskDone == nil {
		t.Fatal("clone was registered while another save still owned the configuration")
	}
	// The regular operation commits its captured configuration before disk completion.
	a.config.Machines[1].Name = "Latest saved name"
	third := vm.NewMachine()
	third.Name = target.Name
	a.config.Machines = append(a.config.Machines, third)
	path := filepath.Join(a.root, "library.yaml")
	if err := vm.Save(path, a.config); err != nil {
		t.Fatal(err)
	}
	a.busy = false
	a.flushDisk()
	cfg, err := vm.Load(path)
	if err != nil || len(cfg.Machines) != 4 || cfg.Machines[1].Name != "Latest saved name" || cfg.Machines[3].ID != target.ID {
		t.Fatal("clone completion lost another save or failed to persist clone", cfg, err)
	}
	if cfg.Machines[2].Name == cfg.Machines[3].Name || a.diskDone != nil {
		t.Fatal("completion retained a duplicate name or ran more than once")
	}
	a.flushDisk()
	if len(a.config.Machines) != 4 {
		t.Fatal("clone was registered twice")
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
