//go:build windows

package networkmgr

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"my-vm/internal/hcs"
)

var hcn = windows.NewLazySystemDLL("computenetwork.dll")
var freeMemory = windows.NewLazySystemDLL("ole32.dll").NewProc("CoTaskMemFree")

func free(p *uint16) {
	if p != nil {
		freeMemory.Call(uintptr(unsafe.Pointer(p)))
	}
}
func nativeError(name string, hr uintptr, result *uint16) error {
	defer free(result)
	if int32(hr) >= 0 {
		return nil
	}
	detail := ""
	if result != nil {
		detail = windows.UTF16PtrToString(result)
	}
	return fmt.Errorf("%s: HRESULT 0x%08X %s", name, uint32(hr), detail)
}
func listNetworks() ([]Network, error) {
	networks, err := hcs.Networks()
	if err != nil {
		return nil, err
	}
	output := make([]Network, 0, len(networks))
	for _, n := range networks {
		prefixes, err := networkPrefixes(n.ID)
		if err != nil {
			return nil, err
		}
		kind := n.Type
		if kind == "" {
			kind = "NAT"
		}
		output = append(output, Network{ID: n.ID, Name: n.Name, Type: kind, Subnet: strings.Join(prefixes, ",")})
	}
	return output, nil
}
func networkPrefixes(id string) ([]string, error) {
	guid, err := windows.GUIDFromString("{" + strings.Trim(id, "{}") + "}")
	if err != nil {
		return nil, err
	}
	var handle uintptr
	var result, properties *uint16
	hr, _, _ := hcn.NewProc("HcnOpenNetwork").Call(uintptr(unsafe.Pointer(&guid)), uintptr(unsafe.Pointer(&handle)), uintptr(unsafe.Pointer(&result)))
	if err = nativeError("HcnOpenNetwork", hr, result); err != nil {
		return nil, err
	}
	defer hcn.NewProc("HcnCloseNetwork").Call(handle)
	query, _ := windows.UTF16PtrFromString(`{"SchemaVersion":{"Major":2,"Minor":0}}`)
	result = nil
	hr, _, _ = hcn.NewProc("HcnQueryNetworkProperties").Call(handle, uintptr(unsafe.Pointer(query)), uintptr(unsafe.Pointer(&properties)), uintptr(unsafe.Pointer(&result)))
	defer free(properties)
	if err = nativeError("HcnQueryNetworkProperties", hr, result); err != nil {
		return nil, err
	}
	var data struct {
		Ipams []struct {
			Subnets []struct {
				Prefix string `json:"IpAddressPrefix"`
			}
		}
	}
	if properties == nil {
		return nil, fmt.Errorf("HCN 未返回网络属性")
	}
	if err = json.Unmarshal([]byte(windows.UTF16PtrToString(properties)), &data); err != nil {
		return nil, err
	}
	var prefixes []string
	for _, ipam := range data.Ipams {
		for _, subnet := range ipam.Subnets {
			if subnet.Prefix != "" {
				prefixes = append(prefixes, subnet.Prefix)
			}
		}
	}
	return prefixes, nil
}
func hostPrefixes() ([]netip.Prefix, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var output []netip.Prefix
	for _, adapter := range interfaces {
		addresses, err := adapter.Addrs()
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			if p, err := netip.ParsePrefix(address.String()); err == nil && p.Addr().Is4() {
				output = append(output, p.Masked())
			}
		}
	}
	return output, nil
}
func networkSettings(name, cidr string) ([]byte, error) {
	prefix, err := validateNetwork(name, cidr)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"Name": name, "Type": "NAT", "SchemaVersion": map[string]int{"Major": 2, "Minor": 0}, "Ipams": []any{map[string]any{"Type": "Static", "Subnets": []any{map[string]any{"IpAddressPrefix": cidr, "Routes": []any{map[string]string{"NextHop": prefix.Addr().Next().String(), "DestinationPrefix": "0.0.0.0/0"}}}}}}})
}
func createNetwork(id, name, cidr string) error {
	data, err := networkSettings(name, cidr)
	if err != nil {
		return err
	}
	guid, err := windows.GUIDFromString("{" + id + "}")
	if err != nil {
		return err
	}
	settings, err := windows.UTF16PtrFromString(string(data))
	if err != nil {
		return err
	}
	var handle uintptr
	var result *uint16
	hr, _, _ := hcn.NewProc("HcnCreateNetwork").Call(uintptr(unsafe.Pointer(&guid)), uintptr(unsafe.Pointer(settings)), uintptr(unsafe.Pointer(&handle)), uintptr(unsafe.Pointer(&result)))
	if handle != 0 {
		defer hcn.NewProc("HcnCloseNetwork").Call(handle)
	}
	return nativeError("HcnCreateNetwork", hr, result)
}
func removeNetwork(id string) error {
	guid, err := windows.GUIDFromString("{" + id + "}")
	if err != nil {
		return err
	}
	var result *uint16
	hr, _, _ := hcn.NewProc("HcnDeleteNetwork").Call(uintptr(unsafe.Pointer(&guid)), uintptr(unsafe.Pointer(&result)))
	return nativeError("HcnDeleteNetwork", hr, result)
}
func hasEndpoints(id string) (bool, error) {
	filter, _ := json.Marshal(map[string]string{"VirtualNetwork": id})
	data, _ := json.Marshal(map[string]any{"SchemaVersion": map[string]int{"Major": 2, "Minor": 0}, "Filter": string(filter)})
	query, _ := windows.UTF16PtrFromString(string(data))
	var result, properties *uint16
	hr, _, _ := hcn.NewProc("HcnEnumerateEndpoints").Call(uintptr(unsafe.Pointer(query)), uintptr(unsafe.Pointer(&properties)), uintptr(unsafe.Pointer(&result)))
	defer free(properties)
	if err := nativeError("HcnEnumerateEndpoints", hr, result); err != nil {
		return false, err
	}
	if properties == nil {
		return false, fmt.Errorf("HCN 未返回端点列表")
	}
	var ids []string
	if err := json.Unmarshal([]byte(windows.UTF16PtrToString(properties)), &ids); err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}
