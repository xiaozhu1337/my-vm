//go:build windows

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	vm "my-vm/library"
)

func (a *application) beginClone(source vm.Machine) {
	if a.diskBusy(source.ID) || a.manager.State(source.ID) != "已关机" || !a.leavePage() {
		return
	}
	a.edit(vm.NewClone(source, a.config.Machines), true)
	a.cloneSource = &source
	a.loadCloneSnapshots(source)
}

func (a *application) loadCloneSnapshots(source vm.Machine) {
	version := a.editVersion
	var snapshots []vm.Snapshot
	a.run(source.ID, "读取复制来源", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		snapshots, err = a.manager.Snapshots(ctx, source.ID)
		return err
	}, func() { a.acceptCloneSnapshots(source.ID, version, snapshots) })
}

func (a *application) acceptCloneSnapshots(sourceID string, version int, snapshots []vm.Snapshot) bool {
	if a.cloneSource == nil || a.cloneSource.ID != sourceID || a.editVersion != version {
		return false
	}
	a.cloneSnapshots, a.cloneSnapshotsLoaded = snapshots, true
	if len(snapshots) == 0 {
		a.cloneSnapshot = vm.CloneCurrent
	}
	return true
}

func (a *application) cloneStateEditor(c *ui.Context) {
	card(c, "复制来源", "副本是独立磁盘，不继承源虚拟机的快照树。", func() {
		if button(c, "刷新复制来源", !a.diskBusy(a.cloneSource.ID)) {
			a.cloneSnapshotsLoaded = false
			a.cloneSnapshot = ""
			a.loadCloneSnapshots(*a.cloneSource)
		}
		if !a.cloneSnapshotsLoaded {
			muted(c, "正在读取快照；读取失败后可点击刷新重试。")
			return
		}
		options := []string{"", vm.CloneCurrent}
		labels := map[string]string{"": "请选择复制来源", vm.CloneCurrent: "当前工作状态（包含最后一次快照后的更改）"}
		for _, snapshot := range a.cloneSnapshots {
			options = append(options, snapshot.ID)
			labels[snapshot.ID] = snapshot.Name + " / " + time.Unix(snapshot.DateSeconds, 0).Format("2006-01-02 15:04")
		}
		choice(c, "要复制的状态", &a.cloneSnapshot, options, labels)
		muted(c, "历史快照复制其磁盘和已保存的固件，不加载运行内存；不会恢复或切换源虚拟机。")
	})
}

func (a *application) saveClone(source, target vm.Machine) {
	if a.diskBusy(source.ID) {
		return
	}
	if !a.cloneSnapshotsLoaded || a.cloneSnapshot == "" {
		a.report(fmt.Errorf("请先读取并选择要复制的状态"))
		return
	}
	for _, existing := range a.config.Machines {
		if strings.EqualFold(existing.Name, target.Name) {
			a.message, a.messageError = "此名称已存在，请为副本选择其他名称。", true
			return
		}
	}
	state := "当前工作状态"
	for _, snapshot := range a.cloneSnapshots {
		if snapshot.ID == a.cloneSnapshot {
			state = "快照「" + snapshot.Name + "」"
		}
	}
	if !a.confirm("复制虚拟机", "从「"+source.Name+"」的"+state+"创建「"+target.Name+"」？", "将复制所选状态的磁盘及可用固件，不加载运行内存。客户机的账户、主机名及系统身份也会保留。副本默认断网且不共享主机目录；联网前请准备客户机身份，避免名称、静态 IP 或系统身份冲突。") {
		return
	}
	snapshot := a.cloneSnapshot
	a.runDisk("复制虚拟机", source, target.ID, func(progress func(int64, int64)) error {
		return a.action(vm.Request{Action: "clone", VM: source.ID, Machine: &target, Snapshot: snapshot})
	}, func() { a.finishClone(target) })
	if a.diskJobs[source.ID] != nil {
		a.page = ""
		a.cloneSource = nil
	}
}

func (a *application) finishClone(target vm.Machine) {
	a.selected = target.ID
	a.resetSnapshots()
}
