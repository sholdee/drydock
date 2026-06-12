package app

import (
	"maps"

	"slices"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/render"
)

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func cloneResolvedSourceMap(in map[string]render.ResolvedSource) map[string]render.ResolvedSource {
	if len(in) == 0 {
		return map[string]render.ResolvedSource{}
	}
	out := make(map[string]render.ResolvedSource, len(in))
	maps.Copy(out, in)
	return out
}

func appendUniqueString(values []string, value string) []string {
	if value == "" {
		return values
	}
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func (p localProvider) recordCacheEvent(event cacheevent.Event) {
	if p.cacheEvents != nil {
		p.cacheEvents.Record(event)
	}
}

func (p localProvider) recordAcquisition(record cacheevent.AcquisitionRecord) {
	if p.acquisitions != nil {
		p.acquisitions.Record(record)
	}
}
