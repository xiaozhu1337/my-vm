//go:build windows

package vm

import (
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
