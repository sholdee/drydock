package app

import (
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/render"
)

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneResolvedSourceMap(in map[string]render.ResolvedSource) map[string]render.ResolvedSource {
	if len(in) == 0 {
		return map[string]render.ResolvedSource{}
	}
	out := make(map[string]render.ResolvedSource, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func appendUniqueString(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (p localProvider) recordCacheEvent(event cacheevent.Event) {
	if p.cacheEvents != nil {
		p.cacheEvents.Record(event)
	}
}
