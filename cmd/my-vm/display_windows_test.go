//go:build windows

package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestViewDisplayAndDriverISO(t *testing.T) {
	a := testApplication(t, 1)
	a.config.Machines[0].Display = "" // Existing libraries keep the original VGA device.
	a.edit(a.config.Machines[0], false)
	if a.draft.Display != "std" || a.hasUnsavedChanges() {
		t.Fatal("opening a legacy machine must not switch its display or dirty its draft")
	}
	tt := testView(a, 1080, 1800)
	click(t, tt, "标准 VGA（安装系统/兼容显示）")
	click(t, tt, "高清 virtio 显卡（Windows 10/11）")
	if a.draft.Display != "virtio" || !a.hasUnsavedChanges() || !tt.HasText("VioGpu Resolution Service") {
		t.Fatal("HD selection must update the draft and explain its guest requirements")
	}
	label, ok := tt.Find("驱动光盘 ISO（可留空）")
	if !ok {
		t.Fatal("missing driver ISO field")
	}
	tt.ClickAt(label.X+40, label.Y+label.H+24)
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type(`C:\drivers\virtio-win.iso`)
	if a.draft.DriverISO != `C:\drivers\virtio-win.iso` || a.config.Machines[0].DriverISO != "" {
		t.Fatal("driver ISO input should only edit the draft")
	}
	click(t, tt, "卸载驱动光盘")
	if a.draft.DriverISO != "" {
		t.Fatal("driver ISO was not unmounted")
	}
}

func TestViewDisplayDetails(t *testing.T) {
	for _, mode := range []struct{ display, label string }{
		{"", "标准 VGA / 兼容显示"},
		{"std", "标准 VGA / 兼容显示"},
		{"virtio", "virtio / 高清显示（需客户机驱动与分辨率服务）"},
	} {
		t.Run(mode.display, func(t *testing.T) {
			a := testApplication(t, 1)
			a.config.Machines[0].Display = mode.display
			a.config.Machines[0].DriverISO = `C:\drivers\virtio-win.iso`
			tt := testView(a, 1080, 1200)
			if !tt.HasText(mode.label) || !tt.HasText(`C:\drivers\virtio-win.iso`) {
				t.Fatalf("display details missing: %q", tt.Texts())
			}
		})
	}
}
