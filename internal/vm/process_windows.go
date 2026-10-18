package vm

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func hideConsole(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }

type processJob struct{ handle windows.Handle }

func (j processJob) Close() error { return windows.CloseHandle(j.handle) }

func startProcess(cmd *exec.Cmd) (processJob, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return processJob{}, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return processJob{}, err
	}
	if err = cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return processJob{}, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		_ = windows.CloseHandle(process)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = windows.CloseHandle(job)
		return processJob{}, fmt.Errorf("无法为 QEMU 配置进程退出保护: %w", err)
	}
	return processJob{handle: job}, nil
}
