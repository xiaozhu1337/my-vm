//go:build windows

package vm

import (
	"fmt"
	"golang.org/x/sys/windows"
	"my-vm/internal/sharing"
	"path/filepath"
)

func Configuration(root string, m Machine, endpoint string) (map[string]any, error) {
	if err := requireHCS(m); err != nil {
		return nil, err
	}
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	pipe := `\\.\pipe\my-vm-` + m.ID
	connection := func(name string) map[string]any {
		return map[string]any{"NamedPipe": pipe + name, "AccessSids": []string{user.User.Sid.String()}}
	}
	attachments := map[string]any{"0": map[string]any{"Type": "VirtualDisk", "Path": DiskPath(root, m)}}
	if m.ISO != "" {
		attachments["1"] = map[string]any{"Type": "Iso", "Path": m.ISO}
	}
	if m.DriverISO != "" {
		attachments["2"] = map[string]any{"Type": "Iso", "Path": m.DriverISO}
	}
	devices := map[string]any{
		"VideoMonitor":      map[string]any{"HorizontalResolution": 1280, "VerticalResolution": 720, "ConnectionOptions": connection(".basic")},
		"EnhancedModeVideo": map[string]any{"ConnectionOptions": connection(".enhanced")},
		"Keyboard":          map[string]any{}, "Mouse": map[string]any{},
		"Scsi":            map[string]any{"my-vm-scsi": map[string]any{"Attachments": attachments}},
		"NetworkAdapters": map[string]any{endpoint: map[string]any{"EndpointId": endpoint}},
	}
	if len(m.Shares) > 0 {
		devices["HvSocket"] = map[string]any{"HvSocketConfig": map[string]any{"ServiceTable": map[string]any{sharing.ServiceGUID(): map[string]any{"AllowWildcardBinds": false, "BindSecurityDescriptor": "D:P(A;;FA;;;SY)(A;;FA;;;BA)", "ConnectSecurityDescriptor": "D:P(A;;FA;;;SY)(A;;FA;;;BA)"}}}}
	}
	directory := filepath.Dir(DiskPath(root, m))
	return map[string]any{
		"SchemaVersion":                     map[string]int{"Major": 2, "Minor": 5},
		"Owner":                             "MyVM",
		"ShouldTerminateOnLastHandleClosed": true,
		"VirtualMachine": map[string]any{
			"Services":        map[string]any{"Shutdown": map[string]any{}},
			"StopOnReset":     false,
			"Chipset":         map[string]any{"Uefi": map[string]any{"Console": "Default"}},
			"ComputeTopology": map[string]any{"Memory": map[string]any{"SizeInMB": m.MemoryMiB, "AllowOvercommit": m.DynamicMemory}, "Processor": map[string]any{"Count": m.CPUs}},
			"Devices":         devices,
			"GuestState":      map[string]any{"GuestStateFilePath": filepath.Join(directory, "guest.vmgs"), "RuntimeStateFilePath": filepath.Join(directory, "runtime.vmrs")},
		},
	}, nil
}
func basicPipe(m Machine) string { return fmt.Sprintf(`\\.\pipe\my-vm-%s.basic`, m.ID) }
