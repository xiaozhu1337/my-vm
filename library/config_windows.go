//go:build windows

package library

import (
	"errors"
	"os"
	"path/filepath"
	"slices"

	"my-vm/internal/vm"
)

func cloneConfig(cfg Config) Config {
	cfg.Machines = slices.Clone(cfg.Machines)
	for i := range cfg.Machines {
		m := &cfg.Machines[i]
		m.Shares = slices.Clone(m.Shares)
		m.NetworkAdapters = slices.Clone(m.NetworkAdapters)
		m.COMPorts = slices.Clone(m.COMPorts)
	}
	return cfg
}

func sameConfigFile(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime() == b.ModTime()
}

func configFile(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return info, err
}

// config is called under the service configuration lock. Cached values stay immutable.
func (s *Service) config() (Config, error) {
	path := filepath.Join(s.runtime.Root, "library.yaml")
	for range 3 {
		before, err := configFile(path)
		if err != nil {
			return Config{}, err
		}
		if s.cached != nil && sameConfigFile(s.configFile, before) {
			return cloneConfig(*s.cached), nil
		}
		cfg, err := vm.Load(path)
		if err != nil {
			return Config{}, err
		}
		after, err := configFile(path)
		if err != nil {
			return Config{}, err
		}
		if !sameConfigFile(before, after) {
			continue
		}
		s.cached, s.configFile = &cfg, after
		s.revision++
		return cloneConfig(cfg), nil
	}
	return Config{}, errors.New("配置持续被外部修改，请稍后重试")
}
