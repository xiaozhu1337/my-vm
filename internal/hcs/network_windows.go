//go:build windows

package hcs

import (
	"encoding/json"
	"fmt"
	"golang.org/x/sys/windows"
	"strings"
	"unsafe"
)

var network = windows.NewLazySystemDLL("computenetwork.dll")

type Network struct{ ID, Name, Type string }

func Networks() ([]Network, error) {
	var ids, detail *uint16
	hr, _, _ := network.NewProc("HcnEnumerateNetworks").Call(uintptr(unsafe.Pointer(wide(`{}`))), uintptr(unsafe.Pointer(&ids)), uintptr(unsafe.Pointer(&detail)))
	data, msg := resultText(ids), resultText(detail)
	if err := check("HcnEnumerateNetworks", hr, msg); err != nil {
		return nil, err
	}
	var values []string
	if err := json.Unmarshal([]byte(data), &values); err != nil {
		return nil, err
	}
	result := make([]Network, 0, len(values))
	for _, id := range values {
		guid, err := windows.GUIDFromString("{" + strings.Trim(id, "{}") + "}")
		if err != nil {
			return nil, err
		}
		var handle uintptr
		hr, _, _ = network.NewProc("HcnOpenNetwork").Call(uintptr(unsafe.Pointer(&guid)), uintptr(unsafe.Pointer(&handle)), uintptr(unsafe.Pointer(&detail)))
		msg = resultText(detail)
		detail = nil
		if err = check("HcnOpenNetwork", hr, msg); err != nil {
			return nil, err
		}
		var properties *uint16
		hr, _, _ = network.NewProc("HcnQueryNetworkProperties").Call(handle, uintptr(unsafe.Pointer(wide(`{}`))), uintptr(unsafe.Pointer(&properties)), uintptr(unsafe.Pointer(&detail)))
		data, msg = resultText(properties), resultText(detail)
		detail = nil
		network.NewProc("HcnCloseNetwork").Call(handle)
		if err = check("HcnQueryNetworkProperties", hr, msg); err != nil {
			return nil, err
		}
		var item Network
		if err = json.Unmarshal([]byte(data), &item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

type Endpoint struct{ ID windows.GUID }

func CreateEndpoint(switchID, owner string) (*Endpoint, error) {
	guid, err := windows.GUIDFromString("{" + strings.Trim(switchID, "{}") + "}")
	if err != nil {
		return nil, fmt.Errorf("交换机 ID 无效: %w", err)
	}
	var net, endpoint uintptr
	var detail *uint16
	hr, _, _ := network.NewProc("HcnOpenNetwork").Call(uintptr(unsafe.Pointer(&guid)), uintptr(unsafe.Pointer(&net)), uintptr(unsafe.Pointer(&detail)))
	if err = check("HcnOpenNetwork", hr, resultText(detail)); err != nil {
		return nil, err
	}
	defer network.NewProc("HcnCloseNetwork").Call(net)
	id, err := windows.GenerateGUID()
	if err != nil {
		return nil, err
	}
	settings, _ := json.Marshal(map[string]any{"SchemaVersion": map[string]int{"Major": 2, "Minor": 0}, "Owner": owner, "HostComputeNetwork": switchID})
	detail = nil
	hr, _, _ = network.NewProc("HcnCreateEndpoint").Call(net, uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(wide(string(settings)))), uintptr(unsafe.Pointer(&endpoint)), uintptr(unsafe.Pointer(&detail)))
	if err = check("HcnCreateEndpoint", hr, resultText(detail)); err != nil {
		return nil, err
	}
	network.NewProc("HcnCloseEndpoint").Call(endpoint)
	return &Endpoint{ID: id}, nil
}
func (e *Endpoint) Close() error {
	if e == nil {
		return nil
	}
	var detail *uint16
	hr, _, _ := network.NewProc("HcnDeleteEndpoint").Call(uintptr(unsafe.Pointer(&e.ID)), uintptr(unsafe.Pointer(&detail)))
	return check("HcnDeleteEndpoint", hr, resultText(detail))
}
func (e *Endpoint) String() string { return strings.Trim(e.ID.String(), "{}") }
