package rdpauth

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Login is transferred only over the VM-specific parent-host socket.
type Login struct {
	Username string `json:"username"`
	Password []byte `json:"password"`
}

func (l *Login) Clear() { clear(l.Password); l.Password = nil }
func (l *Login) Validate() error {
	if l.Username == "" || len(l.Username) > 256 || strings.ContainsAny(l.Username, "\x00\r\n") || !utf8.ValidString(l.Username) || len(l.Password) == 0 || len(l.Password) > 1024 || !utf8.Valid(l.Password) || bytes.ContainsAny(l.Password, "\x00\r\n") {
		return errors.New("无效的增强登录凭据")
	}
	return nil
}

// The host chooses the stable VM pipe; a guest cannot choose another VM's credential key.
func Target(pipe string) string {
	return fmt.Sprintf("MyVM/LinuxRDP/%x", sha256.Sum256([]byte(strings.ToLower(pipe))))
}
