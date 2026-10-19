//go:build windows

package guestagent

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func sessionName(target string) (string, *windows.SecurityAttributes, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", nil, err
	}
	var session uint32
	if err = windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil {
		return "", nil, err
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", nil, err
	}
	if resolved, e := filepath.EvalSymlinks(absolute); e == nil {
		absolute = resolved
	}
	sid := user.User.Sid.String()
	security, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;" + sid + ")")
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(absolute))))
	name := fmt.Sprintf(`Local\MyVMGuestTools.%s.%d.%x`, sid, session, digest[:12])
	return name, &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: security}, nil
}

// LockInstallation prevents one installer from stopping another one's preparation process.
func LockInstallation(target string) (func(), error) {
	path, err := windows.UTF16PtrFromString(target + ".install.lock")
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return nil, errors.New("另一个增强安装正在运行，请等待完成")
	}
	if err != nil {
		return nil, err
	}
	return func() { windows.CloseHandle(handle) }, nil
}

func acquire(ctx context.Context, cancel context.CancelFunc) (func(), bool, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, false, err
	}
	name, attributes, err := sessionName(executable)
	if err != nil {
		return nil, false, err
	}
	mutex, err := windows.CreateMutex(attributes, false, windows.StringToUTF16Ptr(name+".Agent"))
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(mutex)
		return func() {}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	event, err := windows.CreateEvent(attributes, 1, 0, windows.StringToUTF16Ptr(name+".Stop"))
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(mutex)
		return nil, false, err
	}
	if err = windows.ResetEvent(event); err != nil {
		windows.CloseHandle(event)
		windows.CloseHandle(mutex)
		return nil, false, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			state, e := windows.WaitForSingleObject(event, 100)
			if e != nil || state == windows.WAIT_OBJECT_0 {
				cancel()
				return
			}
		}
	}()
	return func() {
		cancel()
		<-done
		windows.CloseHandle(event)
		windows.CloseHandle(mutex)
	}, true, nil
}

// Stop only targets the installed executable owned by this user in this desktop session.
func Stop(target string) (bool, error) { return stop(target, 18389) }

func stop(target string, port uint16) (bool, error) {
	resolved, err := filepath.EvalSymlinks(target)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	target = resolved
	processes, err := matchingProcesses(target, port)
	if err != nil {
		return false, err
	}
	defer func() {
		for _, process := range processes {
			windows.CloseHandle(process)
		}
	}()
	if len(processes) == 0 {
		return false, nil
	}
	name, _, err := sessionName(target)
	if err != nil {
		return false, err
	}
	event, eventErr := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, windows.StringToUTF16Ptr(name+".Stop"))
	if eventErr == nil {
		err = windows.SetEvent(event)
		windows.CloseHandle(event)
		if err != nil {
			return false, err
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			allStopped := true
			for _, process := range processes {
				state, e := windows.WaitForSingleObject(process, 0)
				if e != nil {
					return true, e
				}
				allStopped = allStopped && state == windows.WAIT_OBJECT_0
			}
			if allStopped {
				return true, nil
			}
			time.Sleep(25 * time.Millisecond)
		}
	} else if !errors.Is(eventErr, windows.ERROR_FILE_NOT_FOUND) {
		return false, eventErr
	}
	// Older agents have no stop event; terminate only the handles verified above.
	for _, process := range processes {
		state, e := windows.WaitForSingleObject(process, 0)
		if e != nil {
			return true, e
		}
		if state == windows.WAIT_OBJECT_0 {
			continue
		}
		if err = windows.TerminateProcess(process, 0); err != nil {
			return true, fmt.Errorf("无法退出旧增强代理，请先从原会话退出: %w", err)
		}
		if state, err = windows.WaitForSingleObject(process, 3000); err != nil || state != windows.WAIT_OBJECT_0 {
			return true, fmt.Errorf("旧增强代理未退出: %v", err)
		}
	}
	return true, nil
}

func matchingProcesses(target string, port uint16) ([]windows.Handle, error) {
	owners, err := listenerOwners(port)
	if err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	var ownSession uint32
	if err = windows.ProcessIdToSessionId(uint32(os.Getpid()), &ownSession); err != nil {
		return nil, err
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	var result []windows.Handle
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if !owners[entry.ProcessID] || entry.ProcessID == uint32(os.Getpid()) || !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), filepath.Base(target)) {
			continue
		}
		var session uint32
		if windows.ProcessIdToSessionId(entry.ProcessID, &session) != nil || session != ownSession {
			continue
		}
		process, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, entry.ProcessID)
		if e != nil {
			if errors.Is(e, windows.ERROR_INVALID_PARAMETER) {
				continue
			}
			err = e
			break
		}
		var path [32768]uint16
		size := uint32(len(path))
		e = windows.QueryFullProcessImageName(process, 0, &path[0], &size)
		if e != nil || !strings.EqualFold(windows.UTF16ToString(path[:size]), target) {
			windows.CloseHandle(process)
			continue
		}
		var token windows.Token
		if e = windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); e != nil {
			windows.CloseHandle(process)
			err = e
			break
		}
		owner, e := token.GetTokenUser()
		token.Close()
		if e != nil || !owner.User.Sid.Equals(user.User.Sid) {
			windows.CloseHandle(process)
			continue
		}
		result = append(result, process)
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return result, nil
	}
	for _, process := range result {
		windows.CloseHandle(process)
	}
	return nil, err
}

func listenerOwners(port uint16) (map[uint32]bool, error) {
	proc := windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")
	var size uint32
	code, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, windows.AF_INET, 3, 0)
	if code != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) && code != 0 {
		return nil, windows.Errno(code)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if size < 4 || size > 16<<20 {
			return nil, errors.New("无效的 TCP 监听表大小")
		}
		data := make([]byte, size)
		code, _, _ = proc.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&size)), 0, windows.AF_INET, 3, 0)
		if code == uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
			continue
		}
		if code != 0 {
			return nil, windows.Errno(code)
		}
		count := int(binary.LittleEndian.Uint32(data[:4]))
		if count > (len(data)-4)/24 {
			return nil, errors.New("无效的 TCP 监听表")
		}
		result := map[uint32]bool{}
		for i := 0; i < count; i++ {
			row := data[4+i*24 : 4+(i+1)*24]
			if binary.BigEndian.Uint16(row[8:10]) == port {
				result[binary.LittleEndian.Uint32(row[20:24])] = true
			}
		}
		return result, nil
	}
	return nil, errors.New("TCP 监听表持续变化，请重试")
}
