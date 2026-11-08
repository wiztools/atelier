package mediasniff

import (
	"testing"

	"atelier/internal/mediasniff/mediasnifftest"
)

func TestMP4Duration(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want float64
	}{
		{name: "v0 mvhd", data: mediasnifftest.MP4WithDuration(0, 1000, 10500), want: 10.5},
		{name: "v1 mvhd", data: mediasnifftest.MP4WithDuration(1, 24000, 240000), want: 10},
		{name: "no moov", data: mediasnifftest.FTYP(), want: 0},
		{name: "not mp4", data: []byte("not a video at all"), want: 0},
		{name: "empty", data: nil, want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := MP4Duration(tt.data)
			if tt.want > 0 {
				if !ok || got != tt.want {
					t.Fatalf("MP4Duration = (%v, %v), want (%v, true)", got, ok, tt.want)
				}
			} else if ok {
				t.Fatalf("MP4Duration should fail-soft, got (%v, true)", got)
			}
		})
	}
}

// TestMP4CodedVideoDimensions pins the stsd sample-entry read: the coded size
// of an anamorphic or square track, and fail-soft everywhere the sample table
// is missing or unreadable.
func TestMP4CodedVideoDimensions(t *testing.T) {
	for _, tt := range []struct {
		name       string
		data       []byte
		wantWidth  int
		wantHeight int
		wantOK     bool
	}{
		{name: "anamorphic track", data: mediasnifftest.MP4WithSampleEntry(1280, 720, 960, 720), wantWidth: 960, wantHeight: 720, wantOK: true},
		{name: "square track", data: mediasnifftest.MP4WithSampleEntry(1344, 768, 1344, 768), wantWidth: 1344, wantHeight: 768, wantOK: true},
		{name: "no sample table", data: mediasnifftest.MP4WithVideoTrack(1344, 768), wantOK: false},
		{name: "video trak without stsd", data: mediasnifftest.MP4VideoOnly(), wantOK: false},
		{name: "no moov", data: mediasnifftest.FTYP(), wantOK: false},
		{name: "not mp4", data: []byte("not a video at all"), wantOK: false},
		{name: "empty", data: nil, wantOK: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			width, height, ok := MP4CodedVideoDimensions(tt.data)
			if ok != tt.wantOK || (ok && (width != tt.wantWidth || height != tt.wantHeight)) {
				t.Fatalf("MP4CodedVideoDimensions = (%d, %d, %v), want (%d, %d, %v)", width, height, ok, tt.wantWidth, tt.wantHeight, tt.wantOK)
			}
		})
	}
}

func TestMP4HasAudioTrack(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want bool
	}{
		{name: "video and audio tracks", data: mediasnifftest.MP4WithVideoTrack(1344, 768, 339), want: true},
		{name: "audio only", data: mediasnifftest.MP4AudioOnly(), want: true},
		{name: "video only", data: mediasnifftest.MP4VideoOnly(), want: false},
		{name: "no trak at all", data: mediasnifftest.MP4WithDuration(0, 1000, 10500), want: false},
		{name: "no moov", data: mediasnifftest.FTYP(), want: false},
		{name: "not mp4", data: []byte("not a video at all"), want: false},
		{name: "empty", data: nil, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := MP4HasAudioTrack(tt.data); got != tt.want {
				t.Fatalf("MP4HasAudioTrack = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMP4VideoDimensions(t *testing.T) {
	for _, tt := range []struct {
		name   string
		data   []byte
		wantW  float64
		wantH  float64
		wantOK bool
	}{
		{name: "ltx extend output", data: mediasnifftest.MP4WithVideoTrack(1344, 768, 339), wantW: 1344, wantH: 768, wantOK: true},
		{name: "portrait", data: mediasnifftest.MP4WithVideoTrack(720, 1280), wantW: 720, wantH: 1280, wantOK: true},
		{name: "audio only", data: mediasnifftest.MP4AudioOnly(), wantOK: false},
		{name: "no moov", data: mediasnifftest.FTYP(), wantOK: false},
		{name: "not mp4", data: []byte("not a video at all"), wantOK: false},
		{name: "empty", data: nil, wantOK: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, h, ok := MP4VideoDimensions(tt.data)
			if ok != tt.wantOK || (tt.wantOK && (w != tt.wantW || h != tt.wantH)) {
				t.Fatalf("MP4VideoDimensions = (%v, %v, %v), want (%v, %v, %v)", w, h, ok, tt.wantW, tt.wantH, tt.wantOK)
			}
		})
	}
}

func TestMP4VideoFrameCount(t *testing.T) {
	for _, tt := range []struct {
		name   string
		data   []byte
		want   float64
		wantOK bool
	}{
		{name: "single run", data: mediasnifftest.MP4WithVideoTrack(1344, 768, 339), want: 339, wantOK: true},
		{name: "runs sum", data: mediasnifftest.MP4WithVideoTrack(1344, 768, 243, 96), want: 339, wantOK: true},
		{name: "no sample table", data: mediasnifftest.MP4WithVideoTrack(1344, 768), wantOK: false},
		{name: "audio only", data: mediasnifftest.MP4AudioOnly(), wantOK: false},
		{name: "not mp4", data: []byte("not a video at all"), wantOK: false},
		{name: "empty", data: nil, wantOK: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := MP4VideoFrameCount(tt.data)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("MP4VideoFrameCount = (%v, %v), want (%v, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
