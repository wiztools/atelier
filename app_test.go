package main

import "testing"

func TestNormalizeBaseURL(t *testing.T) {
	got, err := normalizeBaseURL("localhost:11434/")
	if err != nil {
		t.Fatalf("normalizeBaseURL returned error: %v", err)
	}
	if got != "http://localhost:11434" {
		t.Fatalf("normalizeBaseURL = %q, want %q", got, "http://localhost:11434")
	}
}

func TestNormalizeImagePayload(t *testing.T) {
	payload := normalizeImagePayload("iVBORw0KGgo=")
	if payload != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("normalizeImagePayload = %q", payload)
	}

	dataURL := "data:image/png;base64,iVBORw0KGgo="
	if normalizeImagePayload(dataURL) != dataURL {
		t.Fatal("data URL should pass through unchanged")
	}
}

func TestCollectImagesFromSingularImageField(t *testing.T) {
	raw := []byte(`{"model":"x/z-image-turbo:latest","response":"","done_reason":"stop","image":"iVBORw0KGgo="}`)
	images := collectImagesFromJSON(raw)
	if len(images) != 1 {
		t.Fatalf("collectImagesFromJSON returned %d images, want 1", len(images))
	}
	if images[0] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("image = %q", images[0])
	}
}

func TestCompactRawResponseRedactsImageData(t *testing.T) {
	raw := []byte(`{"image":"iVBORw0KGgo="}`)
	compact := compactRawResponse(raw)
	if compact == `{"image":"iVBORw0KGgo="}` {
		t.Fatal("compact raw response should redact image data")
	}
}

func TestDecodeImagePayload(t *testing.T) {
	data, extension, err := decodeImagePayload("data:image/png;base64,iVBORw0KGgo=")
	if err != nil {
		t.Fatalf("decodeImagePayload returned error: %v", err)
	}
	if len(data) != 8 {
		t.Fatalf("decoded data length = %d, want 8", len(data))
	}
	if extension != ".png" {
		t.Fatalf("extension = %q, want .png", extension)
	}
}

func TestNormalizeImagePayloadRejectsNonImageBase64(t *testing.T) {
	if normalizeImagePayload("stop") != "" {
		t.Fatal("non-image base64 should not be treated as a renderable image")
	}
}

func TestSanitizeFilename(t *testing.T) {
	got := sanitizeFilename(`bad/name:image?.png`)
	if got != "bad-name-image-.png" {
		t.Fatalf("sanitizeFilename = %q", got)
	}
}
