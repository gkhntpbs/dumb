//go:build windows

package ollama

import (
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// attachOwnedProcess confines this exact Ollama process and descendants to an
// unnamed job. Closing our non-inherited handle terminates owned model runners;
// unrelated host processes and pre-existing Ollama instances cannot join it.
func attachOwnedProcess(command *exec.Cmd) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("ollama: create owned process job: %w", err)
	}
	fail := func(err error) (func(), error) { windows.CloseHandle(job); return nil, err }
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return fail(fmt.Errorf("ollama: configure owned process job: %w", err))
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		return fail(fmt.Errorf("ollama: open owned process: %w", err))
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return fail(fmt.Errorf("ollama: assign owned process job: %w", err))
	}
	var once sync.Once
	return func() { once.Do(func() { _ = windows.CloseHandle(job) }) }, nil
}
