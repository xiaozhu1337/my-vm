//go:build windows

// Package hostmedia prepares private enhancement media from this host's installed GPU drivers.
package hostmedia

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"my-vm/internal/media"
)

// Prepare leaves the original image untouched and publishes no image on failure.
func Prepare(source, platform, directory string) (target string, err error) {
	if platform != "windows" && platform != "linux" {
		return "", errors.New("增强光盘平台无效")
	}
	adapters, err := InstalledAdapters()
	if err != nil {
		return "", fmt.Errorf("读取宿主 GPU 驱动: %w", err)
	}
	before := make(map[string]string)
	for _, adapter := range adapters {
		digest, err := fileDigest(adapter.inf)
		if err != nil {
			return "", err
		}
		before[adapter.inf] = digest
	}
	stage, err := newStaging(filepath.Join(directory, "__tmp"))
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, stage.cleanup())
		if err != nil && target != "" {
			err = errors.Join(err, os.Remove(target))
			target = ""
		}
	}()
	if err = stage.extract(source); err != nil {
		return "", err
	}
	if platform == "windows" {
		err = collectWindows(stage, adapters)
	} else {
		err = collectLinux(stage, adapters)
	}
	if err != nil {
		return "", err
	}
	after, err := InstalledAdapters()
	if err != nil {
		return "", err
	}
	if !reflect.DeepEqual(adapters, after) {
		return "", errors.New("宿主显卡驱动在准备期间发生变化，请重试")
	}
	for path, digest := range before {
		current, err := fileDigest(path)
		if err != nil {
			return "", err
		}
		if current != digest {
			return "", errors.New("宿主显卡 INF 在准备期间发生变化，请重试")
		}
	}
	reservation, err := os.CreateTemp(directory, "guest-tools-*.iso")
	if err != nil {
		return "", err
	}
	target = reservation.Name()
	if err = reservation.Close(); err != nil {
		return target, err
	}
	if err = os.Remove(target); err != nil {
		return target, err
	}
	if err = media.CreateISO(stage.root, target); err != nil {
		return target, fmt.Errorf("创建宿主匹配的 GPU 增强光盘: %w", err)
	}
	return target, nil
}
