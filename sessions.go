package main

import (
	"fmt"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

// procInfo is one row of the process table.
type procInfo struct {
	PID    uint32
	Parent uint32
	Exe    string // image name, e.g. "Discord.exe"
}

// processTable snapshots all processes (toolhelp).
func processTable() (map[uint32]procInfo, error) {
	snap, err := syscall.CreateToolhelp32Snapshot(th32csSnapProcess, 0)
	if err != nil {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer syscall.CloseHandle(snap)
	table := map[uint32]procInfo{}
	var pe syscall.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = syscall.Process32First(snap, &pe); err == nil; err = syscall.Process32Next(snap, &pe) {
		table[pe.ProcessID] = procInfo{PID: pe.ProcessID, Parent: pe.ParentProcessID, Exe: syscall.UTF16ToString(pe.ExeFile[:])}
	}
	if err != nil && err != syscall.ERROR_NO_MORE_FILES {
		return nil, err
	}
	return table, nil
}

// hasAncestorIn reports whether any ancestor of pid is in set.
func hasAncestorIn(table map[uint32]procInfo, pid uint32, set map[uint32]bool) bool {
	cur := pid
	for depth := 0; depth < 64; depth++ {
		p, ok := table[cur]
		if !ok || p.Parent == 0 || p.Parent == cur {
			return false
		}
		if set[p.Parent] {
			return true
		}
		cur = p.Parent
	}
	return false
}

// rootPIDs returns the processes named exe whose parent is not also exe,
// i.e. the top of each process tree (Discord.exe spawns several Discord.exe).
func rootPIDs(table map[uint32]procInfo, exe string) []uint32 {
	var out []uint32
	for _, p := range table {
		if !strings.EqualFold(p.Exe, exe) {
			continue
		}
		if parent, ok := table[p.Parent]; ok && strings.EqualFold(parent.Exe, exe) {
			continue
		}
		out = append(out, p.PID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// audioSession is one render session seen by the audio engine.
type audioSession struct {
	PID    uint32
	State  uint32
	Device string
}

// listRenderSessions enumerates the sessions of every active render endpoint.
// Sessions with PID 0 (system sounds) are skipped.
func listRenderSessions() ([]audioSession, error) {
	en, err := newDeviceEnumerator()
	if err != nil {
		return nil, err
	}
	defer en.Release()
	col, err := en.EnumAudioEndpoints(eRender, deviceStateActive)
	if err != nil {
		return nil, err
	}
	defer col.Release()
	n, err := col.GetCount()
	if err != nil {
		return nil, err
	}
	var out []audioSession
	for i := uint32(0); i < n; i++ {
		dev, err := col.Item(i)
		if err != nil {
			return nil, err
		}
		name, _ := dev.FriendlyName()
		mgr, err := dev.ActivateSessionManager()
		dev.Release()
		if err != nil {
			continue
		}
		sessions, err := mgr.GetSessionEnumerator()
		if err != nil {
			mgr.Release()
			continue
		}
		cnt, _ := sessions.GetCount()
		for j := int32(0); j < cnt; j++ {
			s, err := sessions.GetSession(j)
			if err != nil {
				continue
			}
			state, _ := s.GetState()
			if s2, err := s.Control2(); err == nil {
				if pid, err := s2.GetProcessId(); err == nil && pid != 0 {
					out = append(out, audioSession{PID: pid, State: state, Device: name})
				}
				s2.Release()
			}
			s.Release()
		}
		sessions.Release()
		mgr.Release()
	}
	return out, nil
}

func sessionStateName(s uint32) string {
	switch s {
	case audioSessionStateActive:
		return "active"
	case audioSessionStateInactive:
		return "inactive"
	case audioSessionStateExpired:
		return "expired"
	}
	return fmt.Sprint(s)
}

// cmdSessions prints who is producing audio right now and where.
func cmdSessions() error {
	sessions, err := listRenderSessions()
	if err != nil {
		return err
	}
	table, err := processTable()
	if err != nil {
		return err
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].PID < sessions[j].PID })
	fmt.Printf("  %-7s %-32s %-9s %s\n", "pid", "process", "state", "device")
	for _, s := range sessions {
		exe := "?"
		if p, ok := table[s.PID]; ok {
			exe = p.Exe
		}
		fmt.Printf("  %-7d %-32s %-9s %s\n", s.PID, exe, sessionStateName(s.State), s.Device)
	}
	return nil
}
