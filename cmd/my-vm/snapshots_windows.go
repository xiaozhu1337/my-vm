//go:build windows

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/components/flow"
	vm "my-vm/library"
)

const snapshotRoot = "@snapshot-root"
const snapshotCurrent = "@snapshot-current"

// The working state is a separate leaf, never a saved recovery point.
func snapshotDiagram(snapshots []vm.Snapshot) flow.Data {
	face := func(title, detail string, current bool) func(*ui.Context) {
		return func(c *ui.Context) {
			opts := flow.CardOptions{Width: 200, Title: title, Description: detail}
			if current {
				opts.Border = c.Theme().Accent
			}
			flow.Card(c, opts)
		}
	}
	data := flow.Data{Nodes: []flow.Node{{ID: snapshotRoot, Label: "快照起点", Content: face("快照起点", "各分支的共同起点", false)}}}
	parent := snapshotRoot
	for _, snapshot := range snapshots {
		data.Nodes = append(data.Nodes, flow.Node{ID: snapshot.ID, Label: snapshot.Name,
			Content: face(snapshot.Name, time.Unix(snapshot.DateSeconds, 0).Format("2006-01-02 15:04"), false)})
		source := snapshot.ParentID
		if source == "" {
			source = snapshotRoot
		}
		data.Edges = append(data.Edges, flow.Edge{Source: source, Target: snapshot.ID})
		if snapshot.Current {
			parent = snapshot.ID
		}
	}
	data.Nodes = append(data.Nodes, flow.Node{ID: snapshotCurrent, Label: "当前位置", Content: face("当前位置", "当前工作状态，未保存为快照", true)})
	data.Edges = append(data.Edges, flow.Edge{Source: parent, Target: snapshotCurrent, Dashed: true})
	return data
}

func (a *application) snapshotView(c *ui.Context, m vm.Machine, off bool) {
	if !off {
		muted(c, "运行中创建快照会短暂暂停，保存内存、设备、固件和磁盘；恢复后继续快照时的状态。GPU 共享需先关机。")
	}
	if a.diskBusy(m.ID) {
		muted(c, "这台虚拟机正在处理磁盘，请在状态栏查看进度；仍可切换和管理其他虚拟机。")
	}
	card(c, "快照树", "从左向右查看快照与分支；实线连接已保存快照，虚线指向当前位置。", func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			if a.snapshotsLoaded {
				muted(c, fmt.Sprintf("共 %d 个快照", len(a.snapshots)))
			}
			ui.Spacer(c)
			if button(c, "定位当前", a.snapshotsLoaded) {
				a.snapshotSelected = snapshotCurrent
				positions, _, _ := snapshotLayout(snapshotDiagram(a.snapshots))
				position := positions[snapshotCurrent]
				a.snapshotTree.X, a.snapshotTree.Y = position.x-16, position.y-16
			}
			if button(c, "刷新", !a.diskBusy(m.ID)) {
				a.refreshSnapshots(m)
			}
		})
		if !a.snapshotsLoaded {
			text(c, "尚未读取快照")
			muted(c, "点击刷新读取快照，虚拟机运行期间也可查看。")
			return
		}
		a.fixedSnapshotTree(c, snapshotDiagram(a.snapshots))
		muted(c, "节点大小固定，滚动查看分支；点击节点查看详情，也可用 Tab 和回车选择节点。")
		if len(a.snapshots) == 0 {
			muted(c, "还没有快照")
		}
		a.snapshotDetails(c, m)
	})
	card(c, "保存当前状态", "新快照始终从当前位置创建；要从历史节点建立分支，请先恢复到该节点。", func() {
		ui.Row(c).FillWidth().Gap(10).Disabled(a.diskBusy(m.ID)).AlignItems(ui.Center).Children(func() {
			components.TextBox(c, &a.snapshotName, components.TextBoxOptions{}).Label("快照名称").Placeholder("例如 before-update").Grow(1).MinWidth(0)
			if action(c, "创建快照", strings.TrimSpace(a.snapshotName) != "", components.ButtonPrimary) {
				a.snapshot(m, "-c", a.snapshotName)
			}
		})
		muted(c, "名称以英文字母开头，可含数字、下划线和连字符，最多 64 个字符。关机快照保存磁盘与固件；运行或暂停快照还保存内存和设备。")
	})
}

func (a *application) snapshotDetails(c *ui.Context, m vm.Machine) {
	var selected *vm.Snapshot
	parent := "快照起点"
	for i := range a.snapshots {
		if a.snapshots[i].ID == a.snapshotSelected {
			selected = &a.snapshots[i]
		}
		if a.snapshots[i].Current {
			parent = a.snapshots[i].Name
		}
	}
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		components.Text(c, "节点详情").FontWeight(600)
		switch {
		case selected != nil:
			infoRow(c, "快照名称", selected.Name)
			infoRow(c, "创建时间", time.Unix(selected.DateSeconds, 0).Format("2006-01-02 15:04:05"))
			p := selected.ParentName
			if p == "" {
				p = "快照起点"
			}
			infoRow(c, "父快照", p)
			if selected.Current {
				muted(c, "当前位置从此快照继续；恢复会放弃其后的磁盘更改。")
			}
			if selected.State == "running" || selected.State == "paused" {
				infoRow(c, "保存内容", "磁盘、固件、内存和设备状态")
			} else if selected.State == "off" {
				infoRow(c, "保存内容", "磁盘与固件（关机状态）")
			} else {
				muted(c, "历史磁盘快照，不含内存和固件；恢复后需要重新启动客户机。")
			}
			if selected.Legacy {
				muted(c, "旧版完整磁盘快照")
			}
		case a.snapshotSelected == snapshotCurrent:
			infoRow(c, "当前节点", "当前位置")
			infoRow(c, "基于快照", parent)
			muted(c, "这里是虚拟机当前的工作状态。创建快照可保存此状态；选中其他节点不会改变当前位置。")
		case a.snapshotSelected == snapshotRoot:
			muted(c, "快照起点用于连接各个分支，不是可恢复或删除的快照。")
		default:
			muted(c, "选择一个快照查看详情，或定位当前工作状态。")
		}
		ui.Row(c).Gap(10).Children(func() {
			enabled := selected != nil && !a.diskBusy(m.ID)
			if button(c, "恢复到此快照", enabled) {
				a.snapshot(m, "-a", selected.Name)
			}
			if action(c, "删除此快照", enabled, components.ButtonGhost) {
				a.snapshot(m, "-d", selected.Name)
			}
		})
	})
}
