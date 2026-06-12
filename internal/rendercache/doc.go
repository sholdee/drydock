// Package rendercache implements the persistent on-disk render-output cache
// store: content-addressed gzipped JSON envelopes with atomic writes and
// LRU-by-mtime size-cap eviction. The package stores opaque payload bytes and
// has no knowledge of internal/app types; the app layer marshals its own
// payload, so this package can be imported by internal/app without an import
// cycle.
package rendercache
