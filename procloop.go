package main

// Process loopback capture (Windows 10 2004+): an IAudioClient that yields the
// audio rendered by one process tree, whatever endpoint it plays to. This is
// the same mechanism Steam Game Recording uses for per-application audio.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var (
	mmdevapi                        = syscall.NewLazyDLL("mmdevapi.dll")
	procActivateAudioInterfaceAsync = mmdevapi.NewProc("ActivateAudioInterfaceAsync")

	iidIUnknown                                 = mustGUID("{00000000-0000-0000-C000-000000000046}")
	iidIAgileObject                             = mustGUID("{94EA2B94-E9CC-49E0-C0FF-EE64CA8F5B90}")
	iidIActivateAudioInterfaceCompletionHandler = mustGUID("{41D949AB-9862-444A-80F6-C261334DA5EB}")
)

const (
	virtualAudioDeviceProcessLoopback = `VAD\Process_Loopback`

	audioclientActivationTypeProcessLoopback = 1
	processLoopbackModeIncludeTree           = 0
	processLoopbackModeExcludeTree           = 1

	vtBlob       = 65
	eNoInterface = 0x80004002
	activateWait = 5 * time.Second
	waitObject0  = 0
	waitTimeout  = 0x102
)

// audioclientActivationParams mirrors AUDIOCLIENT_ACTIVATION_PARAMS (12 bytes).
type audioclientActivationParams struct {
	ActivationType      uint32
	TargetProcessID     uint32
	ProcessLoopbackMode uint32
}

// blobPropVariant is a PROPVARIANT carrying VT_BLOB.
type blobPropVariant struct {
	vt       uint16
	reserved [3]uint16
	cbSize   uint32
	_        uint32
	pBlob    *audioclientActivationParams
}

// activateResult is what the completion handler hands back.
type activateResult struct {
	client *IAudioClient
	err    error
}

// activateHandler implements IActivateAudioInterfaceCompletionHandler +
// IAgileObject in Go. The vtable pointer must be the first field.
type activateHandler struct {
	lpVtbl *[4]uintptr
	refs   int32
	result chan activateResult
}

var (
	handlerVtblOnce sync.Once
	handlerVtbl     [4]uintptr
	handlers        sync.Map // this pointer (uintptr) -> *activateHandler
)

func handlerLookup(this uintptr) *activateHandler {
	if v, ok := handlers.Load(this); ok {
		return v.(*activateHandler)
	}
	return nil
}

func guidEqual(a *GUID, p uintptr) bool {
	b := (*GUID)(unsafe.Pointer(p)) //nolint:govet // native memory
	return *a == *b
}

func handlerQueryInterface(this, riid, ppv uintptr) uintptr {
	if ppv == 0 {
		return eInvalidArg
	}
	if guidEqual(iidIUnknown, riid) || guidEqual(iidIAgileObject, riid) || guidEqual(iidIActivateAudioInterfaceCompletionHandler, riid) {
		*(*uintptr)(unsafe.Pointer(ppv)) = this //nolint:govet // native out-param
		handlerAddRef(this)
		return 0
	}
	*(*uintptr)(unsafe.Pointer(ppv)) = 0 //nolint:govet
	return eNoInterface
}

func handlerAddRef(this uintptr) uintptr {
	if h := handlerLookup(this); h != nil {
		return uintptr(atomic.AddInt32(&h.refs, 1))
	}
	return 1
}

func handlerRelease(this uintptr) uintptr {
	if h := handlerLookup(this); h != nil {
		return uintptr(atomic.AddInt32(&h.refs, -1))
	}
	return 0
}

// handlerActivateCompleted runs on a COM worker thread once activation is done.
func handlerActivateCompleted(this, op uintptr) uintptr {
	h := handlerLookup(this)
	if h == nil {
		return 0
	}
	var hr uint32
	var punk *IAudioClient
	opObj := (*comObj)(unsafe.Pointer(op)) //nolint:govet // native COM pointer
	r, _, _ := syscall.SyscallN(opObj.lpVtbl[3], op, uintptr(unsafe.Pointer(&hr)), uintptr(unsafe.Pointer(&punk)))
	res := activateResult{client: punk}
	switch {
	case failed(r):
		res.err = fmt.Errorf("GetActivateResult: %w", hresult(r))
	case failed(uintptr(hr)):
		res.err = fmt.Errorf("activation failed: %w", hresult(hr))
	case punk == nil:
		res.err = fmt.Errorf("activation returned no interface")
	}
	h.result <- res
	return 0
}

func newActivateHandler() *activateHandler {
	handlerVtblOnce.Do(func() {
		handlerVtbl = [4]uintptr{
			syscall.NewCallback(handlerQueryInterface),
			syscall.NewCallback(handlerAddRef),
			syscall.NewCallback(handlerRelease),
			syscall.NewCallback(handlerActivateCompleted),
		}
	})
	h := &activateHandler{lpVtbl: &handlerVtbl, refs: 1, result: make(chan activateResult, 1)}
	handlers.Store(uintptr(unsafe.Pointer(h)), h)
	return h
}

func (h *activateHandler) dispose() {
	handlers.Delete(uintptr(unsafe.Pointer(h)))
}

// activateProcessLoopback returns an IAudioClient capturing the audio of pid
// (and its child processes when includeTree is set).
func activateProcessLoopback(pid uint32, includeTree bool) (*IAudioClient, error) {
	params := &audioclientActivationParams{
		ActivationType:      audioclientActivationTypeProcessLoopback,
		TargetProcessID:     pid,
		ProcessLoopbackMode: processLoopbackModeExcludeTree,
	}
	if includeTree {
		params.ProcessLoopbackMode = processLoopbackModeIncludeTree
	}
	pv := blobPropVariant{vt: vtBlob, cbSize: uint32(unsafe.Sizeof(*params)), pBlob: params}
	path, err := syscall.UTF16PtrFromString(virtualAudioDeviceProcessLoopback)
	if err != nil {
		return nil, err
	}
	h := newActivateHandler()
	defer h.dispose()
	var op *comObj
	r, _, _ := procActivateAudioInterfaceAsync.Call(
		uintptr(unsafe.Pointer(path)),
		uintptr(unsafe.Pointer(iidIAudioClient)),
		uintptr(unsafe.Pointer(&pv)),
		uintptr(unsafe.Pointer(h)),
		uintptr(unsafe.Pointer(&op)))
	if failed(r) {
		return nil, fmt.Errorf("ActivateAudioInterfaceAsync(pid %d): %w", pid, hresult(r))
	}
	defer op.Release()
	select {
	case res := <-h.result:
		return res.client, res.err
	case <-time.After(activateWait):
		return nil, fmt.Errorf("ActivateAudioInterfaceAsync(pid %d): timeout", pid)
	}
}

// pcm16Format is the fallback format for process loopback when float is refused.
func pcm16Format(rate, channels int) (*waveFormatEx, sampleFormat) {
	wf := &waveFormatEx{
		FormatTag:      waveFormatPCM,
		Channels:       uint16(channels),
		SamplesPerSec:  uint32(rate),
		BitsPerSample:  16,
		BlockAlign:     uint16(channels * 2),
		AvgBytesPerSec: uint32(rate * channels * 2),
	}
	return wf, sampleFormat{Rate: rate, Channels: channels, Bits: 16}
}

// openProcessLoopback activates and initialises an event-driven loopback
// client for pid. It returns the client, its sample format and the event.
func openProcessLoopback(pid uint32, includeTree bool, canon sampleFormat) (*IAudioClient, sampleFormat, syscall.Handle, error) {
	ac, err := activateProcessLoopback(pid, includeTree)
	if err != nil {
		return nil, canon, 0, err
	}
	flags := uint32(audclntStreamFlagsLoopback | audclntStreamFlagsEventCallback)
	format := canon
	if err := ac.Initialize(flags, captureBufferHNS, floatFormat(canon.Rate, canon.Channels).wfx()); err != nil {
		ac.Release()
		if ac, err = activateProcessLoopback(pid, includeTree); err != nil {
			return nil, canon, 0, err
		}
		var wf *waveFormatEx
		wf, format = pcm16Format(canon.Rate, canon.Channels)
		if err := ac.Initialize(flags, captureBufferHNS, wf); err != nil {
			ac.Release()
			return nil, canon, 0, fmt.Errorf("Initialize process loopback: %w", err)
		}
	}
	ev, err := createEvent()
	if err != nil {
		ac.Release()
		return nil, canon, 0, err
	}
	if err := ac.SetEventHandle(ev); err != nil {
		syscall.CloseHandle(ev)
		ac.Release()
		return nil, canon, 0, fmt.Errorf("SetEventHandle: %w", err)
	}
	return ac, format, ev, nil
}
