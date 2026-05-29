package setupaction

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallUsesVerifiedCachedArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	archive := buildDrydockArchive(t, tmp, "cached")
	hash := sha256File(t, archive)
	writeExecutable(t, filepath.Join(tmp, "curl"), `#!/usr/bin/env bash
echo "curl should not be called for a verified cached archive" >&2
exit 2
`)

	installDir := filepath.Join(tmp, "bin")
	outputPath := filepath.Join(tmp, "github-output")
	cmd := exec.Command("bash", "install.sh")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(),
		"PATH="+tmp+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_OUTPUT="+outputPath,
		"RUNNER_OS=Linux",
		"RUNNER_ARCH=X64",
		"DRYDOCK_ALLOW_UNVERIFIED=false",
		"DRYDOCK_CACHE_ARCHIVE="+archive,
		"DRYDOCK_EXPECTED_HASH="+hash,
		"DRYDOCK_INSTALL_DIR="+installDir,
		"DRYDOCK_RELEASE_REPOSITORY=sholdee/drydock",
		"DRYDOCK_RESOLVED_VERSION=v1.2.3",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(installDir, "drydock")); err != nil {
		t.Fatalf("installed drydock missing: %v", err)
	}
	outputs := readGitHubOutput(t, outputPath)
	if got, want := outputs["cache-hit"], "true"; got != want {
		t.Fatalf("cache-hit = %q, want %q", got, want)
	}
	if got, want := outputs["cache-save"], "false"; got != want {
		t.Fatalf("cache-save = %q, want %q", got, want)
	}
}

func TestInstallCopiesVerifiedDownloadIntoCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell action tests require bash")
	}

	tmp := t.TempDir()
	releaseArchive := buildDrydockArchive(t, tmp, "download")
	hash := sha256File(t, releaseArchive)
	cacheArchive := filepath.Join(tmp, "cache", "drydock.tar.gz")
	writeExecutable(t, filepath.Join(tmp, "curl"), `#!/usr/bin/env bash
set -euo pipefail

out=""
args="$*"
while [[ "$#" -gt 0 ]]; do
  if [[ "$1" == "--output" ]]; then
    out="$2"
    shift 2
    continue
  fi
  shift
done

if [[ "${args}" == *"drydock_linux-amd64.tar.gz"* ]]; then
  cp "${DRYDOCK_TEST_ARCHIVE}" "${out}"
  exit 0
fi
if [[ "${args}" == *"checksums.txt"* ]]; then
  printf '`+hash+`  drydock_linux-amd64.tar.gz\n' > "${out}"
  printf '200'
  exit 0
fi
echo "unexpected curl invocation: ${args}" >&2
exit 2
`)

	installDir := filepath.Join(tmp, "bin")
	outputPath := filepath.Join(tmp, "github-output")
	cmd := exec.Command("bash", "install.sh")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(),
		"PATH="+tmp+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_OUTPUT="+outputPath,
		"RUNNER_OS=Linux",
		"RUNNER_ARCH=X64",
		"DRYDOCK_ALLOW_UNVERIFIED=false",
		"DRYDOCK_CACHE_ARCHIVE="+cacheArchive,
		"DRYDOCK_INSTALL_DIR="+installDir,
		"DRYDOCK_RELEASE_REPOSITORY=sholdee/drydock",
		"DRYDOCK_RESOLVED_VERSION=v1.2.3",
		"DRYDOCK_TEST_ARCHIVE="+releaseArchive,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	if got := sha256File(t, cacheArchive); got != hash {
		t.Fatalf("cached archive hash = %q, want %q", got, hash)
	}
	outputs := readGitHubOutput(t, outputPath)
	if got, want := outputs["cache-hit"], "false"; got != want {
		t.Fatalf("cache-hit = %q, want %q", got, want)
	}
	if got, want := outputs["cache-save"], "true"; got != want {
		t.Fatalf("cache-save = %q, want %q", got, want)
	}
}

func buildDrydockArchive(t *testing.T, tmp, label string) string {
	t.Helper()

	src := filepath.Join(tmp, "src-"+label)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir archive src: %v", err)
	}
	writeExecutable(t, filepath.Join(src, "drydock"), "#!/usr/bin/env bash\necho drydock "+label+"\n")

	archive := filepath.Join(tmp, "drydock-"+label+".tar.gz")
	cmd := exec.Command("tar", "-czf", archive, "-C", src, "drydock")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build archive: %v\n%s", err, out)
	}
	return archive
}

func sha256File(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
