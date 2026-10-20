//go:build windows

package vm

import (
	"errors"
	"fmt"
	"my-vm/internal/hcs"
	"os"
	"path/filepath"
	"strings"
)

func SameMediaFile(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	if strings.EqualFold(filepath.Clean(left), filepath.Clean(right)) {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func (m *Manager) GuestMediaInUse(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, session := range m.sessions {
		if SameMediaFile(session.machine.ISO, path) || SameMediaFile(session.machine.DriverISO, path) {
			return true
		}
	}
	return false
}

func (m *Manager) MountGuestMedia(id, path string) error {
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".iso") {
		return errors.New("增强光盘必须使用 ISO 文件的绝对路径")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("增强光盘不是普通文件")
	}
	lock := m.operation(id)
	lock.Lock()
	defer lock.Unlock()
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return errors.New("虚拟机未运行")
	}
	if s.paused {
		return errors.New("请先继续运行虚拟机再插入增强光盘")
	}
	if err = hcs.PrepareFile("my-vm-"+id, path, ""); err != nil {
		return err
	}
	request := "Update"
	if s.machine.DriverISO == "" {
		request = "Add"
	}
	settings := map[string]any{"Type": "Iso", "Path": path}
	if err = s.system.ModifyRequest("VirtualMachine/Devices/Scsi/my-vm-scsi/Attachments/2", request, settings); err != nil {
		return fmt.Errorf("插入增强光盘失败: %w", err)
	}
	m.mu.Lock()
	s.machine.DriverISO = path
	m.mu.Unlock()
	return nil
}
