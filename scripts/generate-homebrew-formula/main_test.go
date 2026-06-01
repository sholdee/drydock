package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHomebrewFormulaGeneration(t *testing.T) {
	checksums := writeChecksums(t, map[string]string{
		"drydock_darwin-amd64.tar.gz": "1111111111111111111111111111111111111111111111111111111111111111",
		"drydock_darwin-arm64.tar.gz": "2222222222222222222222222222222222222222222222222222222222222222",
		"drydock_linux-amd64.tar.gz":  "3333333333333333333333333333333333333333333333333333333333333333",
		"drydock_linux-arm64.tar.gz":  "4444444444444444444444444444444444444444444444444444444444444444",
	})
	output := filepath.Join(t.TempDir(), "drydock.rb")

	if err := run([]string{"--version", "v0.1.9", "--checksums", checksums, "--output", output}); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	gotBytes, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read generated formula: %v", err)
	}
	got := string(gotBytes)

	want := `class Drydock < Formula
  desc "Inspect your Argo CD fleet without getting wet"
  homepage "https://github.com/sholdee/drydock"
  version "0.1.9"
  license "Apache-2.0"

  on_macos do
    on_intel do
      url "https://github.com/sholdee/drydock/releases/download/v#{version}/drydock_darwin-amd64.tar.gz"
      sha256 "1111111111111111111111111111111111111111111111111111111111111111"
    end

    on_arm do
      url "https://github.com/sholdee/drydock/releases/download/v#{version}/drydock_darwin-arm64.tar.gz"
      sha256 "2222222222222222222222222222222222222222222222222222222222222222"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/sholdee/drydock/releases/download/v#{version}/drydock_linux-amd64.tar.gz"
      sha256 "3333333333333333333333333333333333333333333333333333333333333333"
    end

    on_arm do
      url "https://github.com/sholdee/drydock/releases/download/v#{version}/drydock_linux-arm64.tar.gz"
      sha256 "4444444444444444444444444444444444444444444444444444444444444444"
    end
  end

  def install
    bin.install "drydock"
    generate_completions_from_executable bin/"drydock", "completion"
  end

  test do
    assert_match "version: v#{version}", shell_output("#{bin}/drydock version")
    assert_match "completion", shell_output("#{bin}/drydock completion --help")
  end
end
`
	if got != want {
		t.Fatalf("generated formula mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
	assertContains(t, got, "class Drydock < Formula")
	assertContains(t, got, `desc "Inspect your Argo CD fleet without getting wet"`)
	assertContains(t, got, `homepage "https://github.com/sholdee/drydock"`)
	assertContains(t, got, `license "Apache-2.0"`)
	assertContains(t, got, `version "0.1.9"`)
	assertContains(t, got, `url "https://github.com/sholdee/drydock/releases/download/v#{version}/drydock_darwin-amd64.tar.gz"`)
	assertContains(t, got, `url "https://github.com/sholdee/drydock/releases/download/v#{version}/drydock_darwin-arm64.tar.gz"`)
	assertContains(t, got, `url "https://github.com/sholdee/drydock/releases/download/v#{version}/drydock_linux-amd64.tar.gz"`)
	assertContains(t, got, `url "https://github.com/sholdee/drydock/releases/download/v#{version}/drydock_linux-arm64.tar.gz"`)
	assertContains(t, got, `sha256 "1111111111111111111111111111111111111111111111111111111111111111"`)
	assertContains(t, got, `sha256 "2222222222222222222222222222222222222222222222222222222222222222"`)
	assertContains(t, got, `sha256 "3333333333333333333333333333333333333333333333333333333333333333"`)
	assertContains(t, got, `sha256 "4444444444444444444444444444444444444444444444444444444444444444"`)
	assertContains(t, got, `on_macos do`)
	assertContains(t, got, `on_linux do`)
	assertContains(t, got, `on_intel do`)
	assertContains(t, got, `on_arm do`)
	assertContains(t, got, `bin.install "drydock"`)
	assertContains(t, got, `generate_completions_from_executable bin/"drydock", "completion"`)
	assertContains(t, got, `test do`)
	assertContains(t, got, `assert_match "version: v#{version}", shell_output("#{bin}/drydock version")`)
	assertContains(t, got, `assert_match "completion", shell_output("#{bin}/drydock completion --help")`)
}

func TestHomebrewFormulaMissingChecksum(t *testing.T) {
	checksums := writeChecksums(t, map[string]string{
		"drydock_darwin-amd64.tar.gz": "1111111111111111111111111111111111111111111111111111111111111111",
		"drydock_darwin-arm64.tar.gz": "2222222222222222222222222222222222222222222222222222222222222222",
		"drydock_linux-amd64.tar.gz":  "3333333333333333333333333333333333333333333333333333333333333333",
	})
	output := filepath.Join(t.TempDir(), "drydock.rb")

	err := run([]string{"--version", "v0.1.9", "--checksums", checksums, "--output", output})
	if err == nil {
		t.Fatal("run() error = nil, want missing checksum error")
	}
	if !strings.Contains(err.Error(), "missing checksum for drydock_linux-arm64.tar.gz") {
		t.Fatalf("run() error = %q, want missing archive name", err)
	}
}

func TestHomebrewFormulaParsesChecksumVariants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checksums.txt")
	body := strings.Join([]string{
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA  *drydock_darwin-amd64.tar.gz",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  drydock_darwin-arm64.tar.gz",
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc  drydock_linux-amd64.tar.gz",
		"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd  drydock_linux-arm64.tar.gz",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	checksums, err := readChecksums(path)
	if err != nil {
		t.Fatalf("readChecksums() error = %v", err)
	}
	if got := checksums["drydock_darwin-amd64.tar.gz"]; got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("darwin amd64 checksum = %q, want lowercase normalized checksum", got)
	}
}

func TestHomebrewFormulaRejectsInvalidChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checksums.txt")
	if err := os.WriteFile(path, []byte("deadbeef  drydock_linux-amd64.tar.gz\n"), 0o600); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	_, err := readChecksums(path)
	if err == nil {
		t.Fatal("readChecksums() error = nil, want invalid checksum error")
	}
	if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "drydock_linux-amd64.tar.gz") || !strings.Contains(err.Error(), "64 hex") {
		t.Fatalf("readChecksums() error = %q, want line, archive, and checksum shape", err)
	}
}

func TestHomebrewFormulaRejectsMalformedChecksumLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checksums.txt")
	if err := os.WriteFile(path, []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), 0o600); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	_, err := readChecksums(path)
	if err == nil {
		t.Fatal("readChecksums() error = nil, want malformed line error")
	}
	if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "expected checksum and archive name") {
		t.Fatalf("readChecksums() error = %q, want malformed line context", err)
	}
}

func TestHomebrewFormulaRejectsInvalidVersion(t *testing.T) {
	checksums := map[string]string{
		"drydock_darwin-amd64.tar.gz": "1111111111111111111111111111111111111111111111111111111111111111",
		"drydock_darwin-arm64.tar.gz": "2222222222222222222222222222222222222222222222222222222222222222",
		"drydock_linux-amd64.tar.gz":  "3333333333333333333333333333333333333333333333333333333333333333",
		"drydock_linux-arm64.tar.gz":  "4444444444444444444444444444444444444444444444444444444444444444",
	}

	for _, version := range []string{"0.1.9", "v", "v0.1", "v0.1.9\"; system \"bad\""} {
		t.Run(version, func(t *testing.T) {
			_, err := generateFormula(version, checksums)
			if err == nil {
				t.Fatal("generateFormula() error = nil, want invalid version error")
			}
			if !strings.Contains(err.Error(), "SemVer tag") {
				t.Fatalf("generateFormula() error = %q, want SemVer tag error", err)
			}
		})
	}
}

func writeChecksums(t *testing.T, checksums map[string]string) string {
	t.Helper()

	var body strings.Builder
	for _, archive := range requiredArchives {
		sum, ok := checksums[archive]
		if !ok {
			continue
		}
		body.WriteString(sum)
		body.WriteString("  ")
		body.WriteString(archive)
		body.WriteByte('\n')
	}

	path := filepath.Join(t.TempDir(), "checksums.txt")
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		t.Fatalf("write checksums: %v", err)
	}
	return path
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("generated formula missing %q:\n%s", want, got)
	}
}
