package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sholdee/drydock/internal/paritycompare"
)

func main() {
	var options paritycompare.Options
	flag.StringVar(&options.ArgoCDDir, "argocd-dir", "", "directory containing Argo CD generated manifest YAML files")
	flag.StringVar(&options.DrydockDir, "drydock-dir", "", "directory containing drydock generated manifest YAML files")
	flag.StringVar(&options.OutDir, "out-dir", "", "directory for canonical manifests and diffs")
	flag.StringVar(&options.IgnoreFile, "ignore-file", "", "optional YAML file with jsonPointers to ignore")
	flag.Parse()

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
