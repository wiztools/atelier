package mediasniff

import "encoding/binary"

// MP4Duration reads a generated clip's exact duration from its MP4
// container — the moov/mvhd box's duration ÷ timescale. The gateway already
// holds the downloaded bytes, so this prices token- and per-second models by
// what was actually rendered (a planner-omitted or "auto" duration is
// unknowable from the request). Pure byte parsing, no ffprobe dependency;
// fail-soft on any non-MP4 or malformed payload.
func MP4Duration(data []byte) (float64, bool) {
	if len(data) < 8 {
		return 0, false
	}
	// ftyp must lead a valid MP4; a different container (MOV variants, webm)
	// is out of scope for the probe.
	if string(data[4:8]) != "ftyp" {
		return 0, false
	}
	moov, ok := mp4ChildBox(data, "moov")
	if !ok {
		return 0, false
	}
	mvhd, ok := mp4ChildBox(moov, "mvhd")
	if !ok || len(mvhd) < 4 {
		return 0, false
	}
	// mvhd payload: version(1) flags(3), then v0 [created(4) modified(4)
	// timescale(4) duration(4)] or v1 [created(8) modified(8) timescale(4)
	// duration(8)].
	var timescale, duration float64
	switch mvhd[0] {
	case 1:
		if len(mvhd) < 32 {
			return 0, false
		}
		timescale = float64(binary.BigEndian.Uint32(mvhd[20:24]))
		duration = float64(binary.BigEndian.Uint64(mvhd[24:32]))
	default:
		if len(mvhd) < 20 {
			return 0, false
		}
		timescale = float64(binary.BigEndian.Uint32(mvhd[12:16]))
		duration = float64(binary.BigEndian.Uint32(mvhd[16:20]))
	}
	if timescale <= 0 || duration <= 0 {
		return 0, false
	}
	return duration / timescale, true
}

// mp4ChildBox scans one container's boxes for the first of the wanted
// type and returns its payload. Handles the 32-bit size form plus size==0
// (box runs to end of data) and size==1 (64-bit largesize).
func mp4ChildBox(data []byte, boxType string) ([]byte, bool) {
	for offset := 0; offset+8 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		headerLen := uint64(8)
		if size == 1 {
			if offset+16 > len(data) {
				return nil, false
			}
			size = binary.BigEndian.Uint64(data[offset+8 : offset+16])
			headerLen = 16
		} else if size == 0 {
			size = uint64(len(data) - offset)
		}
		if size < headerLen || offset+int(size) > len(data) {
			return nil, false
		}
		if string(data[offset+4:offset+8]) == boxType {
			return data[offset+int(headerLen) : offset+int(size)], true
		}
		offset += int(size)
	}
	return nil, false
}

// mp4EachChild walks one container's immediate child boxes, calling visit with
// each box's type and payload. The walk stops early when visit returns false.
// Shares mp4ChildBox's size-form handling (32-bit, 64-bit largesize,
// size==0 to end of data); a malformed box truncates the walk.
func mp4EachChild(data []byte, visit func(boxType string, payload []byte) bool) {
	for offset := 0; offset+8 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		headerLen := uint64(8)
		if size == 1 {
			if offset+16 > len(data) {
				return
			}
			size = binary.BigEndian.Uint64(data[offset+8 : offset+16])
			headerLen = 16
		} else if size == 0 {
			size = uint64(len(data) - offset)
		}
		if size < headerLen || offset+int(size) > len(data) {
			return
		}
		if !visit(string(data[offset+4:offset+8]), data[offset+int(headerLen):offset+int(size)]) {
			return
		}
		offset += int(size)
	}
}

// mp4VideoTrak returns the payload of moov's first trak whose mdia/hdlr names
// the 'vide' handler — the video track, whose tkhd/stts carry the frame size
// and count pricing needs. ok is false when no video track parses; an
// audio-only container is not priced.
func mp4VideoTrak(data []byte) ([]byte, bool) {
	moov, ok := mp4ChildBox(data, "moov")
	if !ok {
		return nil, false
	}
	var videoTrak []byte
	mp4EachChild(moov, func(boxType string, payload []byte) bool {
		if boxType != "trak" {
			return true
		}
		mdia, ok := mp4ChildBox(payload, "mdia")
		if !ok {
			return true
		}
		hdlr, ok := mp4ChildBox(mdia, "hdlr")
		// hdlr payload: version+flags(4), pre_defined(4), then the 4-byte
		// handler_type ('vide' for a video track, 'soun' for audio).
		if !ok || len(hdlr) < 12 || string(hdlr[8:12]) != "vide" {
			return true
		}
		videoTrak = payload
		return false
	})
	if videoTrak == nil {
		return nil, false
	}
	return videoTrak, true
}

// MP4HasAudioTrack reports whether the MP4 carries an audio track — any moov
// trak whose mdia/hdlr names the 'soun' handler. Pure byte parsing, fail-soft
// false on malformed payloads; the ffmpeg portion-speed tool reads it when
// ffprobe is unavailable, because a filter_complex must not reference [0:a]
// on a silent clip.
func MP4HasAudioTrack(data []byte) bool {
	moov, ok := mp4ChildBox(data, "moov")
	if !ok {
		return false
	}
	found := false
	mp4EachChild(moov, func(boxType string, payload []byte) bool {
		if boxType != "trak" {
			return true
		}
		mdia, ok := mp4ChildBox(payload, "mdia")
		if !ok {
			return true
		}
		hdlr, ok := mp4ChildBox(mdia, "hdlr")
		if ok && len(hdlr) >= 12 && string(hdlr[8:12]) == "soun" {
			found = true
			return false
		}
		return true
	})
	return found
}

// MP4VideoDimensions reads the video track's presentation size from tkhd —
// width and height ride as 32-bit 16.16 fixed-point values. This is the
// effective render size: a model whose video_size is "auto" inherits the
// source clip's dimensions, and the output carries what was actually rendered.
func MP4VideoDimensions(data []byte) (float64, float64, bool) {
	trak, ok := mp4VideoTrak(data)
	if !ok {
		return 0, 0, false
	}
	tkhd, ok := mp4ChildBox(trak, "tkhd")
	if !ok || len(tkhd) < 4 {
		return 0, 0, false
	}
	// tkhd payload: version(1) flags(3), then v0 [created(4) modified(4)
	// trackID(4) reserved(4) duration(4) reserved(8) layer(2) altGroup(2)
	// volume(2) reserved(2) matrix(36)] or v1 [created(8) modified(8)
	// trackID(4) reserved(4) duration(8) + the same trailer], with width and
	// height's 16.16 fixed-point pair closing the box.
	widthOffset := 76
	if tkhd[0] == 1 {
		widthOffset = 88
	}
	if len(tkhd) < widthOffset+8 {
		return 0, 0, false
	}
	width := float64(binary.BigEndian.Uint32(tkhd[widthOffset:widthOffset+4])) / 65536
	height := float64(binary.BigEndian.Uint32(tkhd[widthOffset+4:widthOffset+8])) / 65536
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// MP4CodedVideoDimensions reads the video track's coded frame size from the
// first stsd sample entry — the VisualSampleEntry fixed header's uint16
// width/height pair, the size the decoder produces, versus tkhd's
// presentation size. The ffmpeg screenshot tool compares the two: a
// difference is an anamorphic container, whose players stretch the coded
// frames to the tkhd size. Pure byte parsing, fail-soft on anything
// unparseable; an MP4 without a readable sample table reports no coded size.
func MP4CodedVideoDimensions(data []byte) (int, int, bool) {
	trak, ok := mp4VideoTrak(data)
	if !ok {
		return 0, 0, false
	}
	mdia, ok := mp4ChildBox(trak, "mdia")
	if !ok {
		return 0, 0, false
	}
	minf, ok := mp4ChildBox(mdia, "minf")
	if !ok {
		return 0, 0, false
	}
	stbl, ok := mp4ChildBox(minf, "stbl")
	if !ok {
		return 0, 0, false
	}
	stsd, ok := mp4ChildBox(stbl, "stsd")
	if !ok || len(stsd) < 8 {
		return 0, 0, false
	}
	// stsd payload: version+flags(4) entry_count(4), then the sample entries;
	// a VisualSampleEntry's fixed header carries width(2)/height(2) at its
	// payload offsets 24/26.
	width, height := 0, 0
	mp4EachChild(stsd[8:], func(boxType string, payload []byte) bool {
		if len(payload) < 28 {
			return true
		}
		encodedWidth := int(binary.BigEndian.Uint16(payload[24:26]))
		encodedHeight := int(binary.BigEndian.Uint16(payload[26:28]))
		if encodedWidth > 0 && encodedHeight > 0 {
			width, height = encodedWidth, encodedHeight
			return false
		}
		return true
	})
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// MP4VideoFrameCount sums the video track's stts sample counts — every frame
// the container actually carries. Exact for a purely generated clip, where the
// output holds exactly the frames the model rendered.
func MP4VideoFrameCount(data []byte) (float64, bool) {
	trak, ok := mp4VideoTrak(data)
	if !ok {
		return 0, false
	}
	mdia, ok := mp4ChildBox(trak, "mdia")
	if !ok {
		return 0, false
	}
	minf, ok := mp4ChildBox(mdia, "minf")
	if !ok {
		return 0, false
	}
	stbl, ok := mp4ChildBox(minf, "stbl")
	if !ok {
		return 0, false
	}
	stts, ok := mp4ChildBox(stbl, "stts")
	if !ok || len(stts) < 8 {
		return 0, false
	}
	// stts payload: version+flags(4), entry_count(4), then run-length entries
	// of {sample_count(4), sample_delta(4)}.
	entries := int(binary.BigEndian.Uint32(stts[4:8]))
	if entries < 0 || 8+entries*8 > len(stts) {
		return 0, false
	}
	total := 0.0
	for i := 0; i < entries; i++ {
		offset := 8 + i*8
		total += float64(binary.BigEndian.Uint32(stts[offset : offset+4]))
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}
