// mksyso wraps an .ico file into a COFF object (rsrc_windows_amd64.syso) with
// RT_ICON / RT_GROUP_ICON resources, so the Go linker embeds it as the
// executable's icon (resource id 1). Standard library only; no rsrc tool needed.
// Usage: go run ./tools/mksyso assets/maxmix.ico rsrc_windows_amd64.syso
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	langNeutral = 0x0409

	imageFileMachineAMD64 = 0x8664
	imageRelAMD64Addr32NB = 0x0003
	sectionChars          = 0x40000040 // INITIALIZED_DATA | MEM_READ
)

type icoEntry struct {
	width, height, colors, planes uint8
	wPlanes, wBitCount            uint16
	size                          uint32
	data                          []byte
}

func readICO(path string) ([]icoEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 6 || binary.LittleEndian.Uint16(raw[2:]) != 1 {
		return nil, fmt.Errorf("%s: not an ICO file", path)
	}
	n := int(binary.LittleEndian.Uint16(raw[4:]))
	var out []icoEntry
	for i := 0; i < n; i++ {
		e := raw[6+16*i:]
		size := binary.LittleEndian.Uint32(e[8:])
		off := binary.LittleEndian.Uint32(e[12:])
		out = append(out, icoEntry{
			width: e[0], height: e[1], colors: e[2], planes: e[3],
			wPlanes: binary.LittleEndian.Uint16(e[4:]), wBitCount: binary.LittleEndian.Uint16(e[6:]),
			size: size, data: raw[off : off+size],
		})
	}
	return out, nil
}

// builder assembles the .rsrc section and records relocation sites.
type builder struct {
	buf    bytes.Buffer
	relocs []uint32 // offsets of IMAGE_RESOURCE_DATA_ENTRY.OffsetToData fields
}

func (b *builder) u16(v uint16) { binary.Write(&b.buf, binary.LittleEndian, v) }
func (b *builder) u32(v uint32) { binary.Write(&b.buf, binary.LittleEndian, v) }
func (b *builder) pad(align int) {
	for b.buf.Len()%align != 0 {
		b.buf.WriteByte(0)
	}
}

func (b *builder) directory(ids []uint32, subdirOffsets []uint32, isSubdir bool) {
	b.u32(0) // characteristics
	b.u32(0) // timestamp
	b.u16(0) // major
	b.u16(0) // minor
	b.u16(0) // named entries
	b.u16(uint16(len(ids)))
	for i, id := range ids {
		b.u32(id)
		off := subdirOffsets[i]
		if isSubdir {
			off |= 0x80000000
		}
		b.u32(off)
	}
}

func dirSize(entries int) uint32 { return 16 + 8*uint32(entries) }

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mksyso <icon.ico> <out.syso>")
		os.Exit(2)
	}
	icons, err := readICO(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	n := len(icons)

	// Layout (all offsets within the .rsrc section):
	//   root dir (2 types) -> RT_ICON dir (n ids) -> n lang dirs -> n data entries
	//                       -> RT_GROUP_ICON dir (1 id) -> 1 lang dir -> 1 data entry
	//   then blobs: icon images..., group icon directory.
	root := uint32(0)
	iconTypeDir := root + dirSize(2)
	groupTypeDir := iconTypeDir + dirSize(n)
	langDirs := groupTypeDir + dirSize(1) // n+1 lang dirs of dirSize(1) each
	dataEntries := langDirs + uint32(n+1)*dirSize(1)
	blobs := dataEntries + uint32(n+1)*16

	var b builder
	// Root: type ids ascending.
	b.directory([]uint32{rtIcon, rtGroupIcon}, []uint32{iconTypeDir, groupTypeDir}, true)
	// RT_ICON type dir: ids 1..n -> lang dirs.
	ids := make([]uint32, n)
	langs := make([]uint32, n)
	for i := 0; i < n; i++ {
		ids[i] = uint32(i + 1)
		langs[i] = langDirs + uint32(i)*dirSize(1)
	}
	b.directory(ids, langs, true)
	// RT_GROUP_ICON type dir: id 1 -> its lang dir.
	b.directory([]uint32{1}, []uint32{langDirs + uint32(n)*dirSize(1)}, true)
	// Lang dirs -> data entries.
	for i := 0; i <= n; i++ {
		b.directory([]uint32{langNeutral}, []uint32{dataEntries + uint32(i)*16}, false)
	}
	// Blob offsets.
	blobOff := make([]uint32, n+1)
	off := blobs
	for i, ic := range icons {
		blobOff[i] = off
		off += ic.size
		off = (off + 7) &^ 7
	}
	blobOff[n] = off
	groupSize := uint32(6 + 14*n)
	// Data entries.
	for i := 0; i <= n; i++ {
		b.relocs = append(b.relocs, uint32(b.buf.Len()))
		b.u32(blobOff[i]) // RVA, fixed up by the linker via relocation
		if i < n {
			b.u32(icons[i].size)
		} else {
			b.u32(groupSize)
		}
		b.u32(0)
		b.u32(0)
	}
	// Blobs.
	for i, ic := range icons {
		for uint32(b.buf.Len()) < blobOff[i] {
			b.buf.WriteByte(0)
		}
		b.buf.Write(ic.data)
	}
	for uint32(b.buf.Len()) < blobOff[n] {
		b.buf.WriteByte(0)
	}
	// GRPICONDIR.
	b.u16(0)
	b.u16(1)
	b.u16(uint16(n))
	for i, ic := range icons {
		b.buf.Write([]byte{ic.width, ic.height, ic.colors, ic.planes})
		b.u16(ic.wPlanes)
		b.u16(ic.wBitCount)
		b.u32(ic.size)
		b.u16(uint16(i + 1))
	}
	b.pad(4)
	section := b.buf.Bytes()

	// COFF object: header, one section header, section data, relocations,
	// symbol table (one section symbol), string table.
	const coffHdr, secHdr, relocSize, symSize = 20, 40, 10, 18
	rawPtr := uint32(coffHdr + secHdr)
	relocPtr := rawPtr + uint32(len(section))
	symPtr := relocPtr + uint32(len(b.relocs))*relocSize

	var out bytes.Buffer
	w16 := func(v uint16) { binary.Write(&out, binary.LittleEndian, v) }
	w32 := func(v uint32) { binary.Write(&out, binary.LittleEndian, v) }
	w16(imageFileMachineAMD64)
	w16(1)      // sections
	w32(0)      // timestamp
	w32(symPtr) // symbol table
	w32(1)      // symbols
	w16(0)      // optional header
	w16(0)      // characteristics
	out.Write([]byte(".rsrc\x00\x00\x00"))
	w32(0) // virtual size
	w32(0) // virtual address
	w32(uint32(len(section)))
	w32(rawPtr)
	w32(relocPtr)
	w32(0) // line numbers
	w16(uint16(len(b.relocs)))
	w16(0)
	w32(sectionChars)
	out.Write(section)
	for _, r := range b.relocs {
		w32(r)
		w32(0) // symbol index: .rsrc
		w16(imageRelAMD64Addr32NB)
	}
	out.Write([]byte(".rsrc\x00\x00\x00"))
	w32(0)           // value
	w16(1)           // section number
	w16(0)           // type
	out.WriteByte(3) // IMAGE_SYM_CLASS_STATIC
	out.WriteByte(0) // aux symbols
	w32(4)           // string table size (empty)

	if err := os.WriteFile(os.Args[2], out.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d icon images, %d bytes)\n", os.Args[2], n, out.Len())
}
