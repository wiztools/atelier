package mediasniff

import (
	"bytes"
	"encoding/binary"
)

// ImagePixelDimensions sniffs a decoded image payload's pixel size from its
// magic bytes — PNG/GIF/WebP read from fixed headers, JPEG by scanning for a
// SOFn marker segment. No codec imports (x/image is not a dependency): pricing
// only needs the dimensions, and the outputs fal returns are PNG/JPEG/WebP.
func ImagePixelDimensions(data []byte) (float64, float64, bool) {
	// PNG: 8-byte signature, IHDR's width/height at offsets 16 and 20.
	if len(data) >= 24 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		width := float64(binary.BigEndian.Uint32(data[16:20]))
		height := float64(binary.BigEndian.Uint32(data[20:24]))
		return width, height, width > 0 && height > 0
	}
	// GIF: 6-byte signature, then the 7-byte logical screen descriptor's
	// little-endian width/height.
	if len(data) >= 10 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))) {
		width := float64(binary.LittleEndian.Uint16(data[6:8]))
		height := float64(binary.LittleEndian.Uint16(data[8:10]))
		return width, height, width > 0 && height > 0
	}
	// WebP: RIFF container. VP8X (extended) carries the canvas size minus one
	// as 24-bit little-endian; VP8 (lossy) and VP8L (lossless) carry their own
	// dimensions after their chunk headers.
	if len(data) >= 30 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		switch string(data[12:16]) {
		case "VP8X":
			width := float64(uint32(data[24])|uint32(data[25])<<8|uint32(data[26])<<16) + 1
			height := float64(uint32(data[27])|uint32(data[28])<<8|uint32(data[29])<<16) + 1
			return width, height, width > 0 && height > 0
		case "VP8 ":
			// 3-byte frame tag, then the 10-bit/22-bit start code and
			// little-endian dimensions (14 bits each, stored low 14 bits).
			width := float64(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff)
			height := float64(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff)
			return width, height, width > 0 && height > 0
		case "VP8L":
			// 5-byte signature, then a 14+14-bit packed little-endian size.
			bits := uint32(binary.LittleEndian.Uint16(data[21:23])) | uint32(data[23])<<16 | uint32(data[24])<<24
			width := float64(bits&0x3fff) + 1
			height := float64((bits>>14)&0x3fff) + 1
			return width, height, width > 0 && height > 0
		}
		return 0, 0, false
	}
	// JPEG: scan the marker segments for SOF0..SOF15 (minus DHT/JPG/DAC) and
	// read the big-endian height/width that follow the segment length.
	if len(data) >= 4 && data[0] == 0xff && data[1] == 0xd8 {
		for offset := 2; offset+4 <= len(data); {
			if data[offset] != 0xff {
				offset++
				continue
			}
			marker := data[offset+1]
			if marker == 0xd8 || (0xd0 <= marker && marker <= 0xd7) || marker == 0x01 {
				offset += 2
				continue
			}
			if offset+4 > len(data) {
				break
			}
			segmentLen := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
			if segmentLen < 2 {
				break
			}
			isSOF := marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc
			if isSOF && offset+9 <= len(data) {
				height := float64(binary.BigEndian.Uint16(data[offset+5 : offset+7]))
				width := float64(binary.BigEndian.Uint16(data[offset+7 : offset+9]))
				return width, height, width > 0 && height > 0
			}
			offset += 2 + segmentLen
		}
	}
	return 0, 0, false
}

// JPEGEXIFOrientation extracts the EXIF orientation tag (0x0112) from a JPEG's
// APP1 Exif segment: 1 when absent or upright, 2–8 for the flip/rotate values,
// 0 when the payload is not a JPEG (callers treat 0 like 1). Only the segment
// walk and IFD0 are parsed — no dependencies, no full EXIF decode.
func JPEGEXIFOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0
	}
	pos := 2
	for pos+4 <= len(data) {
		if data[pos] != 0xFF {
			return 1
		}
		marker := data[pos+1]
		// Standalone markers carry no length segment.
		if marker == 0xFF || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			pos += 2
			continue
		}
		// Start of scan: the EXIF segment always precedes the entropy-coded data.
		if marker == 0xDA {
			break
		}
		segmentLength := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		if segmentLength < 2 {
			return 1
		}
		if marker == 0xE1 && pos+10 <= len(data) && string(data[pos+4:pos+10]) == "Exif\x00\x00" {
			segmentEnd := pos + 2 + segmentLength
			if segmentEnd > len(data) {
				segmentEnd = len(data)
			}
			return tiffOrientation(data[pos+10 : segmentEnd])
		}
		pos += 2 + segmentLength
	}
	return 1
}

// tiffOrientation reads the orientation entry (tag 0x0112) from a TIFF header
// (the body of the Exif APP1 segment, after its 6-byte signature). Anything
// unreadable or out of range reports 1 — the fail-safe is "no rotation".
func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder = binary.BigEndian
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
	default:
		return 1
	}
	ifdOffset := int(order.Uint32(tiff[4:8]))
	if ifdOffset < 2 || ifdOffset+2 > len(tiff) {
		return 1
	}
	entryCount := int(order.Uint16(tiff[ifdOffset : ifdOffset+2]))
	for i := 0; i < entryCount; i++ {
		entryStart := ifdOffset + 2 + i*12
		if entryStart+12 > len(tiff) {
			break
		}
		entry := tiff[entryStart : entryStart+12]
		if order.Uint16(entry[0:2]) != 0x0112 {
			continue
		}
		value := int(order.Uint16(entry[8:10]))
		if value >= 1 && value <= 8 {
			return value
		}
		return 1
	}
	return 1
}
