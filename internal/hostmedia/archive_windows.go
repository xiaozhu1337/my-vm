//go:build windows

package hostmedia

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

type archiveOutput struct {
	bytes.Buffer
	limit, lines, maxLines int
	cancel                 context.CancelFunc
}

func (b *archiveOutput) Write(p []byte) (int, error) {
	lines := b.lines + bytes.Count(p, []byte{'\n'})
	if len(p) > b.limit-b.Len() || b.maxLines > 0 && lines > b.maxLines {
		b.cancel()
		return 0, errors.New("增强光盘列表或诊断输出超过安全上限")
	}
	b.lines = lines
	return b.Buffer.Write(p)
}

func archiveCommand(args ...string) ([]byte, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &archiveOutput{limit: 16 << 20, cancel: cancel}
	if len(args) > 0 && args[0] == "-tf" {
		output.maxLines = 20000
	}
	diagnostic := &archiveOutput{limit: 64 << 10, cancel: cancel}
	command := exec.CommandContext(ctx, filepath.Join(system, "tar.exe"), args...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	command.Stdout, command.Stderr = output, diagnostic
	if err = command.Run(); err != nil {
		return nil, fmt.Errorf("读取通用增强光盘失败（需要 Windows 自带 tar.exe）: %w: %s", err, strings.TrimSpace(diagnostic.String()))
	}
	return output.Bytes(), nil
}
