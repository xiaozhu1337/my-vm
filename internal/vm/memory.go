package vm

import (
	"errors"
	"fmt"
)

// MemoryLimitMiB preserves the previous adjustment ceiling when old configurations omit a limit.
func (m Machine) MemoryLimitMiB() int {
	if m.MaxMemoryMiB == 0 {
		return 1048576
	}
	return m.MaxMemoryMiB
}

func (m *Manager) Memory(id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[id]; s != nil {
		return s.machine.MemoryMiB
	}
	return 0
}
func (m *Manager) SetMemory(id string, size int) error {
	if size < 256 || size > 1048576 {
		return errors.New("内存需要在 256 到 1048576 MiB 之间")
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
		return errors.New("请先继续运行虚拟机再调整内存")
	}
	if !s.machine.DynamicMemory {
		return errors.New("请先关机并启用内存回收与热调整")
	}
	if size > s.machine.MemoryLimitMiB() {
		return fmt.Errorf("运行内存不能超过此虚拟机的最大内存 %d MiB", s.machine.MemoryLimitMiB())
	}
	if err := s.system.Modify("VirtualMachine/ComputeTopology/Memory/SizeInMB", size); err != nil {
		return fmt.Errorf("调整客户机内存失败: %w", err)
	}
	m.mu.Lock()
	s.machine.MemoryMiB = size
	m.mu.Unlock()
	return nil
}
