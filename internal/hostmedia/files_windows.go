//go:build windows

package hostmedia

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unsafe"

	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/windows"
)

type fileRecord struct {
	Name   string `yaml:"name"`
	SHA256 string `yaml:"sha256"`
}
type staging struct {
	root  string
	paths map[string]bool
}

func safeRelative(name string) bool {
	if name == "" || !filepath.IsLocal(filepath.FromSlash(name)) || strings.ContainsAny(name, "\\:*?\"<>|\x00\r\n") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
	}
	return true
}
func rejectReparse(path string) error {
	attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(path))
	if err != nil {
		return fmt.Errorf("读取 GPU 文件属性 %s: %w", path, err)
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("GPU 路径包含重解析点: %s", path)
	}
	return nil
}
func newStaging(parent string) (*staging, error) {
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(parent, "gpu-media-")
	if err != nil {
		return nil, err
	}
	return &staging{root: root, paths: map[string]bool{root: true}}, nil
}
func (s *staging) track(name string) string {
	path := filepath.Join(s.root, filepath.FromSlash(name))
	for current := path; current != s.root; current = filepath.Dir(current) {
		s.paths[current] = true
	}
	return path
}
func (s *staging) cleanup() error {
	paths := make([]string, 0, len(s.paths))
	for path := range s.paths {
		paths = append(paths, path)
	}
	slices.SortFunc(paths, func(a, b string) int { return len(b) - len(a) })
	var result error
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			result = errors.Join(result, err)
		}
	}
	return result
}
func (s *staging) copy(source, name string) (record fileRecord, err error) {
	if !safeRelative(name) {
		return record, errors.New("GPU 目标路径无效")
	}
	if err = rejectReparse(source); err != nil {
		return record, err
	}
	input, err := os.Open(source)
	if err != nil {
		return record, err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return record, err
	}
	if !info.Mode().IsRegular() {
		return record, errors.New("GPU 来源不是普通文件")
	}
	target := s.track(name)
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return record, err
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return record, err
	}
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(output, hash), input)
	if err == nil {
		err = output.Sync()
	}
	err = errors.Join(err, output.Close())
	if err != nil {
		return record, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	actual, err := fileDigest(target)
	if err != nil {
		return record, err
	}
	if digest != actual {
		return record, errors.New("宿主 GPU 文件复制校验失败")
	}
	after, err := input.Stat()
	if err != nil {
		return record, err
	}
	if info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return record, errors.New("宿主 GPU 文件在复制期间发生变化，请重试")
	}
	if err = os.Chtimes(target, info.ModTime(), info.ModTime()); err != nil {
		return record, err
	}
	return fileRecord{Name: name, SHA256: digest}, nil
}
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func (s *staging) writeYAML(name string, value any) error {
	if !safeRelative(name) {
		return errors.New("GPU 清单路径无效")
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	path := s.track(name)
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte("# Generated from this host; file paths and SHA-256 checksums describe the payload.\n"), data...), 0600)
}

func archiveCommand(args ...string) ([]byte, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	command := exec.Command(filepath.Join(system, "tar.exe"), args...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	data, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("读取通用增强光盘失败（需要 Windows 自带 tar.exe）: %w: %s", err, strings.TrimSpace(string(data)))
	}
	return data, nil
}
func decodeArchiveNames(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	// Windows tar writes redirected file names using the system ANSI code page.
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	codePage, _, _ := kernel.NewProc("GetACP").Call()
	convert := kernel.NewProc("MultiByteToWideChar")
	size, _, err := convert.Call(codePage, 8, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 0, 0)
	if size == 0 {
		return "", err
	}
	output := make([]uint16, size)
	size, _, err = convert.Call(codePage, 8, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&output[0])), uintptr(len(output)))
	if size == 0 {
		return "", err
	}
	return windows.UTF16ToString(output), nil
}

func (s *staging) extract(source string) error {
	data, err := archiveCommand("-tf", source)
	if err != nil {
		return err
	}
	listing, err := decodeArchiveNames(data)
	if err != nil {
		return fmt.Errorf("增强光盘文件名编码无效: %w", err)
	}
	names := strings.Split(strings.ReplaceAll(listing, "\r\n", "\n"), "\n")
	if len(names) > 20000 {
		return errors.New("增强光盘包含过多文件")
	}
	for _, name := range names {
		if name == "." || name == "" {
			continue
		}
		name = strings.TrimSuffix(name, "/")
		if !safeRelative(name) {
			return fmt.Errorf("增强光盘路径无效: %q", name)
		}
		s.track(name)
		if name == "windows-gpu" || strings.HasPrefix(name, "windows-gpu/") || strings.HasPrefix(name, "gpu/lib/") || strings.HasPrefix(name, "gpu/drivers/") {
			return errors.New("请使用新版通用增强光盘；其中不能预装其他宿主的 GPU 驱动")
		}
	}
	if _, err = archiveCommand("-xf", source, "-C", s.root, "--no-same-owner", "--no-same-permissions"); err != nil {
		return err
	}
	for path := range s.paths {
		if err = rejectReparse(path); err != nil {
			return err
		}
	}
	return nil
}
