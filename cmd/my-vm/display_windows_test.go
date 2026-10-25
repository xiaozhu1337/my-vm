//go:build windows

package main

import "testing"

func TestViewGuestHardwareSettings(t *testing.T) {
	a := testApplication(t, 1)
	a.edit(a.config.Machines[0], false)
	tt := testView(a, 1080, 2200)
	click(t, tt, "Windows 10")
	click(t, tt, "Ubuntu 26")
	if a.draft.GuestOS != "ubuntu26" || a.config.Machines[0].GuestOS != "windows10" || a.draft.DesktopMode() != "wayland" {
		t.Fatal("guest selection changed saved config")
	}
	click(t, tt, "内存回收与热调整：开启")
	click(t, tt, "GPU 共享：关闭")
	if a.draft.DynamicMemory || !a.draft.GPUShared || !a.hasUnsavedChanges() {
		t.Fatal("hardware draft not updated")
	}
	if !tt.HasText("dxgkrnl") || !tt.HasText("Hyper-V HCS") {
		t.Fatal("guest driver requirements missing")
	}
}
func TestViewGuestDesktopMode(t *testing.T) {
	a := testApplication(t, 1)
	a.config.Machines[0].GuestOS = "linux"
	a.edit(a.config.Machines[0], false)
	tt := testView(a, 1080, 2200)
	if tt.HasText("X11 / xrdp") || a.draft.LinuxDesktop != "" {
		t.Fatal("disabled legacy desktop offered or existing configuration changed")
	}
	click(t, tt, "基础画面（旧增强方式停用）")
	click(t, tt, "Wayland / GNOME · KDE")
	if a.draft.LinuxDesktop != "wayland" || a.config.Machines[0].LinuxDesktop != "" {
		t.Fatal("desktop choice did not stay in draft")
	}
	click(t, tt, "Linux（其他发行版）")
	click(t, tt, "Windows 11")
	if a.draft.LinuxDesktop != "" {
		t.Fatal("Windows retained a Linux desktop mode")
	}
}

func TestGlobalMitigationsCheckbox(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		a := testApplication(t, 1)
		a.page = "settings"
		a.config.SideChannelMitigations = enabled
		// Disable saving here; native UI and the main-thread event loop are not used by tests.
		a.busy = true
		tt := testView(a, 1080, 1800)
		if !tt.HasText("启用侧通道缓解") {
			t.Fatal("global setting not visible")
		}
		click(t, tt, "启用侧通道缓解")
		if a.config.SideChannelMitigations != enabled || len(a.operations) != 0 {
			t.Fatal("busy setting was changed")
		}
	}
}

func TestViewHCSDisplayDetails(t *testing.T) {
	a := testApplication(t, 1)
	tt := testView(a, 1080, 1200)
	if !tt.HasText("Hyper-V 基础控制台 / RDP 增强会话") || !tt.HasText("VHDX") {
		t.Fatalf("native backend details missing: %q", tt.Texts())
	}
	if tt.HasText("virtio") {
		t.Fatal("legacy graphics shown for HCS")
	}
}
