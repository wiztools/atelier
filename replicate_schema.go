package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// newReplicateSchemaCache builds a SchemaCache over Replicate's per-model
// input schemas (latest_version.openapi_schema on GET /v1/models/{owner}/{name}).
// The cache machinery is shared with the fal path because both providers
// publish the same OpenAPI dialect: components.schemas.Input with typed,
// enum-constrained properties, which parseModelInputSchema consumes
// unchanged. The disk directory is namespaced under schema-cache/replicate so
// a colliding sanitized filename (an owner/name and a fal endpoint id that
// sanitize identically) can never cross-contaminate the two providers.
func newReplicateSchemaCache(httpClient *http.Client, storageRoot string) *SchemaCache {
	return &SchemaCache{
		dir: filepath.Join(storageRoot, "schema-cache", "replicate"),
		ttl: 7 * 24 * time.Hour,
		now: time.Now,
		fetch: func(ctx context.Context, model string) ([]byte, error) {
			apiKey, err := loadReplicateAPIKey()
			if err != nil {
				return nil, err
			}
			return newReplicateClient(httpClient, apiKey).GetModelInputSchema(ctx, model)
		},
	}
}

// replicateVideoDurationOptions returns the duration values the given
// Replicate video model accepts, drawn from its input schema's duration
// property — the replicate sibling of videoDurationOptions (fal_schema.go),
// reading through the replicate synonym table so the lookup matches the one
// resolveReplicateVideoInput performs at submit time. Models with a free
// numeric duration (no enum) return nil; callers fall back to a generic
// option set.
func replicateVideoDurationOptions(ctx context.Context, client *http.Client, storageRoot, model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	cache := newReplicateSchemaCache(client, storageRoot)
	schema := cache.Get(ctx, model)
	if _, prop, ok := findNative(schema, Overrides{}, "replicate-video", model, "duration"); ok {
		return prop.Enum
	}
	return nil
}
