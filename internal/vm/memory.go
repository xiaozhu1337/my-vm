package vm

func (m *Manager) Memory(id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[id]; s != nil {
		return s.machine.MemoryMiB
	}
	return 0
}
