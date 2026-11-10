//go:build windows

// Package lifetime prevents Chrome from outliving a forcibly stopped helper.
package lifetime

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

// Retain the unnamed job's handle for the entire process lifetime. Windows
// closes it even if MyGo uses TerminateProcess, then kills this job's children.
// The helper's ordinary shutdown still pauses playback and closes Chrome first.
var job windows.Handle

func Guard() error {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(handle, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(handle)
		return err
	}
	if err = windows.AssignProcessToJobObject(handle, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(handle)
		return err
	}
	job = handle
	return nil
}
