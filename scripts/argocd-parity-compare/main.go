package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sholdee/drydock/internal/paritycompare"
)

func main() {
	var options paritycompare.Options
	var appOptions paritycompare.ApplicationOptions
	flag.StringVar(&options.ArgoCDDir, "argocd-dir", "", "directory containing Argo CD generated manifest YAML files")
	flag.StringVar(&options.DrydockDir, "drydock-dir", "", "directory containing drydock generated manifest YAML files")
	flag.StringVar(&options.OutDir, "out-dir", "", "directory for canonical manifests and diffs")
	flag.StringVar(&options.IgnoreFile, "ignore-file", "", "optional YAML file with jsonPointers to ignore")
	flag.StringVar(&appOptions.ArgoCDFile, "argocd-applications", "", "kubectl get applications -o json output; compares Application inventories instead of manifests")
	flag.StringVar(&appOptions.DrydockFile, "drydock-applications", "", "drydock get apps -o json output; compares Application inventories instead of manifests")
	flag.Parse()

	if appOptions.ArgoCDFile != "" || appOptions.DrydockFile != "" {
		appOptions.OutDir = options.OutDir
		os.Exit(compareApplications(appOptions))
	}

	result, err := paritycompare.Compare(options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argocd parity compare: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("Applications: %d\n", result.Applications)
	fmt.Printf("Resources: %d\n", result.Resources)
	fmt.Printf("Differences: %d\n", result.Differences)
	for _, diffFile := range result.DiffFiles {
		fmt.Printf("Diff: %s\n", diffFile)
	}
	if result.Differences > 0 {
		os.Exit(1)
	}
}

func compareApplications(options paritycompare.ApplicationOptions) int {
	result, err := paritycompare.CompareApplications(options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argocd parity compare: %v\n", err)
		return 2
	}
	fmt.Printf("Applications: %d\n", result.Applications)
	for _, key := range result.Missing {
		fmt.Printf("Only in Argo CD: %s\n", key)
	}
	for _, key := range result.Extra {
		fmt.Printf("Only in drydock: %s\n", key)
	}
	for _, key := range result.SpecDifferences {
		fmt.Printf("Spec differs: %s\n", key)
	}
	if result.DiffFile != "" {
		fmt.Printf("Diff: %s\n", result.DiffFile)
	}
	if result.Failed() {
		return 1
	}
	return 0
}
