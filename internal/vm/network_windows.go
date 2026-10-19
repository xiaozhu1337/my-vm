//go:build windows

package vm

import (
	"errors"
	"fmt"
	"strings"

	"my-vm/internal/hcs"
)

// Windows reserves this ID for Default Switch; WSL also creates ICS networks without DHCP.
const defaultSwitchID = "c08cb7b8-9b3c-408e-8e30-5e16a3aeb444"

func selectNetwork(networks []hcs.Network, switchID string) (hcs.Network, error) {
	wanted := strings.Trim(switchID, "{}")
	if wanted == "" {
		wanted = defaultSwitchID
	}
	for _, network := range networks {
		if strings.EqualFold(strings.Trim(network.ID, "{}"), wanted) && (switchID != "" || network.Type == "ICS") {
			return network, nil
		}
	}
	if switchID != "" {
		return hcs.Network{}, fmt.Errorf("找不到指定交换机 %s，请刷新交换机并重新选择", switchID)
	}
	return hcs.Network{}, fmt.Errorf("未找到 Hyper-V Default Switch；请先启用默认交换机或指定有 DHCP 服务的现有交换机")
}

// Callbacks keep endpoint ownership and failure cleanup testable without HCN.
func createNetworkEndpoints(machine Machine, networks []hcs.Network, owner string,
	create func(string, string, ...string) (*hcs.Endpoint, error),
	closeEndpoint func(*hcs.Endpoint) error,
) (endpoints []*hcs.Endpoint, err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, closeNetworkEndpoints(endpoints, closeEndpoint))
			endpoints = nil
		}
	}()
	for _, adapter := range machine.enabledNetworkAdapters() {
		selected, selectErr := selectNetwork(networks, adapter.SwitchID)
		if selectErr != nil {
			return endpoints, selectErr
		}
		endpoint, createErr := create(selected.ID, owner, adapter.MACAddress)
		if createErr != nil {
			return endpoints, createErr
		}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints, nil
}

func closeNetworkEndpoints(endpoints []*hcs.Endpoint, closeEndpoint func(*hcs.Endpoint) error) error {
	var failures []error
	for _, endpoint := range endpoints {
		if err := closeEndpoint(endpoint); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
