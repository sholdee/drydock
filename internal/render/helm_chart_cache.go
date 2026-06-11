package render

import (
	"sync"
	"time"

	helmchart "helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/chart/loader/archive"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	chartv2loader "helm.sh/helm/v4/pkg/chart/v2/loader"
)

// HelmChartLoadCache memoizes chart file reads and tree validation for the
// lifetime of one render session. Load returns a freshly parsed chart on every
// call so Helm dependency processing cannot leak mutations between renders.
type HelmChartLoadCache struct {
	mu      sync.Mutex
	entries map[string]*helmChartCacheEntry
}

type helmChartCacheEntry struct {
	once        sync.Once
	files       []helmChartRawFile
	cacheable   bool
	validateErr error
	loadErr     error
}

type helmChartRawFile struct {
	name    string
	modTime time.Time
	data    []byte
}

func NewHelmChartLoadCache() *HelmChartLoadCache {
	return &HelmChartLoadCache{entries: map[string]*helmChartCacheEntry{}}
}

func (cache *HelmChartLoadCache) Load(chartPath string) (helmchart.Charter, error) {
	if cache == nil {
		return loadValidatedHelmChart(chartPath)
	}
	cache.mu.Lock()
	entry, ok := cache.entries[chartPath]
	if !ok {
		entry = &helmChartCacheEntry{}
		cache.entries[chartPath] = entry
	}
	cache.mu.Unlock()

	entry.once.Do(func() {
		entry.validateErr = validateHelmChartTree(chartPath)
		if entry.validateErr != nil {
			return
		}
		loaded, err := loader.Load(chartPath)
		if err != nil {
			entry.loadErr = err
			return
		}
		v2chart, ok := loaded.(*chartv2.Chart)
		if !ok {
			return
		}
		entry.files = rawFilesFromV2Chart(v2chart)
		entry.cacheable = true
	})
	if entry.validateErr != nil {
		return nil, entry.validateErr
	}
	if entry.loadErr != nil {
		return nil, entry.loadErr
	}
	if !entry.cacheable {
		return loader.Load(chartPath)
	}
	return chartv2loader.LoadFiles(bufferedFilesFromRawFiles(entry.files))
}

func loadValidatedHelmChart(chartPath string) (helmchart.Charter, error) {
	if err := validateHelmChartTree(chartPath); err != nil {
		return nil, err
	}
	return loader.Load(chartPath)
}

func rawFilesFromV2Chart(chart *chartv2.Chart) []helmChartRawFile {
	files := make([]helmChartRawFile, 0, len(chart.Raw))
	for _, file := range chart.Raw {
		files = append(files, helmChartRawFile{
			name:    file.Name,
			modTime: file.ModTime,
			data:    append([]byte(nil), file.Data...),
		})
	}
	return files
}

func bufferedFilesFromRawFiles(files []helmChartRawFile) []*archive.BufferedFile {
	out := make([]*archive.BufferedFile, 0, len(files))
	for _, file := range files {
		out = append(out, &archive.BufferedFile{
			Name:    file.name,
			ModTime: file.modTime,
			Data:    append([]byte(nil), file.data...),
		})
	}
	return out
}
