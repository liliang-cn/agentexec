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

// finishOutput turns whatever is left into events.
//
// A caller that streamed through ParseChunk gets only the buffered tail. A
// caller that collected the output and handed it to Finalize gets it parsed
// here — because the parameter is called fullOutput, and a parameter that is
// accepted and discarded invites exactly one mistake: pass everything, get an
// empty Result and no error, which is the least debuggable outcome available.
func finishOutput(lb *LineBuffer, fullOutput []byte, mapper func(map[string]any) []Event) []Event {
	if !lb.Fed() && len(fullOutput) > 0 {
		events := mapJSONLines(lb.Feed(fullOutput), mapper)
		return append(events, mapJSONLines(lb.Flush(), mapper)...)
	}
	return mapJSONLines(lb.Flush(), mapper)
}
