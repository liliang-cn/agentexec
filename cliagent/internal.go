package cliagent

import (
	"maps"
	"sort"
)

// mergeEnv combines the provider base env and the request env into a sorted
// "KEY=VALUE" slice. Empty base values are dropped; Request.Env overrides base.
func mergeEnv(base, reqEnv map[string]string) []string {
	merged := make(map[string]string, len(base)+len(reqEnv))
	for k, v := range base {
		if v != "" {
			merged[k] = v
		}
	}
	maps.Copy(merged, reqEnv)
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// JSON map accessors — tolerant of missing/mistyped fields.

func mapString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func mapObject(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

func mapArray(m map[string]any, key string) []any {
	if v, ok := m[key].([]any); ok {
		return v
	}
	return nil
}

// mapInt64 coerces a JSON number (float64) or integer to int64.
func mapInt64(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

func mapFloat64(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}
