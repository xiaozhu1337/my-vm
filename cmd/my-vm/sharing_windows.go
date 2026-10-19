//go:build windows

package main

import (
	"fmt"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"my-vm/internal/vm"
	"slices"
)

func (a *application) shareEditor(c *ui.Context) {
	card(c, "主机文件夹映射", "只向这台虚拟机公开所选目录；默认只读。客户机需安装 MyVM Guest Tools。", func() {
		remove := -1
		for i := range a.draft.Shares {
			s := &a.draft.Shares[i]
			ui.Column(c).Key(fmt.Sprintf("share-%d", i)).FillWidth().Gap(8).Children(func() {
				field(c, fmt.Sprintf("共享名称 %d", i+1), &s.Name)
				field(c, fmt.Sprintf("主机文件夹 %d", i+1), &s.Path)
				ui.Row(c).Gap(10).Children(func() {
					if button(c, fmt.Sprintf("浏览文件夹 %d", i+1), true) {
						a.busy = true
						paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.window, Title: "选择共享文件夹", Directory: true})
						a.busy = false
						if err != nil {
							a.report(err)
						} else if len(paths) > 0 {
							s.Path = paths[0]
						}
					}
					mode := "只读"
					if !s.ReadOnly {
						mode = "允许写入"
					}
					if button(c, fmt.Sprintf("%s %d", mode, i+1), true) {
						s.ReadOnly = !s.ReadOnly
					}
					if button(c, fmt.Sprintf("移除共享 %d", i+1), true) {
						remove = i
					}
				})
			})
		}
		if remove >= 0 {
			a.draft.Shares = slices.Delete(a.draft.Shares, remove, remove+1)
		}
		if button(c, "添加共享文件夹", len(a.draft.Shares) < 16) {
			name := ""
			for number := 1; name == ""; number++ {
				candidate := fmt.Sprintf("Shared%d", number)
				if !slices.ContainsFunc(a.draft.Shares, func(s vm.Share) bool { return s.Name == candidate }) {
					name = candidate
				}
			}
			a.draft.Shares = append(a.draft.Shares, vm.Share{Name: name, ReadOnly: true})
		}
		muted(c, "共享名称使用英文字母开头，可包含字母、数字、下划线和连字符。Windows 自动分配空闲盘符；Linux 在文件管理器中显示共享目录。")
		if button(c, "挂载客户机工具光盘", true) {
			machine := a.draft
			id, version := machine.ID, a.editVersion
			var target string
			a.run(id, "读取增强光盘", func() error {
				var err error
				target, err = enhancementDisc(machine)
				return err
			}, func() {
				if a.page == "edit" && a.draft.ID == id && a.editVersion == version {
					a.draft.DriverISO = target
				}
			})
		}
		if a.draft.DriverISO != "" {
			infoRow(c, "工具光盘", a.draft.DriverISO)
			if button(c, "卸载工具光盘", true) {
				a.draft.DriverISO = ""
			}
		}
		muted(c, "工具光盘按客户机系统选择对应的 Windows 或 Linux 版本；安装后才会出现共享目录。可写目录中的修改会直接作用于主机文件。")
	})
}
