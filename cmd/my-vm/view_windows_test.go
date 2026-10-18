//go:build windows

package main

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
	"my-vm/internal/vm"
)

func testApplication(t *testing.T, count int) *application {
	t.Helper()
	a := &application{root: t.TempDir()}
	a.manager = vm.NewManager(a.root, nil)
	for i := range count {
		m := vm.NewMachine()
		m.Name = fmt.Sprintf("Workstation %02d", i+1)
		a.config.Machines = append(a.config.Machines, m)
	}
	if count > 0 {
		a.selected = a.config.Machines[0].ID
	}
	return a
}

func testView(a *application, width, height int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		c.SetTheme(appTheme(c))
		a.view(c)
	}, width, height)
}

func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}

func TestViewNavigation(t *testing.T) {
	a := testApplication(t, 1)
	tt := testView(a, 1080, 720)
	click(t, tt, "编辑配置")
	if a.page != "edit" || a.creating || !tt.HasText("保存更改") {
		t.Fatalf("edit navigation failed: page=%q texts=%q", a.page, tt.Texts())
	}
	click(t, tt, "取消")
	click(t, tt, "Hyper-V 设置")
	if a.page != "settings" || !tt.HasText("Hyper-V 设置") {
		t.Fatal("settings navigation failed")
	}
	click(t, tt, "返回")
	click(t, tt, "新建虚拟机")
	if a.page != "edit" || !a.creating || !tt.HasText("创建虚拟机") {
		t.Fatal("create navigation failed")
	}
}

func TestViewSearchAndPagination(t *testing.T) {
	a := testApplication(t, 21)
	tt := testView(a, 1080, 720)
	click(t, tt, "下一页")
	if a.listPage != 1 || !tt.HasText("Workstation 21") {
		t.Fatal("second page was not displayed")
	}
	click(t, tt, "Workstation 21")
	if a.selected != a.config.Machines[20].ID {
		t.Fatal("machine selection failed")
	}
	click(t, tt, "搜索虚拟机")
	tt.Type("Workstation 02")
	if a.listPage != 0 || !tt.HasText("Workstation 02") || tt.HasText("Workstation 01") {
		t.Fatalf("search failed: page=%d query=%q texts=%q", a.listPage, a.query, tt.Texts())
	}
	click(t, tt, "已启动")
	if !tt.HasText("没有匹配的虚拟机") {
		t.Fatal("running filter included a powered-off machine")
	}
}

func TestViewEditorInput(t *testing.T) {
	a := testApplication(t, 1)
	a.edit(a.config.Machines[0], false)
	tt := testView(a, 1080, 720)
	label, ok := tt.Find("虚拟机名称")
	if !ok {
		t.Fatal("missing name field label")
	}
	tt.ClickAt(label.X+40, label.Y+label.H+24)
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("Renamed workstation")
	if a.draft.Name != "Renamed workstation" || !a.hasUnsavedChanges() {
		t.Fatalf("input did not update draft: %q", a.draft.Name)
	}
	if a.config.Machines[0].Name != "Workstation 01" {
		t.Fatal("editing mutated the saved configuration")
	}
}

func TestViewBusyBlocksActions(t *testing.T) {
	a := testApplication(t, 1)
	a.busy = true
	tt := testView(a, 1080, 720)
	for _, label := range []string{"新建虚拟机", "编辑配置", "Hyper-V 设置", "启动虚拟机"} {
		click(t, tt, label)
	}
	if a.page != "" || !a.busy || a.manager.Active() {
		t.Fatal("busy view accepted an operation")
	}
}

func TestViewPowerStates(t *testing.T) {
	for _, state := range []struct{ state, title, action string }{
		{"已关机", "准备就绪", "启动虚拟机"},
		{"运行中", "虚拟机正在运行", "打开画面"},
		{"已暂停", "虚拟机已暂停", "继续运行"},
	} {
		t.Run(state.state, func(t *testing.T) {
			a := testApplication(t, 1)
			tt := ui.NewTester(func(c *ui.Context) {
				c.SetTheme(appTheme(c))
				a.powerCard(c, a.config.Machines[0], state.state)
			}, 700, 260)
			if !tt.HasText(state.title) || !tt.HasText(state.action) {
				t.Fatalf("wrong power controls: %q", tt.Texts())
			}
			if state.state == "已暂停" {
				click(t, tt, "正常关机")
				if a.busy {
					t.Fatal("paused guest accepted an ACPI shutdown request")
				}
			}
		})
	}
}

func TestViewSnapshots(t *testing.T) {
	a := testApplication(t, 1)
	a.detailTab, a.snapshotsLoaded = 1, true
	for i := range 11 {
		a.snapshots = append(a.snapshots, vm.Snapshot{ID: fmt.Sprint(i + 1), Name: fmt.Sprintf("checkpoint-%02d", i+1), DateSeconds: 1700000000})
	}
	tt := testView(a, 1080, 720)
	tt.Move(850, 500)
	tt.Scroll(850, 500, 0, 1800)
	click(t, tt, "下一组")
	if a.snapshotPage != 1 || !tt.HasText("checkpoint-11") {
		t.Fatalf("snapshot pagination failed: page=%d texts=%q", a.snapshotPage, tt.Texts())
	}
	a.snapshots = nil
	tt.Frame()
	if !tt.HasText("还没有快照") {
		t.Fatal("loaded empty snapshots are not distinguished from unread snapshots")
	}
}

func TestViewRunningBlocksSnapshots(t *testing.T) {
	a := testApplication(t, 1)
	a.snapshotName = "before-update"
	a.snapshots = []vm.Snapshot{{ID: "1", Name: "baseline", DateSeconds: 1700000000}}
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(appTheme(c))
		a.snapshotView(c, a.config.Machines[0], false)
	}, 700, 700)
	for _, label := range []string{"创建快照", "刷新列表", "恢复", "删除"} {
		click(t, tt, label)
	}
	if a.busy || !tt.HasText("关闭虚拟机后可管理快照") {
		t.Fatal("running guest accepted a snapshot operation")
	}
}

func TestViewMinimumSizeAndTheme(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprintf("dark=%t", dark), func(t *testing.T) {
			a := testApplication(t, 21)
			tt := testView(a, 1040, 660)
			tt.SetDark(dark)
			for _, label := range []string{"新建虚拟机", "上一页", "下一页", "Hyper-V 设置", "编辑配置", "启动虚拟机", "Windows / HCS"} {
				r, ok := tt.Find(label)
				if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 1040 || r.Y+r.H > 660 {
					t.Fatalf("control %q outside minimum window: %v, found=%t", label, r, ok)
				}
			}
			for _, page := range []struct{ name, footer string }{{"edit", "保存更改"}, {"settings", "返回"}} {
				a.edit(a.config.Machines[0], false)
				a.page = page.name
				tt.Frame()
				r, ok := tt.Find(page.footer)
				if !ok || r.X < 256 || r.Y < 0 || r.X+r.W > 1040 || r.Y+r.H > 660 {
					t.Fatalf("%s footer outside minimum window: %v, found=%t", page.name, r, ok)
				}
			}
		})
	}
}
