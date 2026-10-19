//go:build windows

package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"my-vm/internal/vm"
)

func (a *application) view(c *ui.Context) {
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Fill().MinHeight(0).Disabled(a.busy || a.loadError != nil).Children(func() {
			a.sidebar(c)
			ui.Column(c).Fill().MinWidth(0).Gap(20).Padding(28).Children(func() {
				switch a.page {
				case "settings":
					a.settings(c)
				case "edit":
					ui.Column(c).Disabled(a.diskBusy(a.draft.ID)).Fill().Children(func() { a.editor(c) })
				default:
					a.details(c)
				}
			})
		})
		a.statusBar(c)
	})
}

func (a *application) sidebar(c *ui.Context) {
	ui.Column(c).Width(256).Shrink(0).FillHeight().Gap(18).Padding(22, 16).
		Background(c.Theme().Surface).Border(1, c.Theme().Border).BorderWidth(0, 1, 0, 0).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Padding(0, 6, 8).Children(func() {
			ui.Box(c).Padding(10).Radius(11).Background(c.Theme().Accent).TextColor(c.Theme().AccentText).Children(func() {
				components.Text(c, "VM").FontWeight(700).FontSize(17)
			})
			ui.Column(c).Gap(3).Children(func() {
				components.Text(c, "My VM").FontSize(22).FontWeight(600)
				muted(c, "Windows 虚拟机工作台")
			})
		})
		if components.Button(c, "新建虚拟机", components.ButtonPrimary).FillWidth().MinHeight(40).Clicked() && a.leavePage() {
			a.edit(vm.NewMachine(), true)
		}
		if components.TextBox(c, &a.query, components.TextBoxOptions{}).Label("搜索虚拟机").Placeholder("搜索虚拟机名称").FillWidth().Changed() {
			a.listPage = 0
		}
		if components.Tabs(c, &a.filter, components.TabsHighlight, "全部", "已启动").List.Changed() {
			a.listPage = 0
		}
		machines := []vm.Machine{}
		for _, m := range a.config.Machines {
			if strings.Contains(strings.ToLower(m.Name), strings.ToLower(a.query)) && (a.filter == 0 || a.manager.State(m.ID) != "已关机") {
				machines = append(machines, m)
			}
		}
		pages := max(1, (len(machines)+19)/20)
		a.listPage = min(a.listPage, pages-1)
		components.ScrollView(c, components.ScrollViewOptions{}).Fill().MinHeight(0).Gap(8).Children(func() {
			for _, m := range machines[a.listPage*20 : min(len(machines), (a.listPage+1)*20)] {
				ui.Column(c).Key(m.ID).FillWidth().Children(func() {
					item := components.Button(c, "", components.ButtonGhost).Label(m.Name).FillWidth().MinWidth(0).Padding(13, 12)
					if m.ID == a.selected && a.page == "" {
						item.Background(c.Theme().Background.Mix(c.Theme().Accent, .1)).Border(1, c.Theme().Accent.Alpha(.25))
					}
					item.Children(func() {
						ui.Column(c).FillWidth().MinWidth(0).Gap(7).Children(func() {
							components.Text(c, m.Name).FontWeight(600).SingleLine().FillWidth()
							ui.Row(c).Gap(8).Children(func() {
								state := a.manager.State(m.ID)
								color := c.Theme().TextMuted
								if state == "运行中" {
									color = c.Theme().Success
								} else if state == "已暂停" {
									color = c.Theme().Warning
								}
								components.Text(c, state).FontSize(12).TextColor(color)
								components.Text(c, fmt.Sprintf("%d 核 / %g GiB", m.CPUs, float64(m.MemoryMiB)/1024)).FontSize(12).TextColor(c.Theme().TextMuted)
							})
						})
					})
					if item.Clicked() && a.leavePage() {
						a.selected, a.page = m.ID, ""
						a.resetSnapshots()
						a.detailTab = 0
					}
				})
			}
			if len(machines) == 0 {
				ui.Column(c).FillWidth().Gap(6).Padding(16, 6).Children(func() {
					if len(a.config.Machines) == 0 {
						text(c, "虚拟机库还是空的")
						muted(c, "点击上方按钮开始创建。")
					} else {
						text(c, "没有匹配的虚拟机")
						muted(c, "试试其他名称或切换筛选。")
					}
				})
			}
		})
		muted(c, fmt.Sprintf("%d 台虚拟机", len(machines)))
		if pages > 1 {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(6).Children(func() {
				if action(c, "上一页", a.listPage > 0, components.ButtonGhost) {
					a.listPage--
				}
				muted(c, fmt.Sprintf("%d/%d", a.listPage+1, pages))
				if action(c, "下一页", a.listPage+1 < pages, components.ButtonGhost) {
					a.listPage++
				}
			})
		}
		ui.Box(c).FillWidth().Height(1).Background(c.Theme().Border)
		variant := components.ButtonGhost
		if a.page == "settings" {
			variant = components.ButtonSecondary
		}
		if components.Button(c, "Hyper-V 设置", variant).FillWidth().MinHeight(38).Clicked() && a.leavePage() {
			a.page = "settings"
			a.qemuDir = a.config.QEMUDir
		}
	})
}

func (a *application) statusBar(c *ui.Context) {
	ui.Row(c).FillWidth().Shrink(0).MinHeight(44).Gap(12).Padding(8, 20).AlignItems(ui.Center).
		Background(c.Theme().Surface).Border(1, c.Theme().Border).BorderWidth(1, 0, 0, 0).Children(func() {
		message, color := a.message, c.Theme().TextMuted
		if message == "" {
			message = "就绪"
		} else if a.messageError {
			color = c.Theme().Danger
		} else if !a.busy {
			color = c.Theme().Success
		}
		if a.diskJob != nil && !a.messageError {
			message, color = a.diskJob.status(), c.Theme().TextMuted
		}
		if a.busy || a.diskJob != nil {
			components.Spinner(c, components.SpinnerRing).Label("正在处理").Size(16, 16)
		}
		components.Text(c, strings.SplitN(message, "\n", 2)[0]).SingleLine().Grow(1).MinWidth(0).FontSize(12).TextColor(color)
		if a.messageError && action(c, "查看详情", !a.busy, components.ButtonGhost) {
			_, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: a.window, Type: mygo.MessageError, Title: "操作详情", Message: "操作未完成", Detail: a.message, Buttons: []string{"关闭"}})
			if err != nil {
				a.report(err)
			}
		}
		muted(c, "Windows / HCS")
	})
}
