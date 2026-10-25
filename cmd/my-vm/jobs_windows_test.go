//go:build windows

package main

import (
	"errors"
	"strings"
	"testing"

	vm "my-vm/library"
)

func TestDiskJobAllowsNavigationAndBlocksSourceActions(t *testing.T) {
	a := testApplication(t, 2)
	source := a.config.Machines[0]
	job := &diskJob{machineID: source.ID, name: source.Name, label: "创建快照", copied: 1, total: 2}
	a.diskJobs = map[string]*diskJob{source.ID: job}
	if status := job.status(); !strings.Contains(status, "50.0%") || strings.Contains(status, "GiB") {
		t.Fatal("snapshot phase was rendered as copied bytes", status)
	}
	tt := testView(a, 1080, 720)
	blockedMachineAction(t, tt, source.Name, "启动虚拟机")
	blockedMachineAction(t, tt, source.Name, "编辑配置")
	if a.busy || a.page != "" || a.manager.Active() {
		t.Fatal("source accepted a conflicting operation")
	}
	if !tt.HasText(job.status()) {
		t.Fatal("copy progress is not visible")
	}
	click(t, tt, a.config.Machines[1].Name)
	chooseMachineAction(t, tt, a.config.Machines[1].Name, "编辑配置")
	if a.page != "edit" || a.draft.ID != a.config.Machines[1].ID {
		t.Fatal("background disk job blocked other machine navigation or editing")
	}
	click(t, tt, "取消")
	click(t, tt, "全局设置")
	if a.page != "settings" || a.busy {
		t.Fatal("background disk job blocked settings")
	}
}

func TestSnapshotResultsBelongToMachineAndRequest(t *testing.T) {
	a := testApplication(t, 2)
	first := a.selected
	request := a.snapshotRequest
	snapshots := []vm.Snapshot{{ID: "one", Name: "original"}}
	a.selected = a.config.Machines[1].ID
	if a.acceptSnapshots(first, request, snapshots) || len(a.snapshots) != 0 {
		t.Fatal("first machine result replaced another machine's list")
	}
	a.selected = first
	a.resetSnapshots()
	if a.acceptSnapshots(first, request, snapshots) {
		t.Fatal("outdated request replaced a new page")
	}
	if !a.acceptSnapshots(first, a.snapshotRequest, snapshots) || !a.snapshotsLoaded || a.snapshotSelected != snapshotCurrent {
		t.Fatal("current snapshot result rejected or working state not selected")
	}
	a.snapshotSelected = "one"
	if !a.acceptSnapshots(first, a.snapshotRequest, snapshots) || a.snapshotSelected != "one" {
		t.Fatal("refresh discarded a valid selection")
	}
	if !a.acceptSnapshots(first, a.snapshotRequest, nil) || a.snapshotSelected != snapshotCurrent {
		t.Fatal("deleted snapshot remained selected")
	}
	a.resetSnapshots()
	if a.snapshotSelected != "" || a.snapshotTree.X != 0 || a.snapshotTree.Y != 0 {
		t.Fatal("navigation retained another tree's selection or scroll position")
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
			blockedMachineAction(t, tt, first.Name, "启动虚拟机")
			blockedMachineAction(t, tt, first.Name, "编辑配置")
			if a.page != "" || a.manager.Active() {
				t.Fatal("busy machine accepted a conflicting UI action")
			}
			click(t, tt, second.Name)
			chooseMachineAction(t, tt, second.Name, "编辑配置")
			if a.page != "edit" || a.draft.ID != second.ID {
				t.Fatal("other machine editor was disabled")
			}
			click(t, tt, "取消")
			click(t, tt, "全局设置")
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

func TestCloneOpensEditorWithIndependentDraft(t *testing.T) {
	a := testApplication(t, 1)
	source := a.config.Machines[0]
	tt := testView(a, 1080, 720)
	chooseMachineAction(t, tt, source.Name, "复制虚拟机")
	if a.cloneSource == nil || a.cloneSource.ID != source.ID || a.draft.ID == source.ID || a.draft.Network != "none" || !tt.HasText("创建独立副本") {
		t.Fatal("clone draft did not preserve source and isolate new identity")
	}
	a.finishOperation(source.ID, "读取复制来源", nil, nil)
	click(t, tt, "取消")
	chooseMachineAction(t, tt, source.Name, "编辑配置")
	if a.cloneSource != nil || a.draft.ID != source.ID || a.creating {
		t.Fatal("clone mode leaked into regular editing")
	}
}

func TestCloneSelectionRequiresExplicitSnapshotAndRejectsOldResults(t *testing.T) {
	a := testApplication(t, 2)
	source := a.config.Machines[0]
	a.edit(vm.NewClone(source, a.config.Machines), true)
	a.cloneSource = &source
	version := a.editVersion
	points := []vm.Snapshot{{ID: "saved", Name: "before-update", Current: true}}
	if !a.acceptCloneSnapshots(source.ID, version, points) || !a.cloneSnapshotsLoaded || a.cloneSnapshot != "" {
		t.Fatal("snapshot source silently selected")
	}
	if a.acceptCloneSnapshots(a.config.Machines[1].ID, version, nil) || a.cloneSnapshot != "" {
		t.Fatal("another source replaced the selection")
	}
	a.edit(source, false)
	if a.acceptCloneSnapshots(source.ID, version, points) || a.cloneSource != nil || a.cloneSnapshotsLoaded {
		t.Fatal("canceled clone accepted an old result")
	}
	a.edit(vm.NewClone(source, a.config.Machines), true)
	a.cloneSource = &source
	if a.acceptCloneSnapshots(source.ID, version, points) || !a.acceptCloneSnapshots(source.ID, a.editVersion, nil) || a.cloneSnapshot != vm.CloneCurrent {
		t.Fatal("reopened editor accepted stale snapshots or empty source was not usable")
	}
}

func TestPrepareCloseCancellationAndOperationGuards(t *testing.T) {
	cases := []struct {
		name                             string
		configure                        func(*application)
		active, external, leave, confirm bool
		wantAllow, wantShutdown          bool
		wantLeave, wantConfirm           int
	}{
		{name: "idle", leave: true, wantAllow: true, wantLeave: 1},
		{name: "unsaved changes canceled", active: true, confirm: true, wantLeave: 1},
		{name: "shutdown canceled", active: true, leave: true, wantLeave: 1, wantConfirm: 1},
		{name: "shutdown confirmed", active: true, leave: true, confirm: true, wantShutdown: true, wantLeave: 1, wantConfirm: 1},
		{name: "dialog open", configure: func(a *application) { a.busy = true }},
		{name: "VM operation", configure: func(a *application) { a.operations = map[string]string{"vm": "starting"} }},
		{name: "disk job", configure: func(a *application) { a.diskJobs = map[string]*diskJob{"vm": {}} }},
		{name: "external job", configure: func(a *application) { a.externalJobs = map[string]bool{"vm": true} }},
		{name: "controller active", external: true},
		{name: "already closing", active: true, configure: func(a *application) { a.closing, a.busy = true, true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := testApplication(t, 1)
			if tc.configure != nil {
				tc.configure(a)
			}
			leaveCalls, confirmCalls := 0, 0
			allow, shutdown := a.prepareClose(tc.active, tc.external, func() bool { leaveCalls++; return tc.leave }, func() bool { confirmCalls++; return tc.confirm })
			if allow != tc.wantAllow || shutdown != tc.wantShutdown || leaveCalls != tc.wantLeave || confirmCalls != tc.wantConfirm {
				t.Fatal("unexpected close decision", allow, shutdown, leaveCalls, confirmCalls)
			}
			if shutdown && (!a.closing || !a.busy || a.beginOperation("vm", "new start")) {
				t.Fatal("new operation accepted while closing")
			}
		})
	}
}

func TestShutdownCompletionClosesOnlyAfterAllGuestsStop(t *testing.T) {
	for _, outcome := range []struct {
		name   string
		err    error
		active bool
	}{
		{"refused", errors.New("guest refused shutdown"), true},
		{"still running", nil, true},
		{"request error but already stopped", errors.New("request failed"), false},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			a := testApplication(t, 1)
			a.closing, a.busy = true, true
			if a.finishShutdown(outcome.err, outcome.active) || a.closeReady || a.closing || a.busy || !a.messageError {
				t.Fatal("failed shutdown exited or prevented recovery", a.message)
			}
			_, retry := a.prepareClose(true, false, func() bool { return true }, func() bool { return true })
			if !retry {
				t.Fatal("failed shutdown could not be retried")
			}
		})
	}
	a := testApplication(t, 1)
	a.closing, a.busy = true, true
	if !a.finishShutdown(nil, false) || !a.closeReady || !a.busy {
		t.Fatal("successful shutdown did not prepare final close")
	}
	unexpected := func() bool { t.Fatal("final close asked for confirmation a second time"); return false }
	allow, shutdown := a.prepareClose(false, true, unexpected, unexpected)
	if !allow || shutdown {
		t.Fatal("final close was blocked or restarted shutdown")
	}
}
