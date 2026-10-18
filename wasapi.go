package main

// Hand-written bindings for the Core Audio (MMDevice / WASAPI) interfaces we
// use. Vtable slot numbers follow mmdeviceapi.h / audioclient.h; slots 0-2 are
// IUnknown.

import (
	"syscall"
	"unsafe"
)

var (
	clsidMMDeviceEnumerator = mustGUID("{BCDE0395-E52F-467C-8E3D-C4579291692E}")
	iidIMMDeviceEnumerator  = mustGUID("{A95664D2-9614-4F35-A746-DE8DB63617E6}")
	iidIAudioClient         = mustGUID("{1CB9AD4C-DBFA-4c32-B178-C2F568A703B2}")
	iidIAudioCaptureClient  = mustGUID("{C8ADBD64-E71E-48a0-A4DE-185C395CD317}")
	iidIAudioRenderClient   = mustGUID("{F294ACFC-3146-4483-A7BF-ADDCA7C260E2}")

	pkeyDeviceFriendlyName = propertyKey{fmtid: *mustGUID("{a45c254e-df1c-4efd-8020-67d146a850e0}"), pid: 14}
)

// EDataFlow / ERole / device states.
const (
	eRender  = 0
	eCapture = 1

	eConsole          = 0
	eMultimedia       = 1
	stgmRead          = 0
	deviceStateActive = 0x1
)

// WASAPI constants (audioclient.h / audiosessiontypes.h).
const (
	audclntShareModeShared = 0

	audclntStreamFlagsLoopback          = 0x00020000
	audclntStreamFlagsAutoConvertPCM    = 0x80000000
	audclntStreamFlagsSRCDefaultQuality = 0x08000000

	audclntBufferFlagsSilent = 0x2
)

type propertyKey struct {
	fmtid GUID
	pid   uint32
}

// propVariant is the 24-byte PROPVARIANT; we only read VT_LPWSTR.
type propVariant struct {
	vt       uint16
	reserved [3]uint16
	val      [16]byte
}

const vtLPWSTR = 31

func (pv *propVariant) String() string {
	if pv.vt != vtLPWSTR {
		return ""
	}
	return utf16PtrToString(*(**uint16)(unsafe.Pointer(&pv.val[0])))
}

func (pv *propVariant) clear() {
	procPropVariantClear.Call(uintptr(unsafe.Pointer(pv)))
}

// waveFormatEx mirrors WAVEFORMATEX; field offsets match the packed C struct
// (Go only adds trailing padding, which is irrelevant for reading).
type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	CbSize         uint16
}

// ---- IMMDeviceEnumerator --------------------------------------------------

type IMMDeviceEnumerator struct{ comObj }

func newDeviceEnumerator() (*IMMDeviceEnumerator, error) {
	var e *IMMDeviceEnumerator
	if err := coCreateInstance(clsidMMDeviceEnumerator, iidIMMDeviceEnumerator, unsafe.Pointer(&e)); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *IMMDeviceEnumerator) EnumAudioEndpoints(flow, stateMask uint32) (*IMMDeviceCollection, error) {
	var c *IMMDeviceCollection
	r, _, _ := syscall.SyscallN(e.lpVtbl[3], e.ptr(), uintptr(flow), uintptr(stateMask), uintptr(unsafe.Pointer(&c)))
	if failed(r) {
		return nil, hresult(r)
	}
	return c, nil
}

func (e *IMMDeviceEnumerator) GetDefaultAudioEndpoint(flow, role uint32) (*IMMDevice, error) {
	var d *IMMDevice
	r, _, _ := syscall.SyscallN(e.lpVtbl[4], e.ptr(), uintptr(flow), uintptr(role), uintptr(unsafe.Pointer(&d)))
	if failed(r) {
		return nil, hresult(r)
	}
	return d, nil
}

func (e *IMMDeviceEnumerator) GetDevice(id string) (*IMMDevice, error) {
	u16, err := syscall.UTF16PtrFromString(id)
	if err != nil {
		return nil, err
	}
	var d *IMMDevice
	r, _, _ := syscall.SyscallN(e.lpVtbl[5], e.ptr(), uintptr(unsafe.Pointer(u16)), uintptr(unsafe.Pointer(&d)))
	if failed(r) {
		return nil, hresult(r)
	}
	return d, nil
}

// ---- IMMDeviceCollection --------------------------------------------------

type IMMDeviceCollection struct{ comObj }

func (c *IMMDeviceCollection) GetCount() (uint32, error) {
	var n uint32
	r, _, _ := syscall.SyscallN(c.lpVtbl[3], c.ptr(), uintptr(unsafe.Pointer(&n)))
	if failed(r) {
		return 0, hresult(r)
	}
	return n, nil
}

func (c *IMMDeviceCollection) Item(i uint32) (*IMMDevice, error) {
	var d *IMMDevice
	r, _, _ := syscall.SyscallN(c.lpVtbl[4], c.ptr(), uintptr(i), uintptr(unsafe.Pointer(&d)))
	if failed(r) {
		return nil, hresult(r)
	}
	return d, nil
}

// ---- IMMDevice ------------------------------------------------------------

type IMMDevice struct{ comObj }

func (d *IMMDevice) ActivateAudioClient() (*IAudioClient, error) {
	var ac *IAudioClient
	r, _, _ := syscall.SyscallN(d.lpVtbl[3], d.ptr(), uintptr(unsafe.Pointer(iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(&ac)))
	if failed(r) {
		return nil, hresult(r)
	}
	return ac, nil
}

func (d *IMMDevice) OpenPropertyStore(mode uint32) (*IPropertyStore, error) {
	var ps *IPropertyStore
	r, _, _ := syscall.SyscallN(d.lpVtbl[4], d.ptr(), uintptr(mode), uintptr(unsafe.Pointer(&ps)))
	if failed(r) {
		return nil, hresult(r)
	}
	return ps, nil
}

func (d *IMMDevice) GetId() (string, error) {
	var p *uint16
	r, _, _ := syscall.SyscallN(d.lpVtbl[5], d.ptr(), uintptr(unsafe.Pointer(&p)))
	if failed(r) {
		return "", hresult(r)
	}
	defer coTaskMemFree(unsafe.Pointer(p))
	return utf16PtrToString(p), nil
}

// FriendlyName reads PKEY_Device_FriendlyName.
func (d *IMMDevice) FriendlyName() (string, error) {
	ps, err := d.OpenPropertyStore(stgmRead)
	if err != nil {
		return "", err
	}
	defer ps.Release()
	return ps.GetString(&pkeyDeviceFriendlyName)
}

// ---- IPropertyStore -------------------------------------------------------

type IPropertyStore struct{ comObj }

func (ps *IPropertyStore) GetString(key *propertyKey) (string, error) {
	var pv propVariant
	r, _, _ := syscall.SyscallN(ps.lpVtbl[5], ps.ptr(), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&pv)))
	if failed(r) {
		return "", hresult(r)
	}
	defer pv.clear()
	return pv.String(), nil
}

// ---- IAudioClient ---------------------------------------------------------

type IAudioClient struct{ comObj }

// Initialize opens a shared-mode stream. bufferDuration is in 100 ns units.
func (ac *IAudioClient) Initialize(flags uint32, bufferDuration int64, format *waveFormatEx) error {
	r, _, _ := syscall.SyscallN(ac.lpVtbl[3], ac.ptr(), audclntShareModeShared, uintptr(flags), uintptr(bufferDuration), 0, uintptr(unsafe.Pointer(format)), 0)
	if failed(r) {
		return hresult(r)
	}
	return nil
}

func (ac *IAudioClient) GetBufferSize() (uint32, error) {
	var n uint32
	r, _, _ := syscall.SyscallN(ac.lpVtbl[4], ac.ptr(), uintptr(unsafe.Pointer(&n)))
	if failed(r) {
		return 0, hresult(r)
	}
	return n, nil
}

func (ac *IAudioClient) GetCurrentPadding() (uint32, error) {
	var n uint32
	r, _, _ := syscall.SyscallN(ac.lpVtbl[6], ac.ptr(), uintptr(unsafe.Pointer(&n)))
	if failed(r) {
		return 0, hresult(r)
	}
	return n, nil
}

// GetMixFormat returns the engine format; free it with coTaskMemFree.
func (ac *IAudioClient) GetMixFormat() (*waveFormatEx, error) {
	var f *waveFormatEx
	r, _, _ := syscall.SyscallN(ac.lpVtbl[8], ac.ptr(), uintptr(unsafe.Pointer(&f)))
	if failed(r) {
		return nil, hresult(r)
	}
	return f, nil
}

func (ac *IAudioClient) Start() error {
	r, _, _ := syscall.SyscallN(ac.lpVtbl[10], ac.ptr())
	if failed(r) {
		return hresult(r)
	}
	return nil
}

func (ac *IAudioClient) Stop() error {
	r, _, _ := syscall.SyscallN(ac.lpVtbl[11], ac.ptr())
	if failed(r) {
		return hresult(r)
	}
	return nil
}

func (ac *IAudioClient) getService(iid *GUID, out unsafe.Pointer) error {
	r, _, _ := syscall.SyscallN(ac.lpVtbl[14], ac.ptr(), uintptr(unsafe.Pointer(iid)), uintptr(out))
	if failed(r) {
		return hresult(r)
	}
	return nil
}

func (ac *IAudioClient) CaptureClient() (*IAudioCaptureClient, error) {
	var c *IAudioCaptureClient
	if err := ac.getService(iidIAudioCaptureClient, unsafe.Pointer(&c)); err != nil {
		return nil, err
	}
	return c, nil
}

func (ac *IAudioClient) RenderClient() (*IAudioRenderClient, error) {
	var c *IAudioRenderClient
	if err := ac.getService(iidIAudioRenderClient, unsafe.Pointer(&c)); err != nil {
		return nil, err
	}
	return c, nil
}

// ---- IAudioCaptureClient --------------------------------------------------

type IAudioCaptureClient struct{ comObj }

// GetBuffer returns the next packet: data pointer, frame count and flags.
func (c *IAudioCaptureClient) GetBuffer() (data *byte, frames, flags uint32, err error) {
	r, _, _ := syscall.SyscallN(c.lpVtbl[3], c.ptr(), uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)), 0, 0)
	if failed(r) {
		return nil, 0, 0, hresult(r)
	}
	return data, frames, flags, nil
}

func (c *IAudioCaptureClient) ReleaseBuffer(frames uint32) error {
	r, _, _ := syscall.SyscallN(c.lpVtbl[4], c.ptr(), uintptr(frames))
	if failed(r) {
		return hresult(r)
	}
	return nil
}

func (c *IAudioCaptureClient) GetNextPacketSize() (uint32, error) {
	var n uint32
	r, _, _ := syscall.SyscallN(c.lpVtbl[5], c.ptr(), uintptr(unsafe.Pointer(&n)))
	if failed(r) {
		return 0, hresult(r)
	}
	return n, nil
}

// ---- IAudioRenderClient ---------------------------------------------------

type IAudioRenderClient struct{ comObj }

func (c *IAudioRenderClient) GetBuffer(frames uint32) (*byte, error) {
	var data *byte
	r, _, _ := syscall.SyscallN(c.lpVtbl[3], c.ptr(), uintptr(frames), uintptr(unsafe.Pointer(&data)))
	if failed(r) {
		return nil, hresult(r)
	}
	return data, nil
}

func (c *IAudioRenderClient) ReleaseBuffer(frames, flags uint32) error {
	r, _, _ := syscall.SyscallN(c.lpVtbl[4], c.ptr(), uintptr(frames), uintptr(flags))
	if failed(r) {
		return hresult(r)
	}
	return nil
}

// ---- events -----------------------------------------------------------------

const audclntStreamFlagsEventCallback = 0x00040000

// SetEventHandle registers the buffer-ready event for event-driven streams.
func (ac *IAudioClient) SetEventHandle(ev syscall.Handle) error {
	r, _, _ := syscall.SyscallN(ac.lpVtbl[13], ac.ptr(), uintptr(ev))
	if failed(r) {
		return hresult(r)
	}
	return nil
}

// ---- audio sessions -----------------------------------------------------------

var (
	iidIAudioSessionManager2 = mustGUID("{77AA99A0-1BD6-484F-8BC7-2C654C9A9B6F}")
	iidIAudioSessionControl2 = mustGUID("{bfb7ff88-7239-4fc9-8fa2-07c950be9c6d}")
)

const (
	audioSessionStateInactive = 0
	audioSessionStateActive   = 1
	audioSessionStateExpired  = 2
)

type IAudioSessionManager2 struct{ comObj }
type IAudioSessionEnumerator struct{ comObj }
type IAudioSessionControl struct{ comObj }
type IAudioSessionControl2 struct{ comObj }

func (d *IMMDevice) ActivateSessionManager() (*IAudioSessionManager2, error) {
	var m *IAudioSessionManager2
	r, _, _ := syscall.SyscallN(d.lpVtbl[3], d.ptr(), uintptr(unsafe.Pointer(iidIAudioSessionManager2)), clsctxAll, 0, uintptr(unsafe.Pointer(&m)))
	if failed(r) {
		return nil, hresult(r)
	}
	return m, nil
}

// GetSessionEnumerator is IAudioSessionManager2 slot 5 (after IAudioSessionManager's two methods).
func (m *IAudioSessionManager2) GetSessionEnumerator() (*IAudioSessionEnumerator, error) {
	var e *IAudioSessionEnumerator
	r, _, _ := syscall.SyscallN(m.lpVtbl[5], m.ptr(), uintptr(unsafe.Pointer(&e)))
	if failed(r) {
		return nil, hresult(r)
	}
	return e, nil
}

func (e *IAudioSessionEnumerator) GetCount() (int32, error) {
	var n int32
	r, _, _ := syscall.SyscallN(e.lpVtbl[3], e.ptr(), uintptr(unsafe.Pointer(&n)))
	if failed(r) {
		return 0, hresult(r)
	}
	return n, nil
}

func (e *IAudioSessionEnumerator) GetSession(i int32) (*IAudioSessionControl, error) {
	var s *IAudioSessionControl
	r, _, _ := syscall.SyscallN(e.lpVtbl[4], e.ptr(), uintptr(i), uintptr(unsafe.Pointer(&s)))
	if failed(r) {
		return nil, hresult(r)
	}
	return s, nil
}

func (s *IAudioSessionControl) GetState() (uint32, error) {
	var st uint32
	r, _, _ := syscall.SyscallN(s.lpVtbl[3], s.ptr(), uintptr(unsafe.Pointer(&st)))
	if failed(r) {
		return 0, hresult(r)
	}
	return st, nil
}

func (s *IAudioSessionControl) Control2() (*IAudioSessionControl2, error) {
	var s2 *IAudioSessionControl2
	if err := s.QueryInterface(iidIAudioSessionControl2, unsafe.Pointer(&s2)); err != nil {
		return nil, err
	}
	return s2, nil
}

// GetProcessId is IAudioSessionControl2 slot 14 (9 IAudioSessionControl methods + 2).
func (s *IAudioSessionControl2) GetProcessId() (uint32, error) {
	var pid uint32
	r, _, _ := syscall.SyscallN(s.lpVtbl[14], s.ptr(), uintptr(unsafe.Pointer(&pid)))
	if failed(r) {
		return 0, hresult(r)
	}
	return pid, nil
}
