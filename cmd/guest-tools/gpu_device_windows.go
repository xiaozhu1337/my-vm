//go:build windows

package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type gpuDevice struct {
	id, name        string
	instance        windows.DEVINST
	status, problem uint32
}

func gpuPVDevices() ([]gpuDevice, error) {
	class := windows.GUID{Data1: 0x4d36e968, Data2: 0xe325, Data3: 0x11ce, Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18}}
	set, err := windows.SetupDiGetClassDevsEx(&class, "", 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return nil, err
	}
	defer set.Close()
	var devices []gpuDevice
	for index := 0; ; index++ {
		info, err := set.EnumDeviceInfo(index)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			break
		}
		if err != nil {
			return nil, err
		}
		hardware, err := set.DeviceRegistryProperty(info, windows.SPDRP_HARDWAREID)
		if err != nil {
			return nil, err
		}
		ids, _ := hardware.([]string)
		matched := false
		for _, id := range ids {
			if strings.HasPrefix(strings.ToUpper(id), `PCI\VEN_1414&DEV_008E`) {
				matched = true
			}
		}
		if !matched {
			continue
		}
		id, err := set.DeviceInstanceID(info)
		if err != nil {
			return nil, err
		}
		friendly, err := set.DeviceRegistryProperty(info, windows.SPDRP_FRIENDLYNAME)
		if err != nil {
			friendly, err = set.DeviceRegistryProperty(info, windows.SPDRP_DEVICEDESC)
		}
		if err != nil {
			return nil, err
		}
		name, _ := friendly.(string)
		device := gpuDevice{id: id, name: name, instance: info.DevInst}
		if err := windows.CM_Get_DevNode_Status(&device.status, &device.problem, device.instance, 0); err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, nil
}

func restartGPU(device gpuDevice) (err error) {
	cfg := windows.NewLazySystemDLL("cfgmgr32.dll")
	check := func(name string, code uintptr) error {
		if code != 0 {
			return fmt.Errorf("%s %s: CONFIGRET 0x%X", name, device.id, uint32(code))
		}
		return nil
	}
	locate := func() error {
		code, _, _ := cfg.NewProc("CM_Locate_DevNodeW").Call(uintptr(unsafe.Pointer(&device.instance)), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(device.id))), 0)
		return check("CM_Locate_DevNodeW", code)
	}
	if err := locate(); err != nil {
		return err
	}
	code, _, _ := cfg.NewProc("CM_Disable_DevNode").Call(uintptr(device.instance), 4)
	if err := check("CM_Disable_DevNode", code); err != nil {
		return err
	}
	disabled, enabled := device.instance, false
	defer func() {
		if !enabled {
			code, _, _ := cfg.NewProc("CM_Enable_DevNode").Call(uintptr(disabled), 0)
			err = errors.Join(err, check("CM_Enable_DevNode recovery", code))
		}
	}()
	if err := locate(); err != nil {
		return err
	}
	code, _, _ = cfg.NewProc("CM_Enable_DevNode").Call(uintptr(device.instance), 0)
	if err := check("CM_Enable_DevNode", code); err != nil {
		return err
	}
	enabled = true
	deadline := time.Now().Add(15 * time.Second)
	var status, problem uint32
	for {
		if err := locate(); err != nil {
			return err
		}
		if err := windows.CM_Get_DevNode_Status(&status, &problem, device.instance, 0); err != nil {
			return err
		}
		if status&8 != 0 && status&0x400 == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if status&0x400 == 0 {
		problem = 0
	}
	return fmt.Errorf("GPU-PV %s 未就绪: 状态 0x%X，设备代码 %d；请在基础会话安装后重启客户机", device.name, status, problem)
}

func prepareWindowsGPU(media string) error {
	devices, err := gpuPVDevices()
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		return nil
	}
	remote, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("GetSystemMetrics").Call(0x1000)
	if remote != 0 {
		return errors.New("Windows GPU-PV 驱动更新需在基础控制台运行；请退出增强会话后重试")
	}
	directory, err := windows.GetWindowsDirectory()
	if err != nil {
		return err
	}
	for _, device := range devices {
		changed, installErr := installWindowsGPU(media, directory, device.name)
		var cleanup *gpuCleanupError
		if installErr != nil && !errors.As(installErr, &cleanup) {
			return fmt.Errorf("准备 Windows GPU-PV %s: %w", device.name, installErr)
		}
		if changed || device.status&8 == 0 || device.status&0x400 != 0 {
			if err := restartGPU(device); err != nil {
				return errors.Join(installErr, err)
			}
		}
		if installErr != nil {
			return fmt.Errorf("GPU-PV 文件已安装并已重载，清理临时备份失败: %w", installErr)
		}
	}
	return nil
}
