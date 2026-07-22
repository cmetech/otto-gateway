//go:build windows

package main

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func init() {
	desktopRunningFn = platformDesktopRunning
	desktopProcessIDsFn = platformDesktopProcessIDs
}

func platformDesktopRunning(candidate desktopCandidate) (bool, error) {
	pids, err := platformDesktopProcessIDs(candidate)
	if err != nil {
		return false, err
	}
	return len(pids) > 0, nil
}

func platformDesktopProcessIDs(candidate desktopCandidate) ([]uint32, error) {
	processes, err := enumerateWindowsProcesses()
	if err != nil {
		return nil, err
	}
	return matchingWindowsProcessIDs(candidate.ExecutablePath, processes), nil
}

func enumerateWindowsProcesses() ([]desktopProcess, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("create process snapshot: %w", err)
	}
	defer windows.CloseHandle(snapshot) //nolint:errcheck // nothing actionable after enumeration

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil, nil
		}
		return nil, fmt.Errorf("start process enumeration: %w", err)
	}

	var processes []desktopProcess
	for {
		if executablePath, ok := queryWindowsProcessPath(entry.ProcessID); ok {
			processes = append(processes, desktopProcess{PID: entry.ProcessID, ExecutablePath: executablePath})
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, fmt.Errorf("continue process enumeration: %w", err)
		}
	}
	return processes, nil
}

func queryWindowsProcessPath(pid uint32) (string, bool) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(process) //nolint:errcheck // handle is closed on every return path

	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", false
	}
	return windows.UTF16ToString(buffer[:size]), true
}
