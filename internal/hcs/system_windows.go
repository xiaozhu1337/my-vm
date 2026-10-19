//go:build windows

package hcs

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var compute = windows.NewLazySystemDLL("computecore.dll")
var memory = windows.NewLazySystemDLL("ole32.dll").NewProc("CoTaskMemFree")

func wide(s string) *uint16 { return windows.StringToUTF16Ptr(s) }
func resultText(p *uint16) string {
	if p == nil {
		return ""
	}
	defer memory.Call(uintptr(unsafe.Pointer(p)))
	return windows.UTF16PtrToString(p)
}
func operationText(p *uint16) string {
	if p == nil {
		return ""
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(p)))
	return windows.UTF16PtrToString(p)
}
func check(op string, hr uintptr, detail string) error {
	if int32(hr) >= 0 {
		return nil
	}
	return fmt.Errorf("%s: HRESULT 0x%08X %s", op, uint32(hr), detail)
}

// Each operation owns its native handle until completion, including cancellation.
func operation(name string, system uintptr, options *uint16) (string, error) {
	op, _, _ := compute.NewProc("HcsCreateOperation").Call(0, 0)
	if op == 0 {
		return "", fmt.Errorf("HcsCreateOperation 失败")
	}
	defer compute.NewProc("HcsCloseOperation").Call(op)
	hr, _, _ := compute.NewProc(name).Call(system, op, uintptr(unsafe.Pointer(options)))
	if err := check(name, hr, ""); err != nil {
		return "", err
	}
	return waitOperation(name, op)
}
func waitOperation(name string, op uintptr) (string, error) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var text *uint16
		hr, _, _ := compute.NewProc("HcsWaitForOperationResult").Call(op, 1000, uintptr(unsafe.Pointer(&text)))
		detail := operationText(text)
		if uint32(hr) != uint32(windows.HCS_E_OPERATION_TIMEOUT) {
			return detail, check(name, hr, detail)
		}
		if time.Now().After(deadline) {
			compute.NewProc("HcsCancelOperation").Call(op)
			return "", fmt.Errorf("%s 超时，已取消操作", name)
		}
	}
}

type System struct {
	mu     sync.Mutex
	handle uintptr
	id     uintptr
	done   chan struct{}
	once   sync.Once
}

var systemsMu sync.Mutex
var systems = map[uintptr]*System{}
var systemID atomic.Uint64
var systemCallback = syscall.NewCallback(func(event *struct {
	Type      uint32
	Data      *uint16
	Operation uintptr
}, context uintptr) uintptr {
	if event.Type == 1 {
		systemsMu.Lock()
		s := systems[context]
		systemsMu.Unlock()
		if s != nil {
			s.once.Do(func() { close(s.done) })
		}
	}
	return 0
})

func (s *System) Done() <-chan struct{} { return s.done }
func Create(id string, configuration any) (*System, error) {
	data, err := json.Marshal(configuration)
	if err != nil {
		return nil, err
	}
	op, _, _ := compute.NewProc("HcsCreateOperation").Call(0, 0)
	if op == 0 {
		return nil, fmt.Errorf("HcsCreateOperation 失败")
	}
	defer compute.NewProc("HcsCloseOperation").Call(op)
	var handle uintptr
	hr, _, _ := compute.NewProc("HcsCreateComputeSystem").Call(uintptr(unsafe.Pointer(wide(id))), uintptr(unsafe.Pointer(wide(string(data)))), op, 0, uintptr(unsafe.Pointer(&handle)))
	if err = check("HcsCreateComputeSystem", hr, ""); err == nil {
		_, err = waitOperation("HcsCreateComputeSystem", op)
	}
	if err != nil {
		if handle != 0 {
			compute.NewProc("HcsCloseComputeSystem").Call(handle)
		}
		return nil, err
	}
	s := &System{handle: handle, id: uintptr(systemID.Add(1)), done: make(chan struct{})}
	systemsMu.Lock()
	systems[s.id] = s
	systemsMu.Unlock()
	hr, _, _ = compute.NewProc("HcsSetComputeSystemCallback").Call(handle, 0, s.id, systemCallback)
	if err = check("HcsSetComputeSystemCallback", hr, ""); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *System) Action(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		return fmt.Errorf("虚拟机已关闭")
	}
	var options *uint16
	if name == "HcsShutDownComputeSystem" {
		options = wide(`{"Mechanism":"IntegrationService","Type":"Shutdown","Force":false}`)
	}
	_, err := operation(name, s.handle, options)
	return err
}
func (s *System) Modify(resource string, settings any) error {
	return s.ModifyRequest(resource, "Update", settings)
}
func (s *System) ModifyRequest(resource, requestType string, settings any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		return fmt.Errorf("虚拟机已关闭")
	}
	data, err := json.Marshal(map[string]any{"ResourcePath": resource, "RequestType": requestType, "Settings": settings})
	if err != nil {
		return err
	}
	op, _, _ := compute.NewProc("HcsCreateOperation").Call(0, 0)
	if op == 0 {
		return fmt.Errorf("HcsCreateOperation 失败")
	}
	defer compute.NewProc("HcsCloseOperation").Call(op)
	hr, _, _ := compute.NewProc("HcsModifyComputeSystem").Call(s.handle, op, uintptr(unsafe.Pointer(wide(string(data)))), 0)
	if err = check("HcsModifyComputeSystem", hr, ""); err != nil {
		return err
	}
	_, err = waitOperation("HcsModifyComputeSystem", op)
	return err
}
func (s *System) Properties() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		return nil, fmt.Errorf("虚拟机已关闭")
	}
	op, _, _ := compute.NewProc("HcsCreateOperation").Call(0, 0)
	if op == 0 {
		return nil, fmt.Errorf("HcsCreateOperation 失败")
	}
	defer compute.NewProc("HcsCloseOperation").Call(op)
	hr, _, _ := compute.NewProc("HcsGetComputeSystemProperties").Call(s.handle, op, uintptr(unsafe.Pointer(wide(`{}`))))
	if err := check("HcsGetComputeSystemProperties", hr, ""); err != nil {
		return nil, err
	}
	data, err := waitOperation("HcsGetComputeSystemProperties", op)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal([]byte(data), &result)
	return result, err
}
func (s *System) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle != 0 {
		compute.NewProc("HcsCloseComputeSystem").Call(s.handle)
		s.handle = 0
	}
	systemsMu.Lock()
	delete(systems, s.id)
	systemsMu.Unlock()
	s.once.Do(func() { close(s.done) })
}
func PrepareFile(id, path, create string) error {
	if create != "" {
		hr, _, _ := compute.NewProc(create).Call(uintptr(unsafe.Pointer(wide(path))))
		if err := check(create, hr, ""); err != nil {
			return err
		}
	}
	hr, _, _ := compute.NewProc("HcsGrantVmAccess").Call(uintptr(unsafe.Pointer(wide(id))), uintptr(unsafe.Pointer(wide(path))))
	return check("HcsGrantVmAccess", hr, path)
}
