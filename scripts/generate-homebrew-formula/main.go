package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	formulaClass = "Drydock"
	releaseBase  = "https://github.com/sholdee/drydock/releases/download/v#{version}"
)

var requiredArchives = []string{
	"drydock_darwin-amd64.tar.gz",
	"drydock_darwin-arm64.tar.gz",
	"drydock_linux-amd64.tar.gz",
	"drydock_linux-arm64.tar.gz",
}

var (
	semverTagPattern = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	sha256Pattern    = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

type options struct {
	version   string
	checksums string
	output    string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "generate Homebrew formula: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	opts, err := parseFlags(args)
	if err != nil {
		return err
	}

	checksums, err := readChecksums(opts.checksums)
	if err != nil {
		return err
	}
	formula, err := generateFormula(opts.version, checksums)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(opts.output), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(opts.output, []byte(formula), 0o644); err != nil {
		return fmt.Errorf("write formula: %w", err)
	}
	return nil
}

func parseFlags(args []string) (options, error) {
	var opts options
	flags := flag.NewFlagSet("generate-homebrew-formula", flag.ContinueOnError)
	flags.StringVar(&opts.version, "version", "", "release tag such as v0.1.9")
	flags.StringVar(&opts.checksums, "checksums", "", "path to checksums.txt")
	flags.StringVar(&opts.output, "output", "", "path to Formula/drydock.rb")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(opts.version) == "" {
		return options{}, fmt.Errorf("--version is required")
	}
	if strings.TrimSpace(opts.checksums) == "" {
		return options{}, fmt.Errorf("--checksums is required")
	}
	if strings.TrimSpace(opts.output) == "" {
		return options{}, fmt.Errorf("--output is required")
	}
	return opts, nil
}

func readChecksums(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}
	defer file.Close()

	checksums := map[string]string{}
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("parse checksums line %d: expected checksum and archive name", lineNumber)
		}
		archive := strings.TrimPrefix(fields[1], "*")
		checksum := fields[0]
		if !sha256Pattern.MatchString(checksum) {
			return nil, fmt.Errorf("parse checksums line %d for %s: checksum must be 64 hex characters", lineNumber, archive)
		}
		checksums[archive] = strings.ToLower(checksum)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}
	return checksums, nil
}

func generateFormula(tag string, checksums map[string]string) (string, error) {
	version, err := homebrewVersion(tag)
	if err != nil {
		return "", err
	}

	for _, archive := range requiredArchives {
		if strings.TrimSpace(checksums[archive]) == "" {
			return "", fmt.Errorf("missing checksum for %s", archive)
		}
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "class %s < Formula\n", formulaClass)
	builder.WriteString("  desc \"Inspect your Argo CD fleet without getting wet\"\n")
	builder.WriteString("  homepage \"https://github.com/sholdee/drydock\"\n")
	fmt.Fprintf(&builder, "  version \"%s\"\n", version)
	builder.WriteString("  license \"Apache-2.0\"\n\n")

	builder.WriteString("  on_macos do\n")
	writeArchiveBlock(&builder, "on_intel", "drydock_darwin-amd64.tar.gz", checksums)
	builder.WriteByte('\n')
	writeArchiveBlock(&builder, "on_arm", "drydock_darwin-arm64.tar.gz", checksums)
	builder.WriteString("  end\n\n")

	builder.WriteString("  on_linux do\n")
	writeArchiveBlock(&builder, "on_intel", "drydock_linux-amd64.tar.gz", checksums)
	builder.WriteByte('\n')
	writeArchiveBlock(&builder, "on_arm", "drydock_linux-arm64.tar.gz", checksums)
	builder.WriteString("  end\n\n")

	builder.WriteString("  def install\n")
	builder.WriteString("    bin.install \"drydock\"\n")
	builder.WriteString("    generate_completions_from_executable bin/\"drydock\", \"completion\"\n")
	builder.WriteString("  end\n\n")

	builder.WriteString("  test do\n")
	builder.WriteString("    assert_match \"version: v#{version}\", shell_output(\"#{bin}/drydock version\")\n")
	builder.WriteString("    assert_match \"completion\", shell_output(\"#{bin}/drydock completion --help\")\n")
	builder.WriteString("  end\n")
	builder.WriteString("end\n")

	return builder.String(), nil
}

func homebrewVersion(tag string) (string, error) {
	tag = strings.TrimSpace(tag)
	if !semverTagPattern.MatchString(tag) {
		return "", fmt.Errorf("--version must be a SemVer tag like vX.Y.Z")
	}
	return strings.TrimPrefix(tag, "v"), nil
}

func writeArchiveBlock(builder *strings.Builder, condition, archive string, checksums map[string]string) {
	fmt.Fprintf(builder, "    %s do\n", condition)
	fmt.Fprintf(builder, "      url \"%s/%s\"\n", releaseBase, archive)
	fmt.Fprintf(builder, "      sha256 \"%s\"\n", checksums[archive])
	builder.WriteString("    end\n")
}
