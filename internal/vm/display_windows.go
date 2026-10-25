//go:build windows

package vm

import "errors"

// GuestDisplayRuntime also permits a paused VM to expose its basic display.
func (m *Manager) GuestDisplayRuntime(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[id]
	if session == nil {
		return "", errors.New("虚拟机未运行")
	}
	return session.runtimeID, nil
}
