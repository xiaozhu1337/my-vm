//go:build windows

package hostmedia

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type Copy struct {
	Source      string `yaml:"source"`
	Destination string `yaml:"destination"`
	Overwrite   bool   `yaml:"overwrite"`
}
type Adapter struct {
	Name           string `yaml:"device_name"`
	Version        string `yaml:"version"`
	HardwareID     string `yaml:"hardware_id"`
	Driver         string `yaml:"driver"`
	Copies         []Copy `yaml:"copies"`
	directory, inf string
}

type windowsManifest struct {
	Architecture string       `yaml:"architecture"`
	Adapters     []Adapter    `yaml:"adapters"`
	Files        []fileRecord `yaml:"files"`
}

// InstalledAdapters resolves only the packages actually bound to present PCI display devices.
func InstalledAdapters() ([]Adapter, error) {
	class := windows.GUID{Data1: 0x4d36e968, Data2: 0xe325, Data3: 0x11ce, Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18}}
	set, err := windows.SetupDiGetClassDevsEx(&class, "", 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return nil, err
	}
	defer set.Close()
	locate := windows.NewLazySystemDLL("setupapi.dll").NewProc("SetupGetInfDriverStoreLocationW")
	var result []Adapter
	for index := 0; ; index++ {
		info, err := set.EnumDeviceInfo(index)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			break
		}
		if err != nil {
			return nil, err
		}
		value, err := set.DeviceRegistryProperty(info, windows.SPDRP_DRIVER)
		if err != nil {
			return nil, err
		}
		driverKey, ok := value.(string)
		if !ok {
			return nil, errors.New("显示驱动注册表标识无效")
		}
		adapter, err := readAdapter(driverKey)
		if err != nil {
			return nil, err
		}
		if adapter.HardwareID == "" {
			continue
		}
		system, err := windows.GetWindowsDirectory()
		if err != nil {
			return nil, err
		}
		inf := filepath.Join(system, "INF", adapter.inf)
		buffer := make([]uint16, 32768)
		var required uint32
		success, _, callErr := locate.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(inf))), 0, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), uintptr(unsafe.Pointer(&required)))
		if success == 0 {
			return nil, fmt.Errorf("定位宿主驱动 %s: %w", inf, callErr)
		}
		adapter.inf = windows.UTF16ToString(buffer)
		adapter.directory = filepath.Dir(adapter.inf)
		adapter.Driver = filepath.Base(adapter.directory)
		for i := range adapter.Copies {
			adapter.Copies[i].Source = "FileRepository/" + adapter.Driver + "/" + adapter.Copies[i].Source
		}
		for _, existing := range result {
			if strings.EqualFold(existing.Name, adapter.Name) && !reflect.DeepEqual(existing, adapter) {
				return nil, fmt.Errorf("同名显卡对应不同驱动，无法安全选择: %s", adapter.Name)
			}
		}
		result = append(result, adapter)
	}
	if len(result) == 0 {
		return nil, errors.New("未找到已安装驱动的物理 PCI 显卡")
	}
	return result, nil
}

func readAdapter(path string) (Adapter, error) {
	var adapter Adapter
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Class\`+path, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return adapter, err
	}
	defer key.Close()
	adapter.HardwareID, _, err = key.GetStringValue("MatchingDeviceId")
	if err != nil {
		return adapter, err
	}
	if !strings.HasPrefix(strings.ToLower(adapter.HardwareID), `pci\`) {
		return Adapter{}, nil
	}
	for name, target := range map[string]*string{"DriverDesc": &adapter.Name, "DriverVersion": &adapter.Version, "InfPath": &adapter.inf} {
		*target, _, err = key.GetStringValue(name)
		if err != nil {
			return adapter, fmt.Errorf("读取宿主显卡 %s: %w", name, err)
		}
	}
	if !safeRelative(adapter.inf) || strings.Contains(adapter.inf, "/") {
		return adapter, errors.New("宿主驱动 INF 名称无效")
	}
	for _, entry := range []struct {
		name, dir string
		overwrite bool
	}{
		{"CopyToVmWhenNewer", "System32", false}, {"CopyToVmWhenNewerWow64", "SysWOW64", false},
		{"CopyToVmOverwrite", "System32", true}, {"CopyToVmOverwriteWow64", "SysWOW64", true},
	} {
		child, err := registry.OpenKey(key, entry.name, registry.QUERY_VALUE)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			continue
		}
		if err != nil {
			return adapter, err
		}
		copies, err := readCopies(child, entry.dir, entry.overwrite)
		child.Close()
		if err != nil {
			return adapter, err
		}
		adapter.Copies = append(adapter.Copies, copies...)
	}
	return adapter, nil
}

func readCopies(key registry.Key, directory string, overwrite bool) ([]Copy, error) {
	names, err := key.ReadValueNames(-1)
	if err != nil {
		return nil, err
	}
	var result []Copy
	for _, name := range names {
		_, kind, err := key.GetValue(name, nil)
		if err != nil {
			return nil, err
		}
		var parts []string
		switch kind {
		case registry.SZ:
			value, _, err := key.GetStringValue(name)
			if err != nil {
				return nil, err
			}
			parts = []string{value}
		case registry.MULTI_SZ:
			parts, _, err = key.GetStringsValue(name)
			if err != nil {
				return nil, err
			}
		default:
			return nil, errors.New("宿主 CopyToVm 声明类型无效")
		}
		if len(parts) < 1 || len(parts) > 2 {
			return nil, errors.New("宿主 CopyToVm 声明长度无效")
		}
		source := strings.ReplaceAll(parts[0], `\`, "/")
		destination := filepath.Base(filepath.FromSlash(source))
		if len(parts) == 2 && parts[1] != "" {
			destination = parts[1]
		}
		if !safeRelative(source) || !safeRelative(destination) || strings.Contains(destination, "/") {
			return nil, errors.New("宿主 CopyToVm 路径无效")
		}
		result = append(result, Copy{Source: source, Destination: directory + "/" + destination, Overwrite: overwrite})
	}
	return result, nil
}

func windowsRuntimeFile(name string) bool {
	name = strings.ToLower(filepath.Base(name))
	// GPU-PV uses the host KMD; Windows guests do not need Linux tools or installers.
	switch filepath.Ext(name) {
	case ".sys", ".msi", ".pnf", ".log", ".so":
		return false
	}
	return !strings.Contains(name, ".so.") && name != "nvidia-smi" && name != "nvidia-ngx-updater"
}

func collectWindows(stage *staging, adapters []Adapter) error {
	manifest := windowsManifest{Architecture: "amd64", Adapters: adapters}
	seen := map[string]bool{}
	for _, adapter := range adapters {
		key := strings.ToLower(adapter.Driver)
		if seen[key] {
			continue
		}
		seen[key] = true
		err := filepath.WalkDir(adapter.directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := rejectReparse(path); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if !windowsRuntimeFile(entry.Name()) {
				return nil
			}
			relative, err := filepath.Rel(adapter.directory, path)
			if err != nil {
				return err
			}
			record, err := stage.copy(path, "windows-gpu/FileRepository/"+adapter.Driver+"/"+filepath.ToSlash(relative))
			if err != nil {
				return err
			}
			record.Name = strings.TrimPrefix(record.Name, "windows-gpu/")
			manifest.Files = append(manifest.Files, record)
			return nil
		})
		if err != nil {
			return fmt.Errorf("复制宿主 GPU 驱动 %s: %w", adapter.Name, err)
		}
	}
	for _, adapter := range adapters {
		for _, mapping := range adapter.Copies {
			found := false
			for _, file := range manifest.Files {
				if strings.EqualFold(file.Name, mapping.Source) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("宿主驱动声明的文件不存在: %s", mapping.Source)
			}
		}
	}
	return stage.writeYAML("windows-gpu/manifest.yaml", manifest)
}
