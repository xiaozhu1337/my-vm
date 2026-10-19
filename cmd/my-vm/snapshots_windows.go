//go:build windows

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"my-vm/internal/vm"
)

func (a *application) snapshotView(c *ui.Context, m vm.Machine, off bool) {
	if !off {
		ui.Column(c).FillWidth().Gap(6).Padding(18).Radius(10).
			Background(c.Theme().Background.Mix(c.Theme().Warning, .1)).Children(func() {
			text(c, "关闭虚拟机后可管理快照")
			muted(c, "快照只保存磁盘状态。恢复会覆盖当前磁盘内容。")
		})
	}
	if a.diskBusy(m.ID) {
		muted(c, "这台虚拟机正在处理磁盘，请在状态栏查看进度；仍可切换和管理其他虚拟机。")
	}
	card(c, "保存当前磁盘状态", "更新系统或安装软件前，留下一份可恢复的磁盘快照。", func() {
		ui.Row(c).FillWidth().Gap(10).Disabled(!off || a.diskJob != nil).AlignItems(ui.Center).Children(func() {
			components.TextBox(c, &a.snapshotName, components.TextBoxOptions{}).Label("快照名称").Placeholder("例如 before-update").Grow(1).MinWidth(0)
			if action(c, "创建快照", strings.TrimSpace(a.snapshotName) != "", components.ButtonPrimary) {
				a.snapshot(m, "-c", a.snapshotName)
			}
		})
		muted(c, "名称以英文字母开头，可含数字、下划线和连字符，最多 64 个字符。")
	})
	card(c, "磁盘快照", "关机时创建的快照不包含内存与运行状态。", func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			if a.snapshotsLoaded {
				muted(c, fmt.Sprintf("共 %d 个快照", len(a.snapshots)))
			}
			ui.Spacer(c)
			if button(c, "刷新列表", off && !a.diskBusy(m.ID)) {
				a.refreshSnapshots(m)
			}
		})
		if len(a.snapshots) == 0 {
			ui.Column(c).FillWidth().Gap(8).AlignItems(ui.Center).Padding(24).Children(func() {
				if a.snapshotsLoaded {
					components.Text(c, "还没有快照").FontSize(18).FontWeight(600)
					muted(c, "在上方填写名称，保存第一个磁盘状态。")
				} else {
					text(c, "尚未读取快照")
					muted(c, "虚拟机关机后，点击刷新列表读取磁盘快照。")
				}
			})
			return
		}
		pages := max(1, (len(a.snapshots)+9)/10)
		a.snapshotPage = min(a.snapshotPage, pages-1)
		for _, snapshot := range a.snapshots[a.snapshotPage*10 : min(len(a.snapshots), (a.snapshotPage+1)*10)] {
			ui.Row(c).Key(snapshot.ID).FillWidth().Gap(12).Padding(14).AlignItems(ui.Center).
				Radius(8).Border(1, c.Theme().Border).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
					components.Text(c, snapshot.Name).FontWeight(600).SingleLine().FillWidth()
					muted(c, time.Unix(snapshot.DateSeconds, 0).Format("2006-01-02 15:04"))
				})
				if button(c, "恢复", off && a.diskJob == nil) {
					a.snapshot(m, "-a", snapshot.Name)
				}
				if action(c, "删除", off && a.diskJob == nil, components.ButtonGhost) {
					a.snapshot(m, "-d", snapshot.Name)
				}
			})
		}
		if pages > 1 {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Justify(ui.End).Children(func() {
				if button(c, "上一组", a.snapshotPage > 0) {
					a.snapshotPage--
				}
				muted(c, fmt.Sprintf("%d / %d", a.snapshotPage+1, pages))
				if button(c, "下一组", a.snapshotPage+1 < pages) {
					a.snapshotPage++
				}
			})
		}
	})
}
