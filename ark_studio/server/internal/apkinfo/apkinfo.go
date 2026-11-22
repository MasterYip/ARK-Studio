// Package apkinfo reads the package name and version of an Android APK from
// its binary AndroidManifest.xml (Go only; the update manifest of
// server/app_manifest.go).  It understands the compiled XML that aapt and
// aapt2 write: a string pool, an optional resource-id map, and element
// chunks; only the attributes of the root <manifest> element are read.
package apkinfo

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
)

// Info is what the update check needs of an APK.
type Info struct {
	Package     string
	VersionCode int64
	VersionName string
}

const (
	chunkStringPool = 0x0001
	chunkXML        = 0x0003
	chunkResMap     = 0x0180
	chunkStartElem  = 0x0102
	utf8Flag        = 1 << 8
	typeString      = 0x03
	typeIntDec      = 0x10
	typeIntHex      = 0x11
	attrVersionCode = 0x0101021b
	attrVersionName = 0x0101021c
	maxManifest     = 4 << 20
)

var errFormat = errors.New("apkinfo: not a compiled AndroidManifest.xml")

// Read reads the manifest of the APK at r (size bytes).
func Read(r io.ReaderAt, size int64) (Info, error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return Info{}, err
	}
	for _, f := range z.File {
		if f.Name != "AndroidManifest.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return Info{}, err
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, maxManifest))
		if err != nil {
			return Info{}, err
		}
		return Parse(data)
	}
	return Info{}, errors.New("apkinfo: no AndroidManifest.xml")
}

// Parse reads the root <manifest> element of a compiled manifest.
func Parse(data []byte) (Info, error) {
	le := binary.LittleEndian
	if len(data) < 8 || le.Uint16(data) != chunkXML {
		return Info{}, errFormat
	}
	var pool []string
	var resIDs []uint32
	off := int(le.Uint16(data[2:]))
	for off+8 <= len(data) {
		typ := le.Uint16(data[off:])
		header := int(le.Uint16(data[off+2:]))
		size := int(le.Uint32(data[off+4:]))
		if size < 8 || off+size > len(data) || header > size {
			return Info{}, errFormat
		}
		chunk := data[off : off+size]
		switch typ {
		case chunkStringPool:
			p, err := stringPool(chunk)
			if err != nil {
				return Info{}, err
			}
			pool = p
		case chunkResMap:
			for i := header; i+4 <= size; i += 4 {
				resIDs = append(resIDs, le.Uint32(chunk[i:]))
			}
		case chunkStartElem:
			return manifestAttrs(chunk, header, pool, resIDs)
		}
		off += size
	}
	return Info{}, errFormat
}

func stringPool(c []byte) ([]string, error) {
	le := binary.LittleEndian
	if len(c) < 28 {
		return nil, errFormat
	}
	header := int(le.Uint16(c[2:]))
	count := int(le.Uint32(c[8:]))
	flags := le.Uint32(c[16:])
	start := int(le.Uint32(c[20:]))
	if count < 0 || header+4*count > len(c) || start > len(c) {
		return nil, errFormat
	}
	out := make([]string, count)
	for i := 0; i < count; i++ {
		at := start + int(le.Uint32(c[header+4*i:]))
		if at >= len(c) {
			return nil, errFormat
		}
		var err error
		if flags&utf8Flag != 0 {
			out[i], err = utf8At(c, at)
		} else {
			out[i], err = utf16At(c, at)
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// utf8At: two lengths (UTF-16 units, then bytes), each one or two bytes.
func utf8At(c []byte, at int) (string, error) {
	skip := func() (int, error) {
		if at >= len(c) {
			return 0, errFormat
		}
		n := int(c[at])
		at++
		if n&0x80 != 0 {
			if at >= len(c) {
				return 0, errFormat
			}
			n = (n&0x7f)<<8 | int(c[at])
			at++
		}
		return n, nil
	}
	if _, err := skip(); err != nil {
		return "", err
	}
	n, err := skip()
	if err != nil || at+n > len(c) {
		return "", errFormat
	}
	return string(c[at : at+n]), nil
}

func utf16At(c []byte, at int) (string, error) {
	le := binary.LittleEndian
	if at+2 > len(c) {
		return "", errFormat
	}
	n := int(le.Uint16(c[at:]))
	at += 2
	if n&0x8000 != 0 {
		if at+2 > len(c) {
			return "", errFormat
		}
		n = (n&0x7fff)<<16 | int(le.Uint16(c[at:]))
		at += 2
	}
	if at+2*n > len(c) {
		return "", errFormat
	}
	units := make([]uint16, n)
	for i := range units {
		units[i] = le.Uint16(c[at+2*i:])
	}
	return string(utf16.Decode(units)), nil
}

func manifestAttrs(c []byte, header int, pool []string, resIDs []uint32) (Info, error) {
	le := binary.LittleEndian
	// After the chunk header: ns, name, attributeStart, attributeSize, attributeCount, ...
	base := header
	if base+20 > len(c) {
		return Info{}, errFormat
	}
	str := func(i uint32) string {
		if int(i) < len(pool) {
			return pool[i]
		}
		return ""
	}
	if name := str(le.Uint32(c[base+4:])); name != "manifest" {
		return Info{}, fmt.Errorf("apkinfo: root element is %q", name)
	}
	attrStart := int(le.Uint16(c[base+8:]))
	attrSize := int(le.Uint16(c[base+10:]))
	count := int(le.Uint16(c[base+12:]))
	if attrSize < 20 {
		return Info{}, errFormat
	}
	var info Info
	for i := 0; i < count; i++ {
		a := base + attrStart + i*attrSize
		if a+20 > len(c) {
			return Info{}, errFormat
		}
		nameIdx := le.Uint32(c[a+4:])
		raw := le.Uint32(c[a+8:])
		dataType := c[a+15]
		value := le.Uint32(c[a+16:])
		name := str(nameIdx)
		var id uint32
		if int(nameIdx) < len(resIDs) {
			id = resIDs[nameIdx]
		}
		text := func() string {
			if raw != 0xffffffff {
				return str(raw)
			}
			if dataType == typeString {
				return str(value)
			}
			return ""
		}
		switch {
		case name == "package":
			info.Package = text()
		case id == attrVersionCode || (id == 0 && name == "versionCode"):
			if dataType == typeIntDec || dataType == typeIntHex {
				info.VersionCode = int64(value)
			}
		case id == attrVersionName || (id == 0 && name == "versionName"):
			info.VersionName = text()
		}
	}
	if info.Package == "" {
		return Info{}, errors.New("apkinfo: no package name")
	}
	return info, nil
}

// ReadBytes is Read over an in-memory APK.
func ReadBytes(apk []byte) (Info, error) {
	return Read(bytes.NewReader(apk), int64(len(apk)))
}
