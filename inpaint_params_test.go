package main

import (
	"testing"
)

// inpaintTestSchema builds a ModelInputSchema from a name→property table so
// resolver tests can craft endpoint shapes without a provider call.
func inpaintTestSchema(props map[string]SchemaProperty) *ModelInputSchema {
	return &ModelInputSchema{Properties: props}
}

func inpaintScalar(name string, enum ...string) SchemaProperty {
	p := SchemaProperty{Name: name, Kind: schemaScalar, Type: "string"}
	p.Enum = enum
	return p
}

func TestResolveInpaintBodyWithoutSchema(t *testing.T) {
	body, notices, err := resolveInpaintBody(nil, ImageInpaintRequest{
		Model: "m", Prompt: "a lighthouse", SourceImage: "data:image/png;base64,AAA", MaskImage: "data:image/png;base64,BBB",
		Width: 64, Height: 48,
	}, Overrides{})
	if err != nil {
		t.Fatalf("nil schema must yield the literal body, got error: %v", err)
	}
	if len(notices) == 0 {
		t.Fatal("nil schema must notice the degraded mapping")
	}
	if body["prompt"] != "a lighthouse" || body["image_url"] == "" || body["mask_url"] == "" {
		t.Fatalf("literal body = %v", body)
	}
	size, ok := body["image_size"].(map[string]any)
	if !ok || size["width"] != 64 || size["height"] != 48 {
		t.Fatalf("image_size = %v", body["image_size"])
	}
}

func TestResolveInpaintBodyWithSchema(t *testing.T) {
	schema := inpaintTestSchema(map[string]SchemaProperty{
		"prompt":        inpaintScalar("prompt"),
		"image_url":     inpaintScalar("image_url"),
		"mask_url":      inpaintScalar("mask_url"),
		"output_format": inpaintScalar("output_format", "jpeg", "png"),
	})
	body, notices, err := resolveInpaintBody(schema, ImageInpaintRequest{
		Model: "fal-ai/flux-pro/v1/fill", Prompt: "a lighthouse",
		SourceImage: "data:image/png;base64,AAA", MaskImage: "data:image/png;base64,BBB",
		Width: 64, Height: 48,
	}, Overrides{})
	if err != nil {
		t.Fatalf("resolveInpaintBody: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("unexpected notices: %v", notices)
	}
	if body["prompt"] != "a lighthouse" || body["image_url"] == "" || body["mask_url"] == "" {
		t.Fatalf("body = %v", body)
	}
	if body["output_format"] != "png" {
		t.Fatalf("lossless output_format = %v, want png", body["output_format"])
	}
	if _, hasSize := body["image_size"]; hasSize {
		t.Fatal("an endpoint with no image_size input must not receive one")
	}
}

func TestResolveInpaintBodyPixelSizeEndpoint(t *testing.T) {
	schema := inpaintTestSchema(map[string]SchemaProperty{
		"prompt":     inpaintScalar("prompt"),
		"image_url":  inpaintScalar("image_url"),
		"mask_url":   inpaintScalar("mask_url"),
		"image_size": {Name: "image_size", Kind: schemaObject, Type: "object"},
	})
	body, _, err := resolveInpaintBody(schema, ImageInpaintRequest{
		Model: "fal-ai/qwen-image-edit/inpaint", Prompt: "p",
		SourceImage: "s", MaskImage: "m", Width: 640, Height: 384,
	}, Overrides{})
	if err != nil {
		t.Fatalf("resolveInpaintBody: %v", err)
	}
	size, ok := body["image_size"].(map[string]any)
	if !ok || size["width"] != 640 || size["height"] != 384 {
		t.Fatalf("image_size = %v, want the source pixels", body["image_size"])
	}
}

func TestResolveInpaintBodyEnumSizeNotices(t *testing.T) {
	schema := inpaintTestSchema(map[string]SchemaProperty{
		"prompt":     inpaintScalar("prompt"),
		"image_url":  inpaintScalar("image_url"),
		"mask_url":   inpaintScalar("mask_url"),
		"image_size": inpaintScalar("image_size", "square_hd", "square"),
	})
	body, notices, err := resolveInpaintBody(schema, ImageInpaintRequest{
		Model: "m", Prompt: "p", SourceImage: "s", MaskImage: "m2", Width: 640, Height: 384,
	}, Overrides{})
	if err != nil {
		t.Fatalf("resolveInpaintBody: %v", err)
	}
	if _, hasSize := body["image_size"]; hasSize {
		t.Fatal("a preset-enum size input must not receive pixels")
	}
	if len(notices) != 1 {
		t.Fatalf("enum size must notice that exact dimensions cannot be requested, got %v", notices)
	}
}

func TestResolveInpaintBodyRefusesMissingFields(t *testing.T) {
	cases := []struct {
		name   string
		schema *ModelInputSchema
	}{
		{"no mask input", inpaintTestSchema(map[string]SchemaProperty{
			"prompt":    inpaintScalar("prompt"),
			"image_url": inpaintScalar("image_url"),
		})},
		{"no source image input", inpaintTestSchema(map[string]SchemaProperty{
			"prompt": inpaintScalar("prompt"),
			"mask":   inpaintScalar("mask"),
		})},
		{"no prompt input", inpaintTestSchema(map[string]SchemaProperty{
			"image_url": inpaintScalar("image_url"),
			"mask_url":  inpaintScalar("mask_url"),
		})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := resolveInpaintBody(tc.schema, ImageInpaintRequest{
				Model: "m", Prompt: "p", SourceImage: "s", MaskImage: "m2",
			}, Overrides{}); err == nil {
				t.Fatal("an endpoint missing a required inpaint field must be refused before any call")
			}
		})
	}
}

func TestResolveReplicateInpaintInput(t *testing.T) {
	// Nil schema: the canonical body only.
	body, notices, err := resolveReplicateInpaintInput(nil, ImageInpaintRequest{
		Model: "m", Prompt: "p", SourceImage: "s", MaskImage: "m2",
	})
	if err != nil || len(notices) == 0 {
		t.Fatalf("nil schema: err=%v notices=%v", err, notices)
	}
	if body["prompt"] != "p" || body["image"] == "" || body["mask"] == "" {
		t.Fatalf("canonical body = %v", body)
	}

	// The dev model's schema: match_input pinned, png requested.
	schema := inpaintTestSchema(map[string]SchemaProperty{
		"prompt":        inpaintScalar("prompt"),
		"image":         inpaintScalar("image"),
		"mask":          inpaintScalar("mask"),
		"output_format": inpaintScalar("output_format", "webp", "jpg", "png"),
		"megapixels":    inpaintScalar("megapixels", "1", "0.25", "match_input"),
	})
	body, _, err = resolveReplicateInpaintInput(schema, ImageInpaintRequest{
		Model: "black-forest-labs/flux-fill-dev", Prompt: "p", SourceImage: "s", MaskImage: "m2",
	})
	if err != nil {
		t.Fatalf("resolveReplicateInpaintInput: %v", err)
	}
	if body["megapixels"] != "match_input" {
		t.Fatalf("megapixels = %v, want match_input (the ~1MP default would mismatch the source)", body["megapixels"])
	}
	if body["output_format"] != "png" {
		t.Fatalf("output_format = %v, want png", body["output_format"])
	}
}

func TestVerifiedInpaintModels(t *testing.T) {
	fal := verifiedInpaintModels("fal")
	rep := verifiedInpaintModels("replicate")
	if len(fal) == 0 || fal[0].ID != defaultFalInpaintModel {
		t.Fatalf("fal catalog = %+v", fal)
	}
	if len(rep) == 0 || rep[0].ID != defaultReplicateInpaintModel {
		t.Fatalf("replicate catalog = %+v", rep)
	}
	// Every catalog entry's model id must route through the adapters'
	// hard-refusal rules consistently: each listed id should carry a label.
	for _, list := range [][]InpaintModelOption{fal, rep} {
		for _, option := range list {
			if option.ID == "" || option.Label == "" {
				t.Fatalf("catalog entry missing id or label: %+v", option)
			}
		}
	}
}
