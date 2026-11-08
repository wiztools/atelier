// Package mediasnifftest builds minimal media byte sequences for tests: WAV
// and MP3 streams, MP4 containers with chosen durations and tracks, encoded
// images, and a JPEG carrying an EXIF orientation. Each fixture is just enough
// structure for the mediasniff readers (and the tools that call them) to parse.
package mediasnifftest

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

// FTYP is a bare 24-byte ftyp box: enough for a sniffer to see an MP4
// container, with no moov, so every duration and track probe must refuse it.
func FTYP() []byte {
	return []byte{
		0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm',
		0x00, 0x00, 0x02, 0x00, 'i', 's', 'o', 'm', 'i', 's', 'o', '2',
	}
}

// WAV builds a minimal PCM WAV: 8 kHz mono 8-bit (byte rate 8000), so
// dataSize bytes of payload equal dataSize/8000 seconds.
func WAV(dataSize uint32) []byte {
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))    // PCM
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))    // mono
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8000)) // sample rate
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8000)) // byte rate
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))    // block align
	_ = binary.Write(&buf, binary.LittleEndian, uint16(8))    // bits per sample
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, dataSize)
	buf.Write(bytes.Repeat([]byte{0x55}, int(dataSize)))
	return buf.Bytes()
}

// MP3 builds n MPEG1 Layer III frames — 128 kbps, 44.1 kHz, 417 bytes and
// 1152 samples each (the same header bytes the fal harness tests serve as
// downloadable MP3s).
func MP3(frames int) []byte {
	frame := make([]byte, 417)
	frame[0], frame[1], frame[2], frame[3] = 0xFF, 0xFB, 0x90, 0x00
	return bytes.Repeat(frame, frames)
}

// MP4WithDuration builds a minimal MP4 byte sequence — ftyp + moov +
// mvhd — carrying the given timescale and duration so the duration probe has
// a real container to parse. version selects mvhd box version 0 or 1. The
// header layout follows ISO BMFF: version+flags, then creation+modification
// (4+4 bytes in v0, 8+8 in v1), then timescale, then duration — no reserved
// fields between them.
func MP4WithDuration(version byte, timescale, duration uint32) []byte {
	var mvhd []byte
	mvhd = append(mvhd, version, 0, 0, 0) // version + flags
	if version == 1 {
		mvhd = append(mvhd, make([]byte, 16)...) // creation + modification (64-bit)
		mvhd = binary.BigEndian.AppendUint32(mvhd, timescale)
		mvhd = binary.BigEndian.AppendUint64(mvhd, uint64(duration))
	} else {
		mvhd = append(mvhd, make([]byte, 8)...) // creation + modification (32-bit)
		mvhd = binary.BigEndian.AppendUint32(mvhd, timescale)
		mvhd = binary.BigEndian.AppendUint32(mvhd, duration)
	}
	// rate + volume + matrix + predefined + next track id pad the payload to
	// the classic 0x6c-byte box; the parser only reads the header fields, but
	// a full-size box keeps the fixture shaped like real output.
	if pad := 0x64 - len(mvhd); pad > 0 {
		mvhd = append(mvhd, make([]byte, pad)...)
	}
	mvhdBox := append(binary.BigEndian.AppendUint32(nil, uint32(len(mvhd)+8)), []byte("mvhd")...)
	mvhdBox = append(mvhdBox, mvhd...)
	moovBox := append(binary.BigEndian.AppendUint32(nil, uint32(len(mvhdBox)+8)), []byte("moov")...)
	moovBox = append(moovBox, mvhdBox...)
	out := FTYP()
	return append(out, moovBox...)
}

// MP4Box builds one length-prefixed ISO BMFF box around a payload.
func MP4Box(boxType string, payload []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(payload)+8))
	out = append(out, boxType...)
	return append(out, payload...)
}

// MP4WithVideoTrack builds ftyp + moov{video trak, audio trak}. The
// video track's tkhd carries the 16.16 fixed-point presentation size and its
// stbl one stts entry per sampleCounts element (their sum is the frame
// count); the trailing audio trak proves the probes pick the 'vide' handler.
// A call with no sampleCounts omits the sample table entirely — the
// frame-count-unparseable shape.
func MP4WithVideoTrack(width, height uint32, sampleCounts ...uint32) []byte {
	tkhd := make([]byte, 84) // v0 layout; width/height close the box as 16.16
	binary.BigEndian.PutUint32(tkhd[76:80], width<<16)
	binary.BigEndian.PutUint32(tkhd[80:84], height<<16)

	videoHdlr := make([]byte, 24)
	copy(videoHdlr[8:12], "vide")

	mdiaPayload := MP4Box("hdlr", videoHdlr)
	if len(sampleCounts) > 0 {
		stts := make([]byte, 8, 8+len(sampleCounts)*8)
		binary.BigEndian.PutUint32(stts[4:8], uint32(len(sampleCounts)))
		for _, count := range sampleCounts {
			stts = binary.BigEndian.AppendUint32(stts, count)
			stts = binary.BigEndian.AppendUint32(stts, 1) // sample_delta
		}
		mdiaPayload = append(mdiaPayload,
			MP4Box("minf", MP4Box("stbl", MP4Box("stts", stts)))...)
	}
	videoTrak := MP4Box("trak", append(MP4Box("tkhd", tkhd), MP4Box("mdia", mdiaPayload)...))

	audioHdlr := make([]byte, 24)
	copy(audioHdlr[8:12], "soun")
	audioTrak := MP4Box("trak", MP4Box("mdia", MP4Box("hdlr", audioHdlr)))

	moov := MP4Box("moov", append(videoTrak, audioTrak...))
	return append(FTYP(), moov...)
}

// MP4AudioOnly builds ftyp + moov{trak} with no video track — the
// shape every video probe must refuse.
func MP4AudioOnly() []byte {
	audioHdlr := make([]byte, 24)
	copy(audioHdlr[8:12], "soun")
	moov := MP4Box("moov", MP4Box("trak", MP4Box("mdia", MP4Box("hdlr", audioHdlr))))
	return append(FTYP(), moov...)
}

// MP4VideoOnly builds ftyp + moov{video trak} with no audio track —
// the silent-clip shape the portion-speed audio sniff must recognize.
func MP4VideoOnly() []byte {
	tkhd := make([]byte, 84)
	binary.BigEndian.PutUint32(tkhd[76:80], 1344<<16)
	binary.BigEndian.PutUint32(tkhd[80:84], 768<<16)
	videoHdlr := make([]byte, 24)
	copy(videoHdlr[8:12], "vide")
	videoTrak := MP4Box("trak", append(MP4Box("tkhd", tkhd), MP4Box("mdia", MP4Box("hdlr", videoHdlr))...))
	return append(FTYP(), MP4Box("moov", videoTrak)...)
}

// MP4WithSampleEntry builds ftyp + moov{video trak} whose tkhd carries
// the display (presentation) size and whose stsd sample entry carries the
// coded size — anamorphic when they differ (players stretch the coded frames
// to the tkhd size), square when they match.
func MP4WithSampleEntry(displayWidth, displayHeight, codedWidth, codedHeight uint32) []byte {
	tkhd := make([]byte, 84)
	binary.BigEndian.PutUint32(tkhd[76:80], displayWidth<<16)
	binary.BigEndian.PutUint32(tkhd[80:84], displayHeight<<16)
	videoHdlr := make([]byte, 24)
	copy(videoHdlr[8:12], "vide")
	// A VisualSampleEntry fixed header: width/height ride as the uint16 pair
	// at payload offsets 24/26; the trailing fields stay zeroed — the coded
	// reader only walks that far.
	entry := make([]byte, 78)
	binary.BigEndian.PutUint16(entry[24:26], uint16(codedWidth))
	binary.BigEndian.PutUint16(entry[26:28], uint16(codedHeight))
	stsd := append(make([]byte, 8), MP4Box("avc1", entry)...) // version+flags + entry_count
	mdiaPayload := append(MP4Box("hdlr", videoHdlr),
		MP4Box("minf", MP4Box("stbl", MP4Box("stsd", stsd)))...)
	videoTrak := MP4Box("trak", append(MP4Box("tkhd", tkhd), MP4Box("mdia", mdiaPayload)...))
	return append(FTYP(), MP4Box("moov", videoTrak)...)
}

// Image encodes a zero-filled image of the given size in one of the
// formats fal returns for image generation.
func Image(t testing.TB, format string, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&buf, img)
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	default:
		t.Fatalf("unknown fixture format %q", format)
	}
	if err != nil {
		t.Fatalf("encode %s fixture: %v", format, err)
	}
	return buf.Bytes()
}

// EXIFJPEG crafts a minimal JPEG marker stream carrying an APP1 Exif
// segment whose IFD0 holds exactly one entry: the orientation tag. The parser
// only walks markers and the TIFF header, so no entropy-coded data is needed —
// the SOS marker terminates the walk.
func EXIFJPEG(orientation uint16) []byte {
	tiff := []byte{'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}
	tiff = append(tiff, 0x01, 0x00)             // one IFD entry
	tiff = append(tiff, 0x12, 0x01)             // tag 0x0112 (orientation)
	tiff = append(tiff, 0x03, 0x00)             // type SHORT
	tiff = append(tiff, 0x01, 0x00, 0x00, 0x00) // count 1
	tiff = append(tiff, byte(orientation), 0x00, 0x00, 0x00)
	tiff = append(tiff, 0x00, 0x00, 0x00, 0x00) // next IFD offset
	exif := append([]byte("Exif\x00\x00"), tiff...)
	out := []byte{0xFF, 0xD8}
	out = append(out, 0xFF, 0xE1, 0x00, byte(len(exif)+2))
	out = append(out, exif...)
	return append(out, 0xFF, 0xDA)
}
