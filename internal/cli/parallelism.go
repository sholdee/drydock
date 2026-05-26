package cli

import "runtime"

const maxDefaultRenderAppsParallelism = 8

func defaultRenderAppsParallelism() int {
	value := runtime.GOMAXPROCS(0)
	if value < 1 {
		return 1
	}
	if value > maxDefaultRenderAppsParallelism {
		return maxDefaultRenderAppsParallelism
	}
	return value
}
