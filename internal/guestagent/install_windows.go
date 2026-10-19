//go:build windows

package guestagent

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func SetStartup(target string, enabled bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if enabled {
		return key.SetStringValue("MyVMGuestTools", `"`+target+`"`)
	}
	err = key.DeleteValue("MyVMGuestTools")
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}
func Startup(target string) (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer key.Close()
	value, _, err := key.GetStringValue("MyVMGuestTools")
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	return value == `"`+target+`"`, err
}

// Upgrade keeps the previous executable until the replacement is listening and startup is saved.
func Upgrade(source, target string, prepare func(string) error, start func(string) (func() error, error), commit func() error) (err error) {
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	unlock, err := LockInstallation(target)
	if err != nil {
		return err
	}
	defer unlock()
	previous := target + ".previous.exe"
	if _, e := os.Stat(previous); e == nil {
		if _, e = os.Stat(target); e == nil {
			return fmt.Errorf("上次升级未完成，已保留原程序 %s；请退出代理并恢复后重试", previous)
		}
		if !os.IsNotExist(e) {
			return e
		}
		if e = move(previous, target); e != nil {
			return fmt.Errorf("恢复原程序 %s: %w", previous, e)
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	staged := filepath.Join(filepath.Dir(target), ".guest-update-"+rand.Text()+".exe")
	if err = stage(source, staged); err != nil {
		return err
	}
	defer os.Remove(staged)
	if err = prepare(staged); err != nil {
		return err
	}
	sourceInfo, e := os.Stat(source)
	if e != nil {
		return e
	}
	oldInfo, oldErr := os.Stat(target)
	if oldErr != nil && !os.IsNotExist(oldErr) {
		return oldErr
	}
	same := oldErr == nil && os.SameFile(sourceInfo, oldInfo)
	running, err := Stop(target)
	if err != nil {
		return err
	}
	backed, published, committed := false, false, false
	var stopNew func() error
	defer func() {
		if err == nil || committed {
			return
		}
		if stopNew != nil {
			if e := stopNew(); e != nil {
				err = errors.Join(err, e)
				return
			}
		}
		if published {
			if e := os.Remove(target); e != nil && !os.IsNotExist(e) {
				err = errors.Join(err, fmt.Errorf("无法移除失败的新程序；原程序保留在 %s: %w", previous, e))
				return
			}
		}
		if backed {
			if e := move(previous, target); e != nil {
				err = errors.Join(err, fmt.Errorf("恢复失败，原程序保留在 %s: %w", previous, e))
				return
			}
		}
		if running {
			if e := restartPrevious(target); e != nil {
				err = errors.Join(err, fmt.Errorf("原代理重新启动失败: %w", e))
			}
		}
	}()
	if !same {
		if oldErr == nil {
			if err = move(target, previous); err != nil {
				return err
			}
			backed = true
		}
		if err = move(staged, target); err != nil {
			return err
		}
		published = true
	}
	if stopNew, err = start(target); err != nil {
		return err
	}
	if err = commit(); err != nil {
		return err
	}
	committed = true
	if backed {
		if err = os.Remove(previous); err != nil {
			return fmt.Errorf("新代理已运行，但旧程序清理失败，保留在 %s: %w", previous, err)
		}
	}
	return nil
}
func stage(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	if err == nil {
		err = output.Sync()
	}
	err = errors.Join(err, output.Close())
	if err != nil {
		os.Remove(target)
	}
	return err
}
func move(source, target string) error {
	from, e := windows.UTF16PtrFromString(source)
	if e != nil {
		return e
	}
	to, e := windows.UTF16PtrFromString(target)
	if e != nil {
		return e
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		e = windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
		if e == nil {
			return nil
		}
		if !errors.Is(e, windows.ERROR_ACCESS_DENIED) && !errors.Is(e, windows.ERROR_SHARING_VIOLATION) || time.Now().After(deadline) {
			return e
		}
		time.Sleep(50 * time.Millisecond)
	}
}
