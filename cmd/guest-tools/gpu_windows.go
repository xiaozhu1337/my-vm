//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/windows"
)

type windowsGPUCopy struct {
	Source, Destination string
	Overwrite           bool
}
type windowsGPUAdapter struct {
	Name            string `yaml:"device_name"`
	Version, Driver string
	HardwareID      string `yaml:"hardware_id"`
	Copies          []windowsGPUCopy
}
type windowsGPUFile struct{ Name, SHA256 string }
type windowsGPUManifest struct {
	Architecture string
	Adapters     []windowsGPUAdapter
	Files        []windowsGPUFile
}

func gpuRelative(name string) bool {
	if name == "" || strings.ContainsAny(name, `\:`) || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
	}
	return true
}

func regularGPUPath(root, name string) (string, error) {
	if !gpuRelative(name) {
		return "", errors.New("GPU 文件路径无效")
	}
	current := root
	for _, part := range strings.Split(name, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(current))
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return "", errors.New("GPU 路径不能经过重解析点")
		}
	}
	info, err := os.Stat(current)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("GPU 载荷不是普通文件")
	}
	return current, nil
}

func gpuDigest(path string) (string, error) {
	source, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer source.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, source); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readWindowsGPU(media string) (windowsGPUManifest, string, error) {
	var manifest windowsGPUManifest
	root := filepath.Join(media, "windows-gpu")
	path, err := regularGPUPath(root, "manifest.yaml")
	if err != nil {
		return manifest, root, err
	}
	file, err := os.Open(path)
	if err != nil {
		return manifest, root, err
	}
	defer file.Close()
	decoder := yaml.NewDecoder(io.LimitReader(file, 2<<20))
	decoder.KnownFields(true)
	if err = decoder.Decode(&manifest); err != nil {
		return manifest, root, err
	}
	if manifest.Architecture != "amd64" || len(manifest.Adapters) == 0 || len(manifest.Files) == 0 {
		return manifest, root, errors.New("Windows GPU 介质清单不完整")
	}
	files, drivers := map[string]bool{}, map[string]bool{}
	for _, adapter := range manifest.Adapters {
		if !gpuRelative(adapter.Driver) || strings.Contains(adapter.Driver, "/") || adapter.Name == "" || adapter.Version == "" {
			return manifest, root, errors.New("GPU 驱动标识无效")
		}
		drivers[strings.ToLower(adapter.Driver)] = true
	}
	for _, record := range manifest.Files {
		parts := strings.Split(record.Name, "/")
		digest, err := hex.DecodeString(record.SHA256)
		if !gpuRelative(record.Name) || len(parts) < 3 || parts[0] != "FileRepository" || !drivers[strings.ToLower(parts[1])] || files[strings.ToLower(record.Name)] || err != nil || len(digest) != sha256.Size {
			return manifest, root, errors.New("GPU 载荷路径、校验值或重复项无效")
		}
		files[strings.ToLower(record.Name)] = true
	}
	for _, adapter := range manifest.Adapters {
		destinations := map[string]bool{}
		for _, record := range adapter.Copies {
			parts := strings.Split(record.Destination, "/")
			if !gpuRelative(record.Destination) || len(parts) != 2 || parts[0] != "System32" && parts[0] != "SysWOW64" || !strings.HasPrefix(strings.ToLower(record.Source), strings.ToLower("FileRepository/"+adapter.Driver+"/")) || !files[strings.ToLower(record.Source)] || destinations[strings.ToLower(record.Destination)] {
				return manifest, root, errors.New("GPU 补充文件映射无效")
			}
			destinations[strings.ToLower(record.Destination)] = true
		}
	}
	return manifest, root, nil
}

func installWindowsGPU(media, directory, name string) (bool, error) {
	manifest, root, err := readWindowsGPU(media)
	if err != nil {
		return false, err
	}
	var selected *windowsGPUAdapter
	for i := range manifest.Adapters {
		if strings.EqualFold(manifest.Adapters[i].Name, name) {
			selected = &manifest.Adapters[i]
			break
		}
	}
	if selected == nil {
		return false, fmt.Errorf("增强盘中没有匹配 %s 的宿主驱动，请在当前宿主重新构建增强盘", name)
	}
	expected := map[string]string{}
	var records []windowsGPUFile
	for _, file := range manifest.Files {
		if !strings.HasPrefix(strings.ToLower(file.Name), strings.ToLower("FileRepository/"+selected.Driver+"/")) {
			continue
		}
		path, err := regularGPUPath(root, file.Name)
		if err != nil {
			return false, err
		}
		digest, err := gpuDigest(path)
		if err != nil {
			return false, err
		}
		if digest != file.SHA256 {
			return false, fmt.Errorf("GPU 文件校验失败: %s", file.Name)
		}
		expected[strings.ToLower(file.Name)] = digest
		records = append(records, file)
	}
	if len(records) == 0 {
		return false, errors.New("匹配的 GPU 驱动包为空")
	}
	parent := filepath.Join(directory, "System32", "HostDriverStore", "FileRepository")
	if err := os.MkdirAll(parent, 0755); err != nil {
		return false, err
	}
	destination := filepath.Join(parent, selected.Driver)
	changed := false
	if _, err := os.Lstat(destination); err == nil {
		for _, file := range records {
			relative := file.Name[len("FileRepository/"+selected.Driver+"/"):]
			path, err := regularGPUPath(destination, relative)
			if err != nil {
				return false, err
			}
			digest, err := gpuDigest(path)
			if err != nil {
				return false, err
			}
			if digest != file.SHA256 {
				return false, fmt.Errorf("已有 GPU 包内容不匹配，未覆盖: %s", path)
			}
		}
	} else if !os.IsNotExist(err) {
		return false, err
	} else {
		if err := stageGPUDriver(root, parent, destination, records); err != nil {
			return false, err
		}
		changed = true
	}
	updated, err := installGPUSupplements(root, directory, selected.Copies, expected)
	return changed || updated, err
}

func stageGPUDriver(root, parent, destination string, records []windowsGPUFile) (err error) {
	temporary, err := os.MkdirTemp(parent, ".my-vm-gpu-")
	if err != nil {
		return err
	}
	var files, dirs []string
	dirs = append(dirs, temporary)
	defer func() {
		if err == nil {
			return
		}
		for i := len(files) - 1; i >= 0; i-- {
			err = errors.Join(err, os.Remove(files[i]))
		}
		for i := len(dirs) - 1; i >= 0; i-- {
			err = errors.Join(err, os.Remove(dirs[i]))
		}
	}()
	for _, record := range records {
		parts := strings.Split(record.Name, "/")[2:]
		folder := temporary
		for _, part := range parts[:len(parts)-1] {
			folder = filepath.Join(folder, part)
			if _, stat := os.Stat(folder); os.IsNotExist(stat) {
				if err = os.Mkdir(folder, 0755); err != nil {
					return err
				}
				dirs = append(dirs, folder)
			}
		}
		target := filepath.Join(folder, parts[len(parts)-1])
		files = append(files, target)
		if err = copyGPUFile(filepath.Join(root, filepath.FromSlash(record.Name)), target, record.SHA256); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = windows.MoveFileEx(windows.StringToUTF16Ptr(temporary), windows.StringToUTF16Ptr(destination), windows.MOVEFILE_WRITE_THROUGH)
		if err == nil || !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) || time.Now().After(deadline) {
			return err
		}
		// Antivirus may briefly hold a freshly copied driver without delete sharing.
		time.Sleep(100 * time.Millisecond)
	}
}

func copyGPUFile(source, target, digest string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(output, hash), input)
	if err == nil && hex.EncodeToString(hash.Sum(nil)) != digest {
		err = errors.New("GPU 文件复制校验失败")
	}
	if err == nil {
		err = output.Sync()
	}
	return errors.Join(err, output.Close())
}

func gpuFileVersion(path string) (uint64, bool) {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size < 52 || size > 16<<20 {
		return 0, false
	}
	data := make([]byte, size)
	if windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&data[0])) != nil {
		return 0, false
	}
	var value *[13]uint32
	var length uint32
	if windows.VerQueryValue(unsafe.Pointer(&data[0]), `\`, unsafe.Pointer(&value), &length) != nil || length < 52 || value[0] != 0xFEEF04BD {
		return 0, false
	}
	return uint64(value[2])<<32 | uint64(value[3]), true
}

func gpuSupplementNeeded(source, target, digest string, overwrite bool) (bool, error) {
	if existing, err := gpuDigest(target); err == nil {
		if existing == digest {
			return false, nil
		}
		if !overwrite {
			src, srcOK := gpuFileVersion(source)
			dst, dstOK := gpuFileVersion(target)
			if srcOK && dstOK && dst > src {
				return false, nil
			}
			if srcOK == dstOK && src == dst {
				a, e1 := os.Stat(source)
				b, e2 := os.Stat(target)
				if e1 != nil || e2 != nil {
					return false, errors.Join(e1, e2)
				}
				if !a.ModTime().After(b.ModTime()) {
					return false, nil
				}
			} else if !srcOK && dstOK {
				return false, nil
			}
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return true, nil
}

type gpuCleanupError struct{ error }

func (e *gpuCleanupError) Unwrap() error { return e.error }

type gpuSupplement struct {
	target, pending, backup string
	handle                  windows.Handle
	installed               bool
}

func installGPUSupplements(root, directory string, records []windowsGPUCopy, expected map[string]string) (changed bool, err error) {
	var plans []*gpuSupplement
	remove := func(path string) error {
		if path == "" {
			return nil
		}
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() {
		var cleanup error
		for _, plan := range plans {
			if plan.handle != 0 {
				windows.CloseHandle(plan.handle)
			}
			cleanup = errors.Join(cleanup, remove(plan.pending))
			if !plan.installed {
				cleanup = errors.Join(cleanup, remove(plan.backup))
			}
		}
		if cleanup != nil {
			if changed {
				err = &gpuCleanupError{cleanup}
			} else {
				err = errors.Join(err, cleanup)
			}
		}
	}()
	for _, record := range records {
		digest, ok := expected[strings.ToLower(record.Source)]
		if !ok {
			return false, errors.New("GPU 补充文件缺少校验值")
		}
		source := filepath.Join(root, filepath.FromSlash(record.Source))
		target := filepath.Join(directory, filepath.FromSlash(record.Destination))
		needed, err := gpuSupplementNeeded(source, target, digest, record.Overwrite)
		if err != nil {
			return false, err
		}
		if !needed {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return false, err
		}
		reservation, err := os.CreateTemp(filepath.Dir(target), ".my-vm-gpu-")
		if err != nil {
			return false, err
		}
		plan := &gpuSupplement{target: target, pending: reservation.Name()}
		plans = append(plans, plan)
		if err := reservation.Close(); err != nil {
			return false, err
		}
		if err := os.Remove(plan.pending); err != nil {
			return false, err
		}
		if err := copyGPUFile(source, plan.pending, digest); err != nil {
			return false, err
		}
		if _, err := os.Lstat(target); err == nil {
			plan.handle, err = windows.CreateFile(windows.StringToUTF16Ptr(target), windows.DELETE,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				plan.handle = 0
				return false, fmt.Errorf("GPU 文件被占用，请关闭客户机程序后重试: %s: %w", target, err)
			}
			backup := plan.pending + ".previous"
			if err := os.Link(target, backup); err != nil {
				return false, err
			}
			plan.backup = backup
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	for _, plan := range plans {
		if plan.handle != 0 {
			if err := windows.CloseHandle(plan.handle); err != nil {
				return false, err
			}
			plan.handle = 0
		}
	}
	for _, plan := range plans {
		flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
		if plan.backup != "" {
			flags |= windows.MOVEFILE_REPLACE_EXISTING
		}
		if err := windows.MoveFileEx(windows.StringToUTF16Ptr(plan.pending), windows.StringToUTF16Ptr(plan.target), flags); err != nil {
			failure := fmt.Errorf("GPU 补充文件替换失败: %s: %w", plan.target, err)
			for i := len(plans) - 1; i >= 0; i-- {
				old := plans[i]
				if !old.installed {
					continue
				}
				var rollback error
				if old.backup == "" {
					rollback = os.Remove(old.target)
				} else {
					rollback = windows.MoveFileEx(windows.StringToUTF16Ptr(old.backup), windows.StringToUTF16Ptr(old.target), windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
				}
				if rollback != nil {
					failure = errors.Join(failure, fmt.Errorf("恢复 GPU 文件失败，原文件保留于 %s: %w", old.backup, rollback))
				} else {
					old.installed = false
				}
			}
			return false, failure
		}
		plan.installed = true
	}
	for _, plan := range plans {
		plan.installed = false
	}
	return len(plans) > 0, nil
}

func copyGPUSupplement(source, target, digest string, overwrite bool) (bool, error) {
	return installGPUSupplements(filepath.Dir(source), filepath.Dir(target),
		[]windowsGPUCopy{{Source: filepath.Base(source), Destination: filepath.Base(target), Overwrite: overwrite}},
		map[string]string{strings.ToLower(filepath.Base(source)): digest})
}
