package main

// Minimal COM / WinRT plumbing on top of the standard library syscall package.
// Every method wrapper calls syscall.SyscallN directly with the
// uintptr(unsafe.Pointer(x)) conversions inline, which is the pattern the Go
// runtime guarantees to keep x alive and un-moved for the duration of the call.

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// GUID is the Win32 GUID layout.
type GUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// mustGUID parses "{xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx}".
func mustGUID(s string) *GUID {
	s = strings.Trim(s, "{}")
	parts := strings.Split(s, "-")
	if len(parts) != 5 {
		panic("bad GUID " + s)
	}
	p := func(str string, bits int) uint64 {
		v, err := strconv.ParseUint(str, 16, bits)
		if err != nil {
			panic("bad GUID " + s)
		}
		return v
	}
	g := &GUID{Data1: uint32(p(parts[0], 32)), Data2: uint16(p(parts[1], 16)), Data3: uint16(p(parts[2], 16))}
	tail := parts[3] + parts[4]
	for i := 0; i < 8; i++ {
		g.Data4[i] = byte(p(tail[2*i:2*i+2], 8))
	}
	return g
}

// hresult is a failed HRESULT.
type hresult uint32

func (h hresult) Error() string {
	return fmt.Sprintf("HRESULT %#08x (%s)", uint32(h), strings.TrimSpace(syscall.Errno(h).Error()))
}

const (
	sFalse                    = 1
	rpcEChangedMode           = 0x80010106
	eInvalidArg               = 0x80070057
	coinitMultithrd           = 0x0
	clsctxAll                 = 0x17
	audclntEDeviceInvalidated = 0x88890004
)

var (
	ole32                = syscall.NewLazyDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")
	procPropVariantClear = ole32.NewProc("PropVariantClear")

	combase                    = syscall.NewLazyDLL("combase.dll")
	procRoGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = combase.NewProc("WindowsDeleteString")
	procWindowsGetStringRaw    = combase.NewProc("WindowsGetStringRawBuffer")
)

// initCOM initialises the multithreaded COM apartment for the calling OS
// thread and pins the goroutine to it. Call in every goroutine that touches COM.
func initCOM() error {
	runtime.LockOSThread()
	r, _, _ := procCoInitializeEx.Call(0, coinitMultithrd)
	if r != 0 && r != sFalse && r != rpcEChangedMode {
		return fmt.Errorf("CoInitializeEx: %w", hresult(r))
	}
	return nil
}

func coTaskMemFree(p unsafe.Pointer) {
	if p != nil {
		procCoTaskMemFree.Call(uintptr(p))
	}
}

// comObj is the in-memory shape of any COM interface pointer: the first word
// points at the vtable. Concrete interfaces embed it and index the vtable.
type comObj struct {
	lpVtbl *[64]uintptr
}

func (o *comObj) ptr() uintptr { return uintptr(unsafe.Pointer(o)) }

// failed reports whether an HRESULT has the severity bit set. Success codes
// such as S_FALSE or AUDCLNT_S_BUFFER_EMPTY are not errors.
func failed(r uintptr) bool { return r&0x80000000 != 0 }

// QueryInterface asks the object for another interface.
func (o *comObj) QueryInterface(iid *GUID, out unsafe.Pointer) error {
	r, _, _ := syscall.SyscallN(o.lpVtbl[0], o.ptr(), uintptr(unsafe.Pointer(iid)), uintptr(out))
	if failed(r) {
		return hresult(r)
	}
	return nil
}

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procCreateEventW = kernel32.NewProc("CreateEventW")
)

// createEvent makes an auto-reset, initially unsignalled event.
func createEvent() (syscall.Handle, error) {
	h, _, err := procCreateEventW.Call(0, 0, 0, 0)
	if h == 0 {
		return 0, fmt.Errorf("CreateEvent: %w", err)
	}
	return syscall.Handle(h), nil
}

// Release drops one reference; safe on nil.
func (o *comObj) Release() {
	if o != nil {
		syscall.SyscallN(o.lpVtbl[2], o.ptr())
	}
}

// coCreateInstance creates an in-process COM object.
func coCreateInstance(clsid, iid *GUID, out unsafe.Pointer) error {
	r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(clsid)), 0, clsctxAll, uintptr(unsafe.Pointer(iid)), uintptr(out))
	if r != 0 {
		return hresult(r)
	}
	return nil
}

// utf16PtrToString copies a NUL-terminated UTF-16 string out of native memory.
func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; n++ {
		ptr = unsafe.Add(ptr, 2)
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

// ---- WinRT HSTRING -------------------------------------------------------

type hstring uintptr

func newHString(s string) (hstring, error) {
	if s == "" {
		return 0, nil
	}
	u16, err := syscall.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h hstring
	r, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u16[0])), uintptr(len(u16)-1), uintptr(unsafe.Pointer(&h)))
	if r != 0 {
		return 0, hresult(r)
	}
	return h, nil
}

func (h hstring) free() {
	if h != 0 {
		procWindowsDeleteString.Call(uintptr(h))
	}
}

func (h hstring) String() string {
	if h == 0 {
		return ""
	}
	var n uint32
	p, _, _ := procWindowsGetStringRaw.Call(uintptr(h), uintptr(unsafe.Pointer(&n)))
	if p == 0 || n == 0 {
		return ""
	}
	buf := make([]uint16, n)
	for i := range buf {
		buf[i] = *(*uint16)(unsafe.Pointer(p + uintptr(i)*2)) //nolint:govet // HSTRING buffer, not Go memory
	}
	return syscall.UTF16ToString(buf)
}

// roGetActivationFactory activates a WinRT class factory for the given IID.
func roGetActivationFactory(class string, iid *GUID) (*comObj, error) {
	cls, err := newHString(class)
	if err != nil {
		return nil, err
	}
	defer cls.free()
	var obj *comObj
	r, _, _ := procRoGetActivationFactory.Call(uintptr(cls), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&obj)))
	if r != 0 {
		return nil, hresult(r)
	}
	if obj == nil {
		return nil, fmt.Errorf("RoGetActivationFactory(%s) returned null", class)
	}
	return obj, nil
}
