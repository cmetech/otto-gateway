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
	entries, err := enumerateWindowsProcessEntries()
	if err != nil {
		return nil, err
	}
	return windowsCandidateProcessIDs(candidate, entries, queryWindowsProcessPath, windowsProcessGone)
}

func enumerateWindowsProcessEntries() ([]desktopProcessEntry, error) {
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

	var entries []desktopProcessEntry
	for {
		entries = append(entries, desktopProcessEntry{
			PID:       entry.ProcessID,
			ImageName: windows.UTF16ToString(entry.ExeFile[:]),
		})
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, fmt.Errorf("continue process enumeration: %w", err)
		}
	}
	return entries, nil
}

func queryWindowsProcessPath(pid uint32) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(process) //nolint:errcheck // handle is closed on every return path

	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:size]), nil
}

func windowsProcessGone(err error) bool {
	// With a nonzero PID sourced from the snapshot, ERROR_INVALID_PARAMETER
	// means the process disappeared before OpenProcess. Fail closed on every
	// other error, including ERROR_ACCESS_DENIED and query-handle failures.
	return errors.Is(err, windows.ERROR_INVALID_PARAMETER)
}
