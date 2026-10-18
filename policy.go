package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"syscall"
	"unsafe"
)

// Per-application default audio device, i.e. what Windows Settings exposes as
// "App volume and device preferences". Windows keeps it in the undocumented
// WinRT class Windows.Media.Internal.AudioPolicyConfig; the vtable layout below
// follows EarTrumpet (File-New-Project/EarTrumpet, Interop/MMDeviceAPI).

var (
	// Windows 10 21H2+ / Windows 11.
	iidPolicyConfig21H2 = mustGUID("{ab3d4648-e242-459f-b02f-541c70306324}")
	// Older Windows 10 builds.
	iidPolicyConfigDownlevel = mustGUID("{2a59116d-6c4f-45e0-a74f-707e3fef9258}")
)

const (
	policyClass = "Windows.Media.Internal.AudioPolicyConfig"

	// vtable slots: IUnknown(3) + IInspectable(3) + 19 unused methods.
	vtSetPersistedDefaultAudioEndpoint = 25
	vtGetPersistedDefaultAudioEndpoint = 26

	mmdevapiToken       = `\\?\SWD#MMDEVAPI#`
	devInterfaceRender  = "#{e6327cad-dcec-4949-ae8a-991e976a79d2}"
	devInterfaceCapture = "#{2eef81be-33fa-4800-9670-1cd474972c3f}"
)

// policyConfig wraps the activated IAudioPolicyConfigFactory.
type policyConfig struct {
	obj *comObj
}

func newPolicyConfig() (*policyConfig, error) {
	var lastErr error
	for _, iid := range []*GUID{iidPolicyConfig21H2, iidPolicyConfigDownlevel} {
		obj, err := roGetActivationFactory(policyClass, iid)
		if err == nil {
			return &policyConfig{obj: obj}, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("RoGetActivationFactory(%s): %w", policyClass, lastErr)
}

func (p *policyConfig) release() {
	p.obj.Release()
	p.obj = nil
}

// setDefault persists deviceID (an MMDevice endpoint ID, or "" to clear) as the
// default render device for the executable behind pid, for both the console and
// multimedia roles. Windows stores this per executable path, so one live PID
// is enough and the setting survives restarts of that application.
func (p *policyConfig) setDefault(pid uint32, deviceID string) error {
	full := ""
	if deviceID != "" {
		full = mmdevapiToken + deviceID + devInterfaceRender
	}
	h, err := newHString(full)
	if err != nil {
		return err
	}
	defer h.free()
	for _, role := range []uintptr{eMultimedia, eConsole} {
		r, _, _ := syscall.SyscallN(p.obj.lpVtbl[vtSetPersistedDefaultAudioEndpoint], p.obj.ptr(), uintptr(pid), eRender, role, uintptr(h))
		if r != 0 {
			return fmt.Errorf("SetPersistedDefaultAudioEndpoint(pid=%d, role=%d): %w", pid, role, hresult(r))
		}
	}
	return nil
}

// getDefault returns the persisted render endpoint ID for pid ("" if none).
func (p *policyConfig) getDefault(pid uint32) (string, error) {
	var h hstring
	r, _, _ := syscall.SyscallN(p.obj.lpVtbl[vtGetPersistedDefaultAudioEndpoint], p.obj.ptr(), uintptr(pid), eRender, eMultimedia|eConsole, uintptr(unsafe.Pointer(&h)))
	if r != 0 {
		return "", fmt.Errorf("GetPersistedDefaultAudioEndpoint(pid=%d): %w", pid, hresult(r))
	}
	defer h.free()
	s := h.String()
	s = strings.TrimPrefix(s, mmdevapiToken)
	s = strings.TrimSuffix(s, devInterfaceRender)
	s = strings.TrimSuffix(s, devInterfaceCapture)
	return s, nil
}

const th32csSnapProcess = 0x2

// processIDs returns every PID of each running executable whose image name
// matches one of names (case-insensitive). Missing names are reported separately.
// Several PIDs matter because e.g. nvcontainer.exe also runs as SYSTEM, which
// the policy API rejects; we try each until one is accepted.
func processIDs(names []string) (found map[string][]uint32, missing []string, err error) {
	snap, err := syscall.CreateToolhelp32Snapshot(th32csSnapProcess, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer syscall.CloseHandle(snap)
	found = map[string][]uint32{}
	want := map[string]string{}
	for _, n := range names {
		want[strings.ToLower(n)] = n
	}
	var pe syscall.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = syscall.Process32First(snap, &pe); err == nil; err = syscall.Process32Next(snap, &pe) {
		exe := strings.ToLower(syscall.UTF16ToString(pe.ExeFile[:]))
		if orig, ok := want[exe]; ok {
			found[orig] = append(found[orig], pe.ProcessID)
		}
	}
	if err != nil && !errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return nil, nil, err
	}
	for _, n := range names {
		if _, ok := found[n]; !ok {
			missing = append(missing, n)
		}
	}
	return found, missing, nil
}

// RouteResult describes what happened to each app in routeApps.
type RouteResult struct {
	Done    []string         // override applied
	Missing []string         // not running
	Failed  map[string]error // running, but rejected by the policy API
}

// routeApps points every configured app at deviceID ("" clears the override).
//
// The policy API answers E_INVALIDARG for a PID that has never talked to the
// audio service (verified empirically: a process becomes eligible as soon as it
// creates an IMMDeviceEnumerator). nvcontainer.exe therefore only qualifies once
// NVIDIA has started a recording or Instant Replay at least once; callers should
// keep retrying such apps.
func routeApps(apps []string, deviceID string) (RouteResult, error) {
	res := RouteResult{Failed: map[string]error{}}
	pc, err := newPolicyConfig()
	if err != nil {
		return res, err
	}
	defer pc.release()
	found, missing, err := processIDs(apps)
	if err != nil {
		return res, err
	}
	res.Missing = missing
	for _, app := range apps {
		pids := found[app]
		if len(pids) == 0 {
			continue
		}
		var lastErr error
		for _, pid := range pids {
			if lastErr = pc.setDefault(pid, deviceID); lastErr == nil {
				break
			}
		}
		if lastErr != nil {
			res.Failed[app] = lastErr
			continue
		}
		res.Done = append(res.Done, app)
		if deviceID == "" {
			log.Printf("[%s] per-app device override cleared", app)
		} else {
			log.Printf("[%s] default render device -> %s", app, deviceID)
		}
	}
	return res, nil
}

// notReadyHint explains the usual cause of a policy API rejection.
const notReadyHint = "process has not used audio yet (for NVIDIA: start one recording / Instant Replay, then retry)"

// routeStatus prints the current per-app override for every configured app.
func routeStatus(apps []string, eps []Endpoint) error {
	pc, err := newPolicyConfig()
	if err != nil {
		return err
	}
	defer pc.release()
	found, missing, err := processIDs(apps)
	if err != nil {
		return err
	}
	for _, app := range apps {
		pids := found[app]
		if len(pids) == 0 {
			continue
		}
		var id string
		var pid uint32
		for _, pid = range pids {
			if id, err = pc.getDefault(pid); err == nil {
				break
			}
		}
		if err != nil {
			fmt.Printf("  %-20s pids %v: %v\n%25s(%s)\n", app, pids, err, "", notReadyHint)
			err = nil
			continue
		}
		name := "(system default)"
		if id != "" {
			name = id + " (not active)"
			for _, e := range eps {
				if e.ID == id {
					name = e.Name
					break
				}
			}
		}
		fmt.Printf("  %-20s pid %-6d -> %s\n", app, pid, name)
	}
	for _, app := range missing {
		fmt.Printf("  %-20s not running\n", app)
	}
	return nil
}

// uiRouteRow is routeStatus in structured form for the settings window.
type uiRouteRow struct {
	App     string
	Running bool
	Device  string
	Error   string
}

// routeRows reports the current per-app device of every recorder app.
func routeRows(apps []string) ([]uiRouteRow, error) {
	pc, err := newPolicyConfig()
	if err != nil {
		return nil, err
	}
	defer pc.release()
	found, _, err := processIDs(apps)
	if err != nil {
		return nil, err
	}
	eps, _ := listEndpoints(eRender)
	var rows []uiRouteRow
	for _, app := range apps {
		row := uiRouteRow{App: app}
		pids := found[app]
		if len(pids) == 0 {
			rows = append(rows, row)
			continue
		}
		row.Running = true
		var id string
		var lastErr error
		for _, pid := range pids {
			if id, lastErr = pc.getDefault(pid); lastErr == nil {
				break
			}
		}
		switch {
		case lastErr != nil:
			row.Error = notReadyHint
		case id == "":
			row.Device = "системное по умолчанию"
		default:
			row.Device = id
			for _, e := range eps {
				if e.ID == id {
					row.Device = e.Name
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}
