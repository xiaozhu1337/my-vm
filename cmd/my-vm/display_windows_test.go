//go:build windows

package main

import "testing"

func TestViewGuestHardwareSettings(t *testing.T) {
	a := testApplication(t, 1)
	a.edit(a.config.Machines[0], false)
	tt := testView(a, 1080, 2200)
	click(t, tt, "Windows")
	click(t, tt, "Linux")
	if a.draft.GuestOS != "linux" || a.config.Machines[0].GuestOS != "windows" {
		t.Fatal("guest selection changed saved config")
	}
	click(t, tt, "内存按需分配：开启")
	click(t, tt, "GPU 共享：关闭")
	if a.draft.DynamicMemory || !a.draft.GPUShared || !a.hasUnsavedChanges() {
		t.Fatal("hardware draft not updated")
	}
	if !tt.HasText("dxgkrnl") || !tt.HasText("Hyper-V HCS") {
		t.Fatal("guest driver requirements missing")
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
