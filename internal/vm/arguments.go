package vm

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

func Arguments(root string, m Machine, qmpAddress string) ([]string, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	block := func(v any) string { data, _ := json.Marshal(v); return string(data) }
	// Auxiliary virtual consoles create hidden SDL windows that can be mistaken for the guest display.
	args := []string{"-name", "guest=" + strings.ReplaceAll(m.Name, ",", ",,"), "-machine", "q35", "-accel", m.Accelerator, "-smp", strconv.Itoa(m.CPUs), "-m", strconv.Itoa(m.MemoryMiB),
		"-display", "sdl", "-monitor", "none", "-serial", "none", "-parallel", "none", "-device", "ich9-ahci,id=sata",
		"-blockdev", block(map[string]any{"driver": "qcow2", "node-name": "osdisk", "file": map[string]any{"driver": "file", "filename": filepath.ToSlash(DiskPath(root, m))}}),
		"-device", "ide-hd,drive=osdisk,bus=sata.0", "-qmp", "tcp:" + qmpAddress}
	if m.Display == "virtio" {
		args = append(args, "-vga", "none", "-device", "virtio-vga,edid=on,max_outputs=1")
	} else {
		args = append(args, "-vga", "std")
	}
	if m.ISO != "" {
		args = append(args, "-blockdev", block(map[string]any{"driver": "raw", "node-name": "cdrom", "read-only": true, "file": map[string]any{"driver": "file", "filename": filepath.ToSlash(m.ISO)}}), "-device", "ide-cd,drive=cdrom,bus=sata.1", "-boot", "order=dc,menu=on")
	} else {
		args = append(args, "-boot", "order=c,menu=on")
	}
	if m.DriverISO != "" {
		args = append(args, "-blockdev", block(map[string]any{"driver": "raw", "node-name": "drivers", "read-only": true, "file": map[string]any{"driver": "file", "filename": filepath.ToSlash(m.DriverISO)}}), "-device", "ide-cd,drive=drivers,bus=sata.2")
	}
	backend := "user,id=net0"
	if m.Network == "bridge" {
		backend = fmt.Sprintf("tap,id=net0,ifname=%s,script=no,downscript=no", m.TAP)
	}
	args = append(args, "-netdev", backend, "-device", "e1000,netdev=net0,mac=52:54:"+m.ID[:2]+":"+m.ID[2:4]+":"+m.ID[4:6]+":"+m.ID[6:8])
	return args, nil
}
