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
	args := []string{"-name", "guest=" + strings.ReplaceAll(m.Name, ",", ",,"), "-machine", "q35", "-accel", m.Accelerator, "-smp", strconv.Itoa(m.CPUs), "-m", strconv.Itoa(m.MemoryMiB),
		"-display", "sdl", "-vga", "std", "-device", "ich9-ahci,id=sata",
		"-blockdev", block(map[string]any{"driver": "qcow2", "node-name": "osdisk", "file": map[string]any{"driver": "file", "filename": filepath.ToSlash(DiskPath(root, m))}}),
		"-device", "ide-hd,drive=osdisk,bus=sata.0", "-qmp", "tcp:" + qmpAddress}
	if m.ISO != "" {
		args = append(args, "-blockdev", block(map[string]any{"driver": "raw", "node-name": "cdrom", "read-only": true, "file": map[string]any{"driver": "file", "filename": filepath.ToSlash(m.ISO)}}), "-device", "ide-cd,drive=cdrom,bus=sata.1", "-boot", "order=dc,menu=on")
	} else {
		args = append(args, "-boot", "order=c,menu=on")
	}
	backend := "user,id=net0"
	if m.Network == "bridge" {
		backend = fmt.Sprintf("tap,id=net0,ifname=%s,script=no,downscript=no", m.TAP)
	}
	args = append(args, "-netdev", backend, "-device", "e1000,netdev=net0,mac=52:54:"+m.ID[:2]+":"+m.ID[2:4]+":"+m.ID[4:6]+":"+m.ID[6:8])
	return args, nil
}
