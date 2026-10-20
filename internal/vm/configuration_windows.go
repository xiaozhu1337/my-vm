//go:build windows

package vm

import (
	"fmt"
	"golang.org/x/sys/windows"
	"my-vm/internal/checkpoint"
	"my-vm/internal/sharing"
	"path/filepath"
	"strconv"
	"strings"
)

func Configuration(root string, m Machine, endpoint string) (map[string]any, error) {
	var endpoints []string
	if endpoint != "" && m.Network != "none" {
		endpoints = []string{endpoint}
	}
	return ConfigurationWithEndpoints(root, m, endpoints)
}

// ConfigurationWithEndpoints accepts one endpoint per enabled NIC, primary first.
func ConfigurationWithEndpoints(root string, m Machine, endpoints []string) (map[string]any, error) {
	if err := requireHCS(m); err != nil {
		return nil, err
	}
	settings, err := Load(filepath.Join(root, "library.yaml"))
	if err != nil {
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
	return configurationWithEndpoints(root, m, endpoints, user.User.Sid.String(), settings.SideChannelMitigations)
}

// Keep schema construction pure so device validation never needs native services in tests.
func configurationWithEndpoints(root string, m Machine, endpoints []string, sid string, sideChannelMitigations bool) (map[string]any, error) {
	if err := requireHCS(m); err != nil {
		return nil, err
	}
	if len(endpoints) != len(m.enabledNetworkAdapters()) {
		return nil, fmt.Errorf("网络端点数量必须与已启用网卡数量一致")
	}
	pipe := `\\.\pipe\my-vm-` + m.ID
	connection := func(name string) map[string]any {
		return map[string]any{"NamedPipe": pipe + name, "AccessSids": []string{sid}}
	}
	storage, err := checkpoint.Load(filepath.Dir(DiskPath(root, m)))
	if err != nil {
		return nil, err
	}
	if storage.Pending != nil {
		return nil, fmt.Errorf("磁盘分叉尚未完成，不能生成启动配置")
	}
	attachments := map[string]any{"0": map[string]any{"Type": "VirtualDisk", "Path": storage.Path(storage.Active)}}
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
		"Scsi": map[string]any{"my-vm-scsi": map[string]any{"Attachments": attachments}},
	}
	if len(endpoints) > 0 {
		adapters := map[string]any{}
		for _, endpoint := range endpoints {
			if strings.TrimSpace(endpoint) == "" {
				return nil, fmt.Errorf("网络端点不能为空")
			}
			if _, exists := adapters[endpoint]; exists {
				return nil, fmt.Errorf("网络端点不能重复")
			}
			adapters[endpoint] = map[string]any{"EndpointId": endpoint}
		}
		devices["NetworkAdapters"] = adapters
	}
	if len(m.COMPorts) > 0 {
		ports := map[string]any{}
		for _, port := range m.COMPorts {
			ports[strconv.Itoa(port.Number-1)] = map[string]any{"NamedPipe": port.Pipe}
		}
		devices["ComPorts"] = ports
	}
	if len(m.Shares) > 0 || m.IsLinux() {
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
			"ComputeTopology": map[string]any{"Memory": map[string]any{"SizeInMB": m.MemoryMiB, "AllowOvercommit": m.DynamicMemory}, "Processor": map[string]any{"Count": m.CPUs, "DisableSpeculationControls": !sideChannelMitigations}},
			"Devices":         devices,
			"GuestState":      map[string]any{"GuestStateFilePath": filepath.Join(directory, "guest.vmgs"), "RuntimeStateFilePath": filepath.Join(directory, "runtime.vmrs")},
		},
	}, nil
}
func basicPipe(m Machine) string { return fmt.Sprintf(`\\.\pipe\my-vm-%s.basic`, m.ID) }
