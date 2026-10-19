//go:build windows

package main

import (
	"fmt"
	"slices"
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

func chooseMachineAction(t *testing.T, tt *ui.Tester, name, action string) {
	t.Helper()
	if err := tt.RightClick(name); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(action); err != nil {
		t.Fatal(err)
	}
}

func blockedMachineAction(t *testing.T, tt *ui.Tester, name, action string) {
	t.Helper()
	if err := tt.RightClick(name); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tt.Menu(), action) {
		t.Fatalf("missing action %q: %q", action, tt.Menu())
	}
	if err := tt.ChooseMenuItem(action); err == nil {
		t.Fatalf("blocked machine accepted %q", action)
	}
	tt.CloseMenu()
}

func TestViewNavigation(t *testing.T) {
	a := testApplication(t, 1)
	tt := testView(a, 1080, 720)
	chooseMachineAction(t, tt, a.config.Machines[0].Name, "编辑配置")
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
	for _, label := range []string{"新建虚拟机", "Hyper-V 设置"} {
		click(t, tt, label)
	}
	if err := tt.RightClick(a.config.Machines[0].Name); err != nil {
		t.Fatal(err)
	}
	if tt.Menu() != nil {
		t.Fatal("busy view opened a machine menu")
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
				ui.Column(c).Label("menu-target").Fill().ContextMenu(func(menu *ui.Menu) {
					a.machineMenu(menu, a.config.Machines[0], state.state)
				}).Children(func() { a.statusCard(c, a.config.Machines[0], state.state) })
			}, 700, 260)
			if !tt.HasText(state.title) {
				t.Fatalf("wrong machine status: %q", tt.Texts())
			}
			if _, ok := tt.Find(state.action); ok {
				t.Fatal("machine action is still on the overview")
			}
			if err := tt.RightClick("menu-target"); err != nil || !slices.Contains(tt.Menu(), state.action) {
				t.Fatalf("missing power menu: %q, %v", tt.Menu(), err)
			}
			if state.state == "已暂停" {
				if err := tt.ChooseMenuItem("正常关机"); err == nil {
					t.Fatal("paused guest accepted an ACPI shutdown request")
				}
			}
		})
	}
}

func TestMachineMenuTargetsRightClickedMachine(t *testing.T) {
	a := testApplication(t, 2)
	first, second := a.config.Machines[0], a.config.Machines[1]
	tt := testView(a, 1080, 720)
	chooseMachineAction(t, tt, second.Name, "编辑配置")
	if a.selected != first.ID || a.page != "edit" || a.draft.ID != second.ID {
		t.Fatal("right-click action used the selected machine instead of its target")
	}
	click(t, tt, "取消")
	chooseMachineAction(t, tt, second.Name, "复制虚拟机")
	if a.cloneSource == nil || a.cloneSource.ID != second.ID || a.draft.ID == second.ID {
		t.Fatal("clone menu used another machine")
	}
}

func TestMachineMenuProtectsCurrentEditor(t *testing.T) {
	for _, index := range []int{0, 1} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			a := testApplication(t, 2)
			m := a.config.Machines[index]
			m.DriverISO = "existing-tools.iso"
			a.config.Machines[index] = m
			a.edit(m, false)
			a.draft.Name = "Unsaved name"
			version := a.editVersion
			tt := testView(a, 1080, 720)
			for _, label := range []string{"安装增强能力", "从列表移除"} {
				blockedMachineAction(t, tt, m.Name, label)
			}
			if a.page != "edit" || a.draft.ID != m.ID || a.draft.Name != "Unsaved name" || a.editVersion != version || !a.hasUnsavedChanges() {
				t.Fatal("menu disturbed the current editor")
			}
			if len(a.config.Machines) != 2 || a.config.Machines[index].DriverISO != m.DriverISO || a.diskBusy(m.ID) {
				t.Fatal("menu removed the edited machine or changed its optical media")
			}
		})
	}
}

func TestMachineMenuSnapshotNavigationAndKeyboard(t *testing.T) {
	a := testApplication(t, 2)
	second := a.config.Machines[1]
	a.snapshotsLoaded = true
	a.snapshots = []vm.Snapshot{{Name: "first-machine-snapshot"}}
	a.externalJobs = map[string]bool{second.ID: true}
	request := a.snapshotRequest
	tt := testView(a, 1080, 720)
	chooseMachineAction(t, tt, second.Name, "管理快照")
	if a.selected != second.ID || a.page != "" || a.detailTab != 1 || a.snapshotsLoaded || len(a.snapshots) != 0 || a.snapshotRequest <= request {
		t.Fatal("snapshot navigation retained another machine's results")
	}
	if !tt.HasText("正在处理") {
		t.Fatal("snapshot page lost its target's job status")
	}
	a = testApplication(t, 1)
	tt = testView(a, 1080, 720)
	click(t, tt, a.config.Machines[0].Name)
	tt.Key(ui.Shift, ui.KeyF10)
	if !slices.Contains(tt.Menu(), "编辑配置") {
		t.Fatalf("keyboard menu unavailable: %q", tt.Menu())
	}
	if err := tt.ChooseMenuItem("编辑配置"); err != nil || a.page != "edit" {
		t.Fatalf("keyboard menu did not navigate: %v", err)
	}
}

func TestMachineMenuStateRestrictions(t *testing.T) {
	for _, test := range []struct {
		name, state, backend string
		dynamic              bool
		blocked              []string
	}{
		{"off", "已关机", "hcs", true, []string{"打开画面", "暂停", "正常关机", "调整运行内存", "基础控制台（安装与维护）", "强制断电"}},
		{"running", "运行中", "hcs", true, []string{"启动虚拟机", "编辑配置", "复制虚拟机", "从列表移除"}},
		{"paused", "已暂停", "hcs", true, []string{"启动虚拟机", "正常关机", "编辑配置", "复制虚拟机", "安装增强能力", "调整运行内存"}},
		{"fixed-memory", "运行中", "hcs", false, []string{"调整运行内存"}},
		{"legacy", "已关机", "qemu", false, []string{"启动虚拟机", "打开画面", "复制虚拟机", "安装增强能力", "管理快照", "调整运行内存"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := testApplication(t, 1)
			m := a.config.Machines[0]
			m.Backend, m.DynamicMemory = test.backend, test.dynamic
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Box(c).Label("target").Fill().ContextMenu(func(menu *ui.Menu) { a.machineMenu(menu, m, test.state) })
			}, 700, 400)
			for _, label := range test.blocked {
				blockedMachineAction(t, tt, "target", label)
			}
			if a.manager.Active() || len(a.operations) != 0 || a.page != "" {
				t.Fatal("disabled menu item started an operation")
			}
		})
	}
}

func TestMachineMenuOpensMemoryPageForTarget(t *testing.T) {
	a := testApplication(t, 2)
	m := a.config.Machines[1]
	m.DynamicMemory = true
	a.runtimeMemoryID = a.selected
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Label("target").Fill().ContextMenu(func(menu *ui.Menu) { a.machineMenu(menu, m, "运行中") })
	}, 700, 400)
	chooseMachineAction(t, tt, "target", "调整运行内存")
	if a.selected != m.ID || a.page != "memory" || a.runtimeMemoryID != "" {
		t.Fatal("memory menu did not select its target and reset the previous input")
	}
}

func TestOverviewOnlyShowsInformation(t *testing.T) {
	a := testApplication(t, 1)
	tt := testView(a, 1080, 720)
	for _, label := range []string{"编辑配置", "复制虚拟机", "启动虚拟机", "打开画面", "暂停", "正常关机", "安装增强能力", "更多操作", "应用内存", "强制断电", "从列表移除"} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("overview still has scattered action %q", label)
		}
	}
	if !tt.HasText("设备与连接") || !tt.HasText("准备就绪") {
		t.Fatal("overview lost its status and device information")
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
			for _, label := range []string{"新建虚拟机", "上一页", "下一页", "Hyper-V 设置", "Workstation 01", "右键虚拟机查看操作", "Windows / HCS"} {
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
