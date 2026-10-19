//go:build windows

package guestops

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var resumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

func runCommand(command *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return err
	}
	// Assign before the first instruction, so a fast child cannot escape the job.
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED, HideWindow: true}
	command.Cancel = func() error {
		err := windows.TerminateJobObject(job, 1)
		// Cancellation may arrive while the suspended process is not yet assigned.
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		return err
	}
	if err = command.Start(); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(command.Process.Pid))
	if err == nil {
		defer windows.CloseHandle(process)
		err = windows.AssignProcessToJobObject(job, process)
		if err == nil {
			status, _, _ := resumeProcess.Call(uintptr(process))
			if int32(status) < 0 {
				err = fmt.Errorf("NtResumeProcess: 0x%x", status)
			}
		}
	}
	if err != nil {
		command.Process.Kill()
		command.Wait()
		return err
	}
	return command.Wait()
}
