package app

func mergeRefRoots(base, extra map[string]string) map[string]string {
	if len(base) == 0 && len(extra) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(base)+len(extra))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}
