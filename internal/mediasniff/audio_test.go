package mediasniff

import (
	"encoding/base64"
	"encoding/binary"
	"testing"

	"atelier/internal/mediasniff/mediasnifftest"
)

// buildID3v2 prepends a 10-byte ID3v2 header declaring pad bytes of padding.
func buildID3v2(pad int) []byte {
	out := []byte{'I', 'D', '3', 4, 0, 0}
	size := uint32(pad)
	out = append(out, byte(size>>21&0x7F), byte(size>>14&0x7F), byte(size>>7&0x7F), byte(size&0x7F))
	return append(out, make([]byte, pad)...)
}

// buildFLAC builds a FLAC file whose STREAMINFO declares the given sample
// rate and total sample count.
func buildFLAC(sampleRate uint32, totalSamples uint64) []byte {
	streamInfo := make([]byte, 34)
	packed := uint64(sampleRate)<<44 | totalSamples&0xFFFFFFFFF
	binary.BigEndian.PutUint64(streamInfo[10:18], packed)
	out := []byte("fLaC")
	out = append(out, 0x80) // last metadata block, type 0 (STREAMINFO)
	out = append(out, 0, 0, 34)
	return append(out, streamInfo...)
}

// buildOGGVorbis builds a two-page Vorbis stream: a first page carrying the
// ID packet (with the declared sample rate) and a last page whose granule
// position is the total sample count.
func buildOGGVorbis(rate uint32, totalSamples int64) []byte {
	packet := make([]byte, 16)
	copy(packet, "\x01vorbis")
	packet[11] = 2 // channels
	binary.LittleEndian.PutUint32(packet[12:16], rate)

	page := func(headerType byte, granule int64, payload []byte) []byte {
		page := make([]byte, 27)
		copy(page, "OggS")
		page[4] = 0
		page[5] = headerType
		binary.LittleEndian.PutUint64(page[6:14], uint64(granule))
		binary.LittleEndian.PutUint32(page[14:18], 1) // serial
		binary.LittleEndian.PutUint32(page[18:22], 0) // sequence
		if payload == nil {
			return page // zero segment laces
		}
		page[26] = 1 // one segment
		return append(append(page, byte(len(payload))), payload...)
	}
	out := page(0x02, 0, packet) // beginning of stream
	return append(out, page(0x04, totalSamples, nil)...)
}

// buildM4A builds an MP4 file (ftyp + moov/mvhd v0) declaring the given
// timescale and duration.
func buildM4A(timescale, duration uint32) []byte {
	box := func(kind string, body []byte) []byte {
		out := make([]byte, 8+len(body))
		binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
		copy(out[4:8], kind)
		copy(out[8:], body)
		return out
	}
	mvhd := make([]byte, 20)
	binary.BigEndian.PutUint32(mvhd[12:16], timescale)
	binary.BigEndian.PutUint32(mvhd[16:20], duration)
	file := box("ftyp", []byte("M4A \x00\x00\x00\x00M4A "))
	return append(file, box("moov", box("mvhd", mvhd))...)
}

func TestProbeAudioDuration(t *testing.T) {
	cases := []struct {
		name   string
		data   []byte
		want   float64
		wantOK bool
	}{
		{"wav 1s", mediasnifftest.WAV(8000), 1.0, true},
		{"wav 2.5s", mediasnifftest.WAV(20000), 2.5, true},
		{"wav empty data chunk", mediasnifftest.WAV(0), 0, false},
		{"mp3 ten frames", mediasnifftest.MP3(10), 10 * 1152.0 / 44100, true},
		{"mp3 one frame", mediasnifftest.MP3(1), 1152.0 / 44100, true},
		{"mp3 behind id3v2", append(buildID3v2(16), mediasnifftest.MP3(4)...), 4 * 1152.0 / 44100, true},
		{"mp3 junk after frames stops the walk", append(mediasnifftest.MP3(2), []byte("TAG trailing id3v1 block")...), 2 * 1152.0 / 44100, true},
		{"flac 2s", buildFLAC(44100, 88200), 2.0, true},
		{"flac unknown total samples", buildFLAC(44100, 0), 0, false},
		{"ogg vorbis 2s", buildOGGVorbis(22050, 44100), 2.0, true},
		{"m4a 1.5s", buildM4A(1000, 1500), 1.5, true},
		{"m4a without moov", buildM4A(1000, 1500)[:24], 0, false},
		{"unknown bytes", []byte("not audio at all"), 0, false},
		{"empty", nil, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := AudioDuration(tc.data)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got %v)", ok, tc.wantOK, got)
			}
			if ok && got != tc.want {
				t.Fatalf("duration = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDataURLAudioDuration(t *testing.T) {
	dataURL := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(mediasnifftest.WAV(8000))
	if secs, ok := DataURLAudioDuration(dataURL); !ok || secs != 1.0 {
		t.Fatalf("DataURLAudioDuration(wav) = %v, %v; want 1.0, true", secs, ok)
	}
	if _, ok := DataURLAudioDuration("https://example.com/clip.mp3"); ok {
		t.Fatal("a remote URL is not probeable; want ok=false")
	}
	if _, ok := DataURLAudioDuration("data:audio/mpeg;base64,!!!not-base64!!!"); ok {
		t.Fatal("undecodable payload; want ok=false")
	}
}
