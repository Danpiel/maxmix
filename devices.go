package main

import (
	"fmt"
	"strings"
	"unsafe"
)

// Endpoint is an active audio endpoint (render or capture).
type Endpoint struct {
	ID   string
	Name string
	Flow uint32 // eRender / eCapture
}

// listEndpoints returns all active endpoints of the given flow.
func listEndpoints(flow uint32) ([]Endpoint, error) {
	en, err := newDeviceEnumerator()
	if err != nil {
		return nil, fmt.Errorf("MMDeviceEnumerator: %w", err)
	}
	defer en.Release()
	col, err := en.EnumAudioEndpoints(flow, deviceStateActive)
	if err != nil {
		return nil, fmt.Errorf("EnumAudioEndpoints: %w", err)
	}
	defer col.Release()
	n, err := col.GetCount()
	if err != nil {
		return nil, err
	}
	out := make([]Endpoint, 0, n)
	for i := uint32(0); i < n; i++ {
		dev, err := col.Item(i)
		if err != nil {
			return nil, err
		}
		id, err := dev.GetId()
		if err == nil {
			var name string
			name, err = dev.FriendlyName()
			if err == nil {
				out = append(out, Endpoint{ID: id, Name: name, Flow: flow})
			}
		}
		dev.Release()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// findEndpoint returns the first endpoint whose name contains match (case-insensitive).
func findEndpoint(eps []Endpoint, match string) (Endpoint, bool) {
	m := strings.ToLower(match)
	for _, e := range eps {
		if strings.Contains(strings.ToLower(e.Name), m) {
			return e, true
		}
	}
	return Endpoint{}, false
}

// openDevice returns the IMMDevice for an endpoint ID.
func openDevice(id string) (*IMMDevice, error) {
	en, err := newDeviceEnumerator()
	if err != nil {
		return nil, err
	}
	defer en.Release()
	dev, err := en.GetDevice(id)
	if err != nil {
		return nil, fmt.Errorf("GetDevice(%s): %w (device gone?)", id, err)
	}
	return dev, nil
}

// waveFormatExtensible mirrors the packed Win32 WAVEFORMATEXTENSIBLE (40 bytes).
type waveFormatExtensible struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	CbSize         uint16
	ValidBits      uint16
	ChannelMask    uint32
	SubFormat      [16]byte
}

const (
	waveFormatPCM           = 0x1
	waveFormatIEEEFloat     = 0x3
	waveFormatExtensibleTag = 0xFFFE
)

var (
	subtypePCM   = [16]byte{0x01, 0, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xAA, 0, 0x38, 0x9B, 0x71}
	subtypeFloat = [16]byte{0x03, 0, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xAA, 0, 0x38, 0x9B, 0x71}
)

// sampleFormat describes how a stream's samples are laid out.
type sampleFormat struct {
	Rate     int
	Channels int
	Bits     int
	Float    bool
}

func (f sampleFormat) BlockAlign() int { return f.Channels * f.Bits / 8 }

func (f sampleFormat) String() string {
	kind := "pcm"
	if f.Float {
		kind = "float"
	}
	return fmt.Sprintf("%d Hz, %d ch, %d-bit %s", f.Rate, f.Channels, f.Bits, kind)
}

// floatFormat builds a WAVEFORMATEXTENSIBLE for 32-bit float at rate/channels.
func floatFormat(rate, channels int) *waveFormatExtensible {
	wf := &waveFormatExtensible{
		FormatTag:     waveFormatExtensibleTag,
		Channels:      uint16(channels),
		SamplesPerSec: uint32(rate),
		BitsPerSample: 32,
		CbSize:        22,
		ValidBits:     32,
		SubFormat:     subtypeFloat,
	}
	wf.BlockAlign = uint16(channels * 4)
	wf.AvgBytesPerSec = uint32(rate * channels * 4)
	if channels == 1 {
		wf.ChannelMask = 0x4 // FRONT_CENTER
	} else {
		wf.ChannelMask = 0x3 // FRONT_LEFT | FRONT_RIGHT
	}
	return wf
}

func (wf *waveFormatExtensible) wfx() *waveFormatEx {
	return (*waveFormatEx)(unsafe.Pointer(wf))
}

// parseFormat interprets a WAVEFORMATEX returned by GetMixFormat.
func parseFormat(wfx *waveFormatEx) (sampleFormat, error) {
	f := sampleFormat{Rate: int(wfx.SamplesPerSec), Channels: int(wfx.Channels), Bits: int(wfx.BitsPerSample)}
	switch wfx.FormatTag {
	case waveFormatPCM:
	case waveFormatIEEEFloat:
		f.Float = true
	case waveFormatExtensibleTag:
		ext := (*waveFormatExtensible)(unsafe.Pointer(wfx))
		switch ext.SubFormat {
		case subtypePCM:
		case subtypeFloat:
			f.Float = true
		default:
			return f, fmt.Errorf("unsupported subformat %x", ext.SubFormat)
		}
	default:
		return f, fmt.Errorf("unsupported format tag %#x", wfx.FormatTag)
	}
	if f.Bits != 16 && f.Bits != 24 && f.Bits != 32 {
		return f, fmt.Errorf("unsupported bit depth %d", f.Bits)
	}
	return f, nil
}
