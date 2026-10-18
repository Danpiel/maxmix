package main

// System default audio devices: read them through IMMDeviceEnumerator and set
// them through the undocumented IPolicyConfig (the interface the Sound control
// panel uses; also used by SoundSwitch, nircmd, EarTrumpet).

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

var (
	clsidPolicyConfigClient = mustGUID("{870af99c-171d-4f9e-af0d-e63df40c2bc9}")
	iidIPolicyConfig        = mustGUID("{f8679f50-850a-41cf-9c72-430f290290c8}")
)

const (
	eCommunications      = 2
	vtSetDefaultEndpoint = 13 // IPolicyConfig: IUnknown(3) + 10 methods before it
	roleCount            = 3
)

var roleNames = [roleCount]string{"console", "multimedia", "communications"}

// IPolicyConfig is the Sound control panel's private interface.
type IPolicyConfig struct{ comObj }

func newPolicyConfigClient() (*IPolicyConfig, error) {
	var pc *IPolicyConfig
	if err := coCreateInstance(clsidPolicyConfigClient, iidIPolicyConfig, unsafe.Pointer(&pc)); err != nil {
		return nil, fmt.Errorf("PolicyConfigClient: %w", err)
	}
	return pc, nil
}

// SetDefaultEndpoint makes the endpoint the default for one role.
func (pc *IPolicyConfig) SetDefaultEndpoint(id string, role uint32) error {
	u, err := syscall.UTF16PtrFromString(id)
	if err != nil {
		return err
	}
	r, _, _ := syscall.SyscallN(pc.lpVtbl[vtSetDefaultEndpoint], pc.ptr(), uintptr(unsafe.Pointer(u)), uintptr(role))
	if failed(r) {
		return hresult(r)
	}
	return nil
}

// audioDefaults holds the default endpoint id per flow and role ("" = none).
type audioDefaults struct {
	Render  [roleCount]string
	Capture [roleCount]string
}

// currentDefaults reads the system defaults for both flows and all roles.
func currentDefaults() (audioDefaults, error) {
	var d audioDefaults
	en, err := newDeviceEnumerator()
	if err != nil {
		return d, err
	}
	defer en.Release()
	for role := uint32(0); role < roleCount; role++ {
		for _, flow := range []uint32{eRender, eCapture} {
			dev, err := en.GetDefaultAudioEndpoint(flow, role)
			if err != nil {
				continue // no device of that flow at all
			}
			id, _ := dev.GetId()
			dev.Release()
			if flow == eRender {
				d.Render[role] = id
			} else {
				d.Capture[role] = id
			}
		}
	}
	return d, nil
}

// restoreDefaults puts back every default that differs from d and reports what
// it changed, as "flow/role -> name".
func restoreDefaults(d audioDefaults) ([]string, error) {
	now, err := currentDefaults()
	if err != nil {
		return nil, err
	}
	pc, err := newPolicyConfigClient()
	if err != nil {
		return nil, err
	}
	defer pc.Release()
	names := map[string]string{}
	for _, flow := range []uint32{eRender, eCapture} {
		if eps, err := listEndpoints(flow); err == nil {
			for _, e := range eps {
				names[e.ID] = e.Name
			}
		}
	}
	var changed []string
	for role := 0; role < roleCount; role++ {
		for _, pair := range []struct {
			flow      string
			want, got string
		}{{"playback", d.Render[role], now.Render[role]}, {"recording", d.Capture[role], now.Capture[role]}} {
			if pair.want == "" || pair.want == pair.got {
				continue
			}
			if _, stillThere := names[pair.want]; !stillThere {
				continue
			}
			if err := pc.SetDefaultEndpoint(pair.want, uint32(role)); err != nil {
				return changed, fmt.Errorf("SetDefaultEndpoint(%s/%s): %w", pair.flow, roleNames[role], err)
			}
			changed = append(changed, fmt.Sprintf("%s/%s -> %s", pair.flow, roleNames[role], names[pair.want]))
		}
	}
	return changed, nil
}

// cmdDefault prints the defaults, or with an argument makes the matching
// playback endpoint the default for every role.
func cmdDefault(args []string) error {
	if len(args) == 0 {
		d, err := currentDefaults()
		if err != nil {
			return err
		}
		for _, flow := range []struct {
			name string
			flow uint32
			ids  [roleCount]string
		}{{"playback", eRender, d.Render}, {"recording", eCapture, d.Capture}} {
			eps, _ := listEndpoints(flow.flow)
			for role, id := range flow.ids {
				name := "(none)"
				for _, e := range eps {
					if e.ID == id {
						name = e.Name
					}
				}
				fmt.Printf("  %-9s %-14s %s\n", flow.name, roleNames[role], name)
			}
		}
		return nil
	}
	// Optional trailing role name limits the change to one role.
	roles := []uint32{0, 1, 2}
	if n := len(args); n > 1 {
		for i, name := range roleNames {
			if strings.EqualFold(args[n-1], name) {
				roles = []uint32{uint32(i)}
				args = args[:n-1]
			}
		}
	}
	match := strings.Join(args, " ")
	eps, err := listEndpoints(eRender)
	if err != nil {
		return err
	}
	ep, ok := findEndpoint(eps, match)
	if !ok {
		return fmt.Errorf("no playback device matches %q", match)
	}
	pc, err := newPolicyConfigClient()
	if err != nil {
		return err
	}
	defer pc.Release()
	for _, role := range roles {
		if err := pc.SetDefaultEndpoint(ep.ID, role); err != nil {
			return err
		}
	}
	fmt.Printf("default playback device -> %s\n", ep.Name)
	return nil
}

// ---- endpoint volume ----------------------------------------------------------

var iidIAudioEndpointVolume = mustGUID("{5CDF2C82-841E-4546-9722-0CF74078229A}")

// IAudioEndpointVolume is the per-device volume control (read-only use here).
type IAudioEndpointVolume struct{ comObj }

const (
	vtGetMasterVolumeScalar = 9
	vtGetMute               = 15
)

func (d *IMMDevice) ActivateEndpointVolume() (*IAudioEndpointVolume, error) {
	var v *IAudioEndpointVolume
	r, _, _ := syscall.SyscallN(d.lpVtbl[3], d.ptr(), uintptr(unsafe.Pointer(iidIAudioEndpointVolume)), clsctxAll, 0, uintptr(unsafe.Pointer(&v)))
	if failed(r) {
		return nil, hresult(r)
	}
	return v, nil
}

func (v *IAudioEndpointVolume) GetMute() (bool, error) {
	var mute int32
	r, _, _ := syscall.SyscallN(v.lpVtbl[vtGetMute], v.ptr(), uintptr(unsafe.Pointer(&mute)))
	if failed(r) {
		return false, hresult(r)
	}
	return mute != 0, nil
}

func (v *IAudioEndpointVolume) GetLevel() (float32, error) {
	var level float32
	r, _, _ := syscall.SyscallN(v.lpVtbl[vtGetMasterVolumeScalar], v.ptr(), uintptr(unsafe.Pointer(&level)))
	if failed(r) {
		return 0, hresult(r)
	}
	return level, nil
}

// endpointVolume reads mute state and master level of one endpoint.
func endpointVolume(id string) (mute bool, level float32, err error) {
	dev, err := openDevice(id)
	if err != nil {
		return false, 0, err
	}
	defer dev.Release()
	vol, err := dev.ActivateEndpointVolume()
	if err != nil {
		return false, 0, err
	}
	defer vol.Release()
	if mute, err = vol.GetMute(); err != nil {
		return false, 0, err
	}
	level, err = vol.GetLevel()
	return mute, level, err
}
