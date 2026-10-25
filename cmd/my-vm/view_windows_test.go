//go:build windows

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	vm "my-vm/library"
)

func testApplication(t *testing.T, count int) *application {
	t.Helper()
	a := &application{root: t.TempDir()}
	for i := range count {
		m := vm.NewMachine()
		m.Name = fmt.Sprintf("Workstation %02d", i+1)
		a.config.Machines = append(a.config.Machines, m)
	}
	if count > 0 {
		a.selected = a.config.Machines[0].ID
	}
	if err := vm.Save(filepath.Join(a.root, "library.yaml"), a.config); err != nil {
		t.Fatal(err)
	}
	service := vm.NewService(a.root, nil)
	a.manager = vm.NewClient(&http.Client{Transport: guiTestTransport(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		service.ServeHTTP(response, request)
		return response.Result(), nil
	})})
	if err := a.manager.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a
}

type guiTestTransport func(*http.Request) (*http.Response, error)

func (f guiTestTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

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

func TestDeviceManagerAndGlobalSettings(t *testing.T) {
	a := testApplication(t, 2)
	machine := a.config.Machines[1]
	tt := testView(a, 1080, 720)
	chooseMachineAction(t, tt, machine.Name, "设备管理器")
	if !a.deviceOnly || a.draft.ID != machine.ID || !tt.HasText("设备管理器") || !tt.HasText("硬件清单") {
		t.Fatalf("wrong device page: %+v %v", a.draft, tt.Texts())
	}
	click(t, tt, "返回")
	click(t, tt, "全局设置")
	click(t, tt, "主机网卡")
	if a.settingsTab != 2 || !tt.HasText("主机网络适配器") {
		t.Fatal("host adapters page missing")
	}
	click(t, tt, "虚拟网络")
	if a.settingsTab != 1 || !tt.HasText("新建 NAT 网络") {
		t.Fatal("network management page missing")
	}
	if err := tt.RightClick("全局设置"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("数据存储"); err != nil {
		t.Fatal(err)
	}
	if a.settingsTab != 3 {
		t.Fatal("global settings menu did not select storage")
	}
}
func TestDeviceDraftIsolationAndAddRemove(t *testing.T) {
	a := testApplication(t, 1)
	m := a.config.Machines[0]
	m.NetworkAdapters = []vm.NetworkAdapter{{Network: "nat", MACAddress: "02:00:00:00:00:01"}}
	m.COMPorts = []vm.COMPort{{Number: 1, Pipe: `\\.\pipe\myvm-fixture-com1`}}
	a.config.Machines[0] = m
	a.edit(m, false)
	a.draft.NetworkAdapters[0].MACAddress = "02:00:00:00:00:02"
	a.draft.COMPorts[0].Pipe = `\\.\pipe\myvm-fixture-other`
	if a.savedDraft.NetworkAdapters[0].MACAddress != m.NetworkAdapters[0].MACAddress || a.savedDraft.COMPorts[0].Pipe != m.COMPorts[0].Pipe || !a.hasUnsavedChanges() {
		t.Fatal("device draft aliases committed state")
	}
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(appTheme(c))
		ui.Column(c).FillWidth().Children(func() { a.networkEditor(c); a.serialEditor(c) })
	}, 900, 2400)
	click(t, tt, "添加网络适配器")
	click(t, tt, "添加 COM 串口")
	if len(a.draft.NetworkAdapters) != 2 || len(a.draft.COMPorts) != 2 || a.draft.COMPorts[1].Number != 2 {
		t.Fatal("device additions failed")
	}
	if err := a.draft.Validate(); err != nil {
		t.Fatal(err)
	}
	click(t, tt, "移除网卡 2")
	click(t, tt, "移除 COM1")
	if len(a.draft.NetworkAdapters) != 1 || len(a.draft.COMPorts) != 1 || a.draft.COMPorts[0].Number != 2 {
		t.Fatal("removing first device corrupted remaining list")
	}
	click(t, tt, "添加 COM 串口")
	if a.draft.COMPorts[1].Number != 1 {
		t.Fatal("did not reuse free COM1 slot")
	}
	a.busy = true
	a.finishEditing(m, a.editVersion)
	a.draft.COMPorts[0].Pipe = `\\.\pipe\changed-after-save`
	if a.savedDraft.COMPorts[0].Pipe != m.COMPorts[0].Pipe || a.config.Machines[0].COMPorts[0].Pipe != m.COMPorts[0].Pipe {
		t.Fatal("modal completion aliases device state")
	}
}
func TestGlobalNetworkMutationGates(t *testing.T) {
	a := testApplication(t, 1)
	a.config.Machines[0].NetworkAdapters = []vm.NetworkAdapter{{Network: "bridge", SwitchID: "00000000-0000-0000-0000-000000000123"}}
	if len(a.networkReferences()) != 1 || !a.networkMutationAllowed() {
		t.Fatal("wrong initial network state")
	}
	a.operations = map[string]string{"host-network-change": "create"}
	if a.networkMutationAllowed() || a.beginOperation(a.selected, "start") {
		t.Fatal("network mutation allowed overlapping operation")
	}
	a.edit(a.config.Machines[0], false)
	a.saveDraft()
	if !a.messageError || a.operations[a.selected] != "" {
		t.Fatal("saved config during network mutation")
	}
}

func TestViewNavigation(t *testing.T) {
	a := testApplication(t, 1)
	tt := testView(a, 1080, 720)
	chooseMachineAction(t, tt, a.config.Machines[0].Name, "编辑配置")
	if a.page != "edit" || a.creating || !tt.HasText("保存更改") {
		t.Fatalf("edit navigation failed: page=%q texts=%q", a.page, tt.Texts())
	}
	click(t, tt, "取消")
	click(t, tt, "全局设置")
	if a.page != "settings" || !tt.HasText("全局设置") {
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
	for _, label := range []string{"新建虚拟机", "全局设置"} {
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
	for _, state := range []struct{ state, action string }{
		{"已关机", "启动虚拟机"},
		{"运行中", "打开画面"},
		{"已暂停", "继续运行"},
	} {
		t.Run(state.state, func(t *testing.T) {
			a := testApplication(t, 1)
			tt := ui.NewTester(func(c *ui.Context) {
				c.SetTheme(appTheme(c))
				ui.Column(c).Label("menu-target").Fill().ContextMenu(func(menu *ui.Menu) {
					a.machineMenu(menu, a.config.Machines[0], state.state)
				}).Children(func() { stateBadge(c, state.state) })
			}, 700, 260)
			if !tt.HasText(state.state) {
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
	if !tt.HasText("设备与连接") || !tt.HasText("已关机") {
		t.Fatal("overview lost its status and device information")
	}
	for _, label := range []string{"Windows 虚拟机工作台", "准备就绪", "启动虚拟机后，客户机画面将在独立窗口中打开。", "右键左侧虚拟机可查看全部操作。"} {
		if tt.HasText(label) {
			t.Fatalf("overview still has redundant information %q", label)
		}
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

func TestViewRunningSnapshotControlsAndTree(t *testing.T) {
	a := testApplication(t, 1)
	a.snapshotName = "before-update"
	a.snapshots = []vm.Snapshot{{ID: "1", Name: "baseline", DateSeconds: 1700000000}, {ID: "2", Name: "branch", ParentID: "1", ParentName: "baseline", Depth: 1, Current: true, DateSeconds: 1700000001}}
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(appTheme(c))
		a.snapshotView(c, a.config.Machines[0], false)
	}, 700, 900)
	for _, label := range []string{"支持运行中创建和删除差分快照", "+-- branch", "父快照：baseline", "当前分支"} {
		if !tt.HasText(label) {
			t.Fatal("running snapshot tree missing", label, tt.Texts())
		}
	}
	click(t, tt, "创建快照")
	if a.busy || a.diskJobs[a.selected] == nil {
		t.Fatal("running snapshot control did not start a background job")
	}
	for _, label := range []string{"创建快照", "刷新列表", "恢复", "删除"} {
		click(t, tt, label)
	}
	if len(a.diskJobs) != 1 {
		t.Fatal("busy snapshot controls accepted a conflicting job")
	}
}

func TestViewMinimumSizeAndTheme(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprintf("dark=%t", dark), func(t *testing.T) {
			a := testApplication(t, 21)
			tt := testView(a, 1040, 660)
			tt.SetDark(dark)
			for _, label := range []string{"新建虚拟机", "上一页", "下一页", "全局设置", "Workstation 01", "右键虚拟机查看操作", "Windows / HCS"} {
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
