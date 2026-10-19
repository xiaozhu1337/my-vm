//go:build windows

package vm

import "errors"

func (m *Manager) GuestRuntime(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[id]
	if session == nil {
		return "", errors.New("虚拟机未运行")
	}
	if session.paused {
		return "", errors.New("虚拟机已暂停")
	}
	return session.runtimeID, nil
}
