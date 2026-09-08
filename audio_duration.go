package main

// Audio duration probing, pure Go. extend_audio's stable-audio inpaint path
// needs the source clip's length to place its mask (mask_start = clip length
// for an append), but by the time the gateway submits to fal the clip is just
// a URL — so the length is sniffed from the artifact bytes before upload, the
// same moment the media resolver still holds the decoded data. Every parser
// here is deliberately conservative: on any structural doubt it reports
// !ok rather than a guess, because a wrong length would place the mask
// mid-audio and mangle the clip instead of extending it. Callers degrade to
// the duration-free extend models (ace-step, sonauto) when the length is
// unknown.

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"strings"
)

// probeAudioDuration returns the clip's duration in seconds for the formats
// Atelier persists or accepts as attachments: WAV, MP3 (CBR and VBR via a
// frame walk), AAC (ADTS), FLAC, OGG (Vorbis/Opus), and M4A/MP4. ok is false
// for anything it cannot parse with confidence.
func probeAudioDuration(data []byte) (seconds float64, ok bool) {
	switch {
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")):
		return wavDuration(data)
	case len(data) >= 4 && bytes.Equal(data[:4], []byte("fLaC")):
		return flacDuration(data)
	case len(data) >= 4 && bytes.Equal(data[:4], []byte("OggS")):
		return oggDuration(data)
	case len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")):
		return mp4Duration(data)
	case len(data) >= 3 && bytes.Equal(data[:3], []byte("ID3")):
		return mpegDuration(data)
	case len(data) >= 4 && data[0] == 0xFF && (data[1]&0xE0) == 0xE0:
		return mpegDuration(data)
	}
	return 0, false
}

// dataURLAudioDuration decodes a data-URL media reference and probes it. A
// non-data URL or undecodable payload reports !ok — the caller decides how to
// degrade (see the extend gateway).
func dataURLAudioDuration(dataURL string) (seconds float64, ok bool) {
	trimmed := strings.TrimSpace(dataURL)
	if !strings.HasPrefix(trimmed, "data:") {
		return 0, false
	}
	comma := strings.Index(trimmed, ",")
	if comma < 0 {
		return 0, false
	}
	data, err := base64.StdEncoding.DecodeString(trimmed[comma+1:])
	if err != nil {
		return 0, false
	}
	return probeAudioDuration(data)
}

// wavDuration walks RIFF chunks for the fmt byte rate and the data payload
// size. Chunks are word-aligned.
func wavDuration(data []byte) (float64, bool) {
	var byteRate uint32
	var payload uint64
	pos := 12 // past "RIFF" size "WAVE"
	for pos+8 <= len(data) {
		id := data[pos : pos+4]
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := pos + 8
		if size < 0 || body+size > len(data) {
			break
		}
		switch {
		case bytes.Equal(id, []byte("fmt ")) && size >= 16:
			byteRate = binary.LittleEndian.Uint32(data[body+8 : body+12])
		case bytes.Equal(id, []byte("data")):
			payload = uint64(size)
		}
		pos = body + size + size%2
	}
	if byteRate == 0 || payload == 0 {
		return 0, false
	}
	return float64(payload) / float64(byteRate), true
}

// flacDuration reads the STREAMINFO block: an 8-byte packed field holding the
// 20-bit sample rate and 36-bit total sample count.
func flacDuration(data []byte) (float64, bool) {
	pos := 4 // past "fLaC"
	for pos+4 <= len(data) {
		header := data[pos]
		size := int(data[pos+1])<<16 | int(data[pos+2])<<8 | int(data[pos+3])
		body := pos + 4
		if header&0x7F == 0 && size >= 18 && body+18 <= len(data) {
			packed := binary.BigEndian.Uint64(data[body+10 : body+18])
			sampleRate := packed >> 44 & 0xFFFFF
			totalSamples := packed & 0xFFFFFFFFF
			if sampleRate == 0 || totalSamples == 0 {
				return 0, false
			}
			return float64(totalSamples) / float64(sampleRate), true
		}
		if header&0x80 != 0 {
			break // last-metadata-block flag
		}
		pos = body + size
	}
	return 0, false
}

// oggDuration divides the last page's granule position (the logical stream's
// total PCM samples) by the codec's sample rate. Vorbis declares its rate in
// the ID header; Opus granule positions always run at 48 kHz.
func oggDuration(data []byte) (float64, bool) {
	idx := bytes.LastIndex(data, []byte("OggS"))
	if idx < 0 || idx+14 > len(data) || data[idx+4] != 0 {
		return 0, false
	}
	granule := int64(binary.LittleEndian.Uint64(data[idx+6 : idx+14]))
	if granule <= 0 || len(data) < 28 {
		return 0, false
	}
	// First page: 27-byte header, segment table, then the codec ID packet.
	packet := 27 + int(data[26])
	rate := 0.0
	if packet+16 <= len(data) && data[packet] == 0x01 && bytes.Equal(data[packet+1:packet+7], []byte("vorbis")) {
		rate = float64(binary.LittleEndian.Uint32(data[packet+12 : packet+16]))
	} else if packet+8 <= len(data) && bytes.Equal(data[packet:packet+8], []byte("OpusHead")) {
		rate = 48000
	}
	if rate <= 0 {
		return 0, false
	}
	return float64(granule) / rate, true
}

// mp4Duration walks ISO-boxes to moov/mvhd and divides the duration by the
// movie timescale. Only 32-bit box sizes are handled (a 64-bit size never
// appears on this path in practice).
func mp4Duration(data []byte) (float64, bool) {
	findBox := func(start, end int, want string) (int, int, bool) {
		for pos := start; pos+8 <= end; {
			size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
			boxType := string(data[pos+4 : pos+8])
			if size == 0 {
				size = end - pos // box runs to the end of the buffer
			}
			if size < 8 || pos+size > end {
				return 0, 0, false
			}
			if boxType == want {
				return pos + 8, pos + size, true
			}
			pos += size
		}
		return 0, 0, false
	}
	moovStart, moovEnd, ok := findBox(0, len(data), "moov")
	if !ok {
		return 0, false
	}
	mvhdStart, mvhdEnd, ok := findBox(moovStart, moovEnd, "mvhd")
	if !ok {
		return 0, false
	}
	var timescale, duration uint64
	switch data[mvhdStart] {
	case 0:
		if mvhdEnd-mvhdStart < 20 {
			return 0, false
		}
		timescale = uint64(binary.BigEndian.Uint32(data[mvhdStart+12 : mvhdStart+16]))
		duration = uint64(binary.BigEndian.Uint32(data[mvhdStart+16 : mvhdStart+20]))
	case 1:
		if mvhdEnd-mvhdStart < 32 {
			return 0, false
		}
		timescale = uint64(binary.BigEndian.Uint32(data[mvhdStart+20 : mvhdStart+24]))
		duration = binary.BigEndian.Uint64(data[mvhdStart+24 : mvhdStart+32])
	default:
		return 0, false
	}
	if timescale == 0 || duration == 0 {
		return 0, false
	}
	return float64(duration) / float64(timescale), true
}

// mpegDuration probes raw MPEG-audio streams: an optional ID3v2 tag, then a
// frame walk (exact for VBR). The frame header's layer bits split MP3
// (layers I–III) from AAC (ADTS carries layer 0).
func mpegDuration(data []byte) (float64, bool) {
	if len(data) >= 10 && bytes.Equal(data[:3], []byte("ID3")) {
		// ID3v2 size is a 28-bit syncsafe integer after the 10-byte header.
		size := int(data[6]&0x7F)<<21 | int(data[7]&0x7F)<<14 | int(data[8]&0x7F)<<7 | int(data[9]&0x7F)
		size += 10
		if size >= len(data) {
			return 0, false
		}
		data = data[size:]
	}
	if len(data) < 4 || data[0] != 0xFF || data[1]&0xE0 != 0xE0 {
		return 0, false
	}
	if data[1]>>1&0x3 == 0 {
		return adtsDuration(data)
	}
	return mp3Duration(data)
}

var adtsSampleRates = [...]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

// adtsDuration walks ADTS frames — 1024 samples each, 13-bit frame length.
func adtsDuration(data []byte) (float64, bool) {
	var samples, rate float64
	pos := 0
	for pos+7 <= len(data) {
		if data[pos] != 0xFF || data[pos+1]&0xE0 != 0xE0 || data[pos+1]>>1&0x3 != 0 {
			break
		}
		idx := data[pos+2] >> 2 & 0xF
		if idx >= uint8(len(adtsSampleRates)) {
			return 0, false
		}
		frameLen := int(data[pos+3]&0x3)<<11 | int(data[pos+4])<<3 | int(data[pos+5]>>5)
		if frameLen <= 0 {
			return 0, false
		}
		samples += 1024
		rate = float64(adtsSampleRates[idx])
		pos += frameLen
	}
	if samples == 0 || rate == 0 {
		return 0, false
	}
	return samples / rate, true
}

// mp3 bitrates in kbps, indexed [version-is-MPEG1][layer] then bitrate index.
// Layer order matches the header encoding shifted to 0=I, 1=II, 2=III.
var mp3Bitrates = [2][3][15]int{
	{ // MPEG2 / MPEG2.5
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256}, // Layer I
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},      // Layer II
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},      // Layer III
	},
	{ // MPEG1
		{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448}, // Layer I
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384},    // Layer II
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},     // Layer III
	},
}

var mp3SampleRates = map[int][3]int{
	3: {44100, 48000, 32000}, // MPEG1
	2: {22050, 24000, 16000}, // MPEG2
	0: {11025, 12000, 8000},  // MPEG2.5
}

// mp3Duration sums frames: samples-per-frame and frame length both depend on
// the MPEG version and layer. The walk stops at the first broken sync —
// trailing tags (ID3v1) and junk end it, and any frames walked so far still
// yield the exact elapsed audio.
func mp3Duration(data []byte) (float64, bool) {
	totalSamples, rate := 0.0, 0.0
	pos := 0
	for pos+4 <= len(data) {
		b1, b2 := data[pos+1], data[pos+2]
		if data[pos] != 0xFF || b1&0xE0 != 0xE0 {
			break
		}
		version := int(b1 >> 3 & 0x3)   // 3=MPEG1, 2=MPEG2, 0=MPEG2.5, 1=reserved
		layerBits := int(b1 >> 1 & 0x3) // 3=Layer I, 2=Layer II, 1=Layer III, 0=AAC
		if version == 1 || layerBits == 0 {
			return 0, false
		}
		rates, ok := mp3SampleRates[version]
		if !ok {
			return 0, false
		}
		rateIdx := int(b2 >> 2 & 0x3)
		bitrateIdx := int(b2 >> 4 & 0xF)
		if rateIdx == 3 || bitrateIdx == 0 || bitrateIdx == 15 {
			return 0, false
		}
		isMPEG1 := 0
		if version == 3 {
			isMPEG1 = 1
		}
		layer := 3 - layerBits // 0=Layer I, 1=Layer II, 2=Layer III
		bitrate := mp3Bitrates[isMPEG1][layer][bitrateIdx] * 1000
		rate = float64(rates[rateIdx])
		pad := int(b2 >> 1 & 0x1)
		var samplesPerFrame, frameLen int
		switch {
		case layer == 0:
			samplesPerFrame, frameLen = 384, (12*bitrate/int(rate)+pad)*4
		case layer == 1, layer == 2 && isMPEG1 == 1:
			samplesPerFrame, frameLen = 1152, 144*bitrate/int(rate)+pad
		default: // MPEG2/2.5 Layer III
			samplesPerFrame, frameLen = 576, 72*bitrate/int(rate)+pad
		}
		if frameLen <= 0 {
			return 0, false
		}
		totalSamples += float64(samplesPerFrame)
		pos += frameLen
	}
	if totalSamples == 0 || rate == 0 {
		return 0, false
	}
	return totalSamples / rate, true
}
