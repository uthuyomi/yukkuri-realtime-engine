//go:build windows

package whispercpp

import (
	"os/exec"
	"syscall"
	"unsafe"
)

// A job handle is owned by the Engine. Windows kills its worker even on abrupt
// Engine termination; the child is not allowed to spawn additional processes.
var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var createJob = kernel32.NewProc("CreateJobObjectW")
var setJob = kernel32.NewProc("SetInformationJobObject")
var assignJob = kernel32.NewProc("AssignProcessToJobObject")

type jobBasic struct {
	ProcessTime, JobTime         int64
	Flags                        uint32
	MinWorkingSet, MaxWorkingSet uintptr
	ActiveProcesses              uint32
	Affinity                     uintptr
	Priority, Scheduling         uint32
}
type jobIO struct{ ReadOps, WriteOps, OtherOps, ReadBytes, WriteBytes, OtherBytes uint64 }
type jobExtended struct {
	Basic                                                      jobBasic
	IO                                                         jobIO
	ProcessMemory, JobMemory, PeakProcessMemory, PeakJobMemory uintptr
}

func prepareChild(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
func containChild(c *exec.Cmd) (func(), error) {
	handle, _, err := createJob.Call(0, 0)
	if handle == 0 {
		return nil, err
	}
	release := func() { _ = syscall.CloseHandle(syscall.Handle(handle)) }
	info := jobExtended{}
	info.Basic.Flags = 0x2000 | 0x8
	info.Basic.ActiveProcesses = 1
	ok, _, err := setJob.Call(handle, 9, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if ok == 0 {
		release()
		return nil, err
	}
	process, e := syscall.OpenProcess(0x0100|0x0001, false, uint32(c.Process.Pid))
	if e != nil {
		release()
		return nil, e
	}
	defer syscall.CloseHandle(process)
	ok, _, err = assignJob.Call(handle, uintptr(process))
	if ok == 0 {
		release()
		return nil, err
	}
	return release, nil
}
