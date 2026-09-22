// Command argocd-parity-chart-push packages a chart directory and pushes it
// to a nested OCI repository with helm's own registry client, so the Argo CD
// render parity smoke exercises a real `chart: org/nested/name` source.
//
// It exists because `oras push` cannot produce the helm media types (config
// application/vnd.cncf.helm.config.v1+json plus a single
// application/vnd.cncf.helm.chart.content.v1.tar+gzip layer) that Argo CD's
// repo-server and drydock both require, and the smoke must not depend on a
// `helm` binary being installed.
//
// The fixture registry is self-signed, so --ca-file is required; the helper
// never falls back to plain HTTP.
package main
