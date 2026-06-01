package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type installerFixture struct {
	dir     string
	fakeBin string
}

func TestInstallDrydockInstallsRequestedTargetAndDefaultCompletions(t *testing.T) {
	fixture := newInstallerFixture(t)
	target := filepath.Join(t.TempDir(), "bin", "drydock")
	home := t.TempDir()
	smokeLog := filepath.Join(t.TempDir(), "smoke.log")

	result := runInstallDrydock(t, fixture, []string{
		"--version", "v1.2.3",
		"--target", target,
		"--yes",
	}, map[string]string{
		"HOME":             home,
		"DRYDOCK_FAKE_LOG": smokeLog,
	})

	if result.err != nil {
		t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	assertFileContains(t, target, "version: v1.2.3")
	assertFileContains(t, filepath.Join(home, ".local/share/bash-completion/completions/drydock"), "complete -F _drydock drydock")
	assertFileContains(t, filepath.Join(home, ".zfunc/_drydock"), "#compdef drydock")
	assertFileContains(t, filepath.Join(home, ".config/fish/completions/drydock.fish"), "complete -c drydock")
	for _, want := range []string{
		"Release:      v1.2.3",
		"Target:       " + target,
		"Archive:      drydock_darwin-arm64.tar.gz",
		"Checksum:     verified",
		"Cosign:       skipped; bundle unavailable",
		"Completions:  planned",
		"zsh hint:",
		"Completed successfully.",
	} {
		if !strings.Contains(result.stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, result.stdout)
		}
	}
	assertFileContains(t, smokeLog, "version")
	if got := countLinesContaining(t, smokeLog, "version"); got < 2 {
		t.Fatalf("version smoke checks = %d, want at least downloaded and installed checks", got)
	}
}

func TestInstallDrydockChecksumFailureExitsNonZero(t *testing.T) {
	fixture := newInstallerFixture(t)
	checksums := filepath.Join(fixture.dir, "checksums.txt")
	if err := os.WriteFile(checksums, []byte("deadbeef  drydock_darwin-arm64.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := runInstallDrydock(t, fixture, []string{
		"--target", filepath.Join(t.TempDir(), "drydock"),
		"--yes",
		"--no-completions",
	}, nil)

	if result.err == nil {
		t.Fatalf("installer succeeded, want checksum failure\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	if !strings.Contains(result.stderr, "Checksum verification failed") {
		t.Fatalf("stderr missing checksum failure:\n%s", result.stderr)
	}
}

func TestInstallDrydockMapsPlatformArchives(t *testing.T) {
	fixture := newInstallerFixture(t)

	tests := []struct {
		name string
		os   string
		arch string
		want string
	}{
		{name: "linux amd64", os: "Linux", arch: "x86_64", want: "drydock_linux-amd64.tar.gz"},
		{name: "linux arm64", os: "Linux", arch: "aarch64", want: "drydock_linux-arm64.tar.gz"},
		{name: "darwin amd64", os: "Darwin", arch: "amd64", want: "drydock_darwin-amd64.tar.gz"},
		{name: "darwin arm64", os: "Darwin", arch: "arm64", want: "drydock_darwin-arm64.tar.gz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "drydock")
			result := runInstallDrydock(t, fixture, []string{"--dry-run", "--target", target, "--yes", "--no-completions"}, map[string]string{
				"DRYDOCK_INSTALL_TEST_OS":   tt.os,
				"DRYDOCK_INSTALL_TEST_ARCH": tt.arch,
			})

			if result.err != nil {
				t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
			}
			if !strings.Contains(result.stdout, "Archive:      "+tt.want) {
				t.Fatalf("stdout missing archive %s:\n%s", tt.want, result.stdout)
			}
		})
	}
}

func TestInstallDrydockVerifiesArchiveSigstoreBundleWhenPresent(t *testing.T) {
	fixture := newInstallerFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.dir, "drydock_darwin-arm64.tar.gz.sigstore.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cosignDir := t.TempDir()
	cosignLog := filepath.Join(t.TempDir(), "cosign.log")
	writeExecutable(t, filepath.Join(cosignDir, "cosign"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >"${COSIGN_LOG}"
`)

	result := runInstallDrydockWithPath(t, fixture, []string{
		"--dry-run",
		"--target", filepath.Join(t.TempDir(), "drydock"),
		"--require-cosign",
		"--yes",
		"--no-completions",
	}, map[string]string{
		"COSIGN_LOG": cosignLog,
	}, cosignDir)

	if result.err != nil {
		t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	if !strings.Contains(result.stdout, "Cosign:       verified") {
		t.Fatalf("stdout missing verified cosign status:\n%s", result.stdout)
	}
	assertFileContains(t, cosignLog, "--certificate-oidc-issuer")
	assertFileContains(t, cosignLog, "https://token.actions.githubusercontent.com")
	assertFileContains(t, cosignLog, "--certificate-identity")
	assertFileContains(t, cosignLog, "https://github.com/sholdee/drydock/.github/workflows/release.yml@refs/heads/main")
}

func TestInstallDrydockRequiresCosignBundleWhenRequested(t *testing.T) {
	fixture := newInstallerFixture(t)
	result := runInstallDrydock(t, fixture, []string{
		"--dry-run",
		"--target", filepath.Join(t.TempDir(), "drydock"),
		"--require-cosign",
		"--yes",
		"--no-completions",
	}, nil)

	if result.err == nil {
		t.Fatalf("installer succeeded, want missing Sigstore bundle failure\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	if !strings.Contains(result.stderr, "sigstore.json was not found") {
		t.Fatalf("stderr missing missing bundle error:\n%s", result.stderr)
	}
}

func TestInstallDrydockRequiresCosignBinaryWhenRequested(t *testing.T) {
	fixture := newInstallerFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.dir, "drydock_darwin-arm64.tar.gz.sigstore.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := runInstallDrydock(t, fixture, []string{
		"--dry-run",
		"--target", filepath.Join(t.TempDir(), "drydock"),
		"--require-cosign",
		"--yes",
		"--no-completions",
	}, nil)

	if result.err == nil {
		t.Fatalf("installer succeeded, want missing cosign failure\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	if !strings.Contains(result.stderr, "cosign is not installed") {
		t.Fatalf("stderr missing missing cosign error:\n%s", result.stderr)
	}
}

func TestInstallDrydockFailsOnCosignVerificationFailure(t *testing.T) {
	fixture := newInstallerFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.dir, "drydock_darwin-arm64.tar.gz.sigstore.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cosignDir := t.TempDir()
	writeExecutable(t, filepath.Join(cosignDir, "cosign"), "#!/usr/bin/env bash\nexit 42\n")

	result := runInstallDrydockWithPath(t, fixture, []string{
		"--dry-run",
		"--target", filepath.Join(t.TempDir(), "drydock"),
		"--yes",
		"--no-completions",
	}, nil, cosignDir)

	if result.err == nil {
		t.Fatalf("installer succeeded, want cosign verification failure\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	if strings.Contains(result.stdout, "Dry run complete") {
		t.Fatalf("stdout shows dry-run success after cosign failure:\n%s", result.stdout)
	}
}

func TestInstallDrydockDefaultTargetResolution(t *testing.T) {
	fixture := newInstallerFixture(t)

	t.Run("updates unmanaged regular file on path", func(t *testing.T) {
		pathDir := t.TempDir()
		existing := filepath.Join(pathDir, "drydock")
		writeExecutable(t, existing, "#!/usr/bin/env bash\necho old\n")
		defaultTarget := filepath.Join(t.TempDir(), "default", "drydock")

		result := runInstallDrydockWithPath(t, fixture, []string{"--dry-run", "--yes"}, map[string]string{
			"DRYDOCK_INSTALL_TEST_DEFAULT_TARGET": defaultTarget,
		}, pathDir)

		if result.err != nil {
			t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
		}
		if !strings.Contains(result.stdout, "Target:       "+existing) {
			t.Fatalf("stdout target = want unmanaged PATH target %s:\n%s", existing, result.stdout)
		}
	})

	tests := []struct {
		name    string
		pathDir string
		setup   func(t *testing.T, pathDir string)
	}{
		{
			name:    "symlink",
			pathDir: filepath.Join(t.TempDir(), "bin"),
			setup: func(t *testing.T, pathDir string) {
				if err := os.MkdirAll(pathDir, 0o755); err != nil {
					t.Fatal(err)
				}
				real := filepath.Join(t.TempDir(), "drydock-real")
				writeExecutable(t, real, "#!/usr/bin/env bash\necho real\n")
				if err := os.Symlink(real, filepath.Join(pathDir, "drydock")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "mise shim path",
			pathDir: filepath.Join(t.TempDir(), ".mise", "shims"),
			setup: func(t *testing.T, pathDir string) {
				writeExecutable(t, filepath.Join(pathDir, "drydock"), "#!/usr/bin/env bash\n# mise shim\n")
			},
		},
		{
			name:    "homebrew prefix path",
			pathDir: filepath.Join(t.TempDir(), "opt", "homebrew", "bin"),
			setup: func(t *testing.T, pathDir string) {
				writeExecutable(t, filepath.Join(pathDir, "drydock"), "#!/usr/bin/env bash\necho brew\n")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t, tt.pathDir)
			defaultTarget := filepath.Join(t.TempDir(), "default", "drydock")
			result := runInstallDrydockWithPath(t, fixture, []string{"--dry-run", "--yes"}, map[string]string{
				"DRYDOCK_INSTALL_TEST_DEFAULT_TARGET": defaultTarget,
			}, tt.pathDir)

			if result.err != nil {
				t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
			}
			if !strings.Contains(result.stdout, "Target:       "+defaultTarget) {
				t.Fatalf("stdout target = want default target %s:\n%s", defaultTarget, result.stdout)
			}
		})
	}
}

func TestInstallDrydockRejectsRelativeTarget(t *testing.T) {
	fixture := newInstallerFixture(t)

	result := runInstallDrydock(t, fixture, []string{"--target", "bin/drydock", "--yes"}, nil)

	if result.err == nil {
		t.Fatalf("installer succeeded, want relative target rejection\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	if !strings.Contains(result.stderr, "Target path must be absolute") {
		t.Fatalf("stderr missing relative target error:\n%s", result.stderr)
	}
}

func TestInstallDrydockRejectsDirectoryTargetBeforeInstalling(t *testing.T) {
	fixture := newInstallerFixture(t)
	targetDir := t.TempDir()

	result := runInstallDrydock(t, fixture, []string{"--target", targetDir, "--yes"}, nil)

	if result.err == nil {
		t.Fatalf("installer succeeded, want directory target rejection\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	if !strings.Contains(result.stderr, "Target path must be a file path") {
		t.Fatalf("stderr missing directory target error:\n%s", result.stderr)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "drydock")); !os.IsNotExist(err) {
		t.Fatalf("installer wrote inside directory target: stat err = %v", err)
	}
}

func TestInstallDrydockSudoUnavailableFailsClearly(t *testing.T) {
	fixture := newInstallerFixture(t)
	target := filepath.Join(t.TempDir(), "protected", "drydock")

	result := runInstallDrydock(t, fixture, []string{"--target", target, "--yes", "--no-completions"}, map[string]string{
		"DRYDOCK_INSTALL_TEST_FORCE_SUDO": "1",
		"DRYDOCK_INSTALL_TEST_NO_SUDO":    "1",
	})

	if result.err == nil {
		t.Fatalf("installer succeeded, want sudo failure\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	if !strings.Contains(result.stderr, "elevated privileges are required") || !strings.Contains(result.stderr, "sudo is not available") {
		t.Fatalf("stderr missing sudo unavailable error:\n%s", result.stderr)
	}
}

func TestInstallDrydockExistingTargetUnwritableParentRequiresSudo(t *testing.T) {
	fixture := newInstallerFixture(t)
	parent := t.TempDir()
	target := filepath.Join(parent, "drydock")
	writeExecutable(t, target, "#!/usr/bin/env bash\necho old\n")
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(parent, 0o755)
	})

	result := runInstallDrydock(t, fixture, []string{"--target", target, "--yes", "--no-completions"}, map[string]string{
		"DRYDOCK_INSTALL_TEST_NO_SUDO": "1",
	})

	if result.err == nil {
		t.Fatalf("installer succeeded, want sudo failure for unwritable parent\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	if !strings.Contains(result.stderr, "elevated privileges are required") {
		t.Fatalf("stderr missing sudo required error:\n%s", result.stderr)
	}
}

func TestInstallDrydockUsesSudoWhenRequired(t *testing.T) {
	fixture := newInstallerFixture(t)
	target := filepath.Join(t.TempDir(), "protected", "drydock")
	sudoDir := t.TempDir()
	sudoLog := filepath.Join(t.TempDir(), "sudo.log")
	writeExecutable(t, filepath.Join(sudoDir, "sudo"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${SUDO_LOG}"
if [[ "${1:-}" == "-v" ]]; then
  exit 0
fi
exec "$@"
`)

	result := runInstallDrydockWithPath(t, fixture, []string{"--target", target, "--yes", "--no-completions"}, map[string]string{
		"DRYDOCK_INSTALL_TEST_FORCE_SUDO": "1",
		"SUDO_LOG":                        sudoLog,
	}, sudoDir)

	if result.err != nil {
		t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	assertFileContains(t, target, "version: v1.2.3")
	assertFileContains(t, sudoLog, "-v")
	assertFileContains(t, sudoLog, "install")
}

func TestInstallDrydockDryRunWritesNoFiles(t *testing.T) {
	fixture := newInstallerFixture(t)
	root := t.TempDir()
	target := filepath.Join(root, "bin", "drydock")
	home := filepath.Join(root, "home")

	result := runInstallDrydock(t, fixture, []string{"--dry-run", "--target", target, "--yes"}, map[string]string{
		"HOME": home,
	})

	if result.err != nil {
		t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target exists after dry-run: stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".local/share/bash-completion/completions/drydock")); !os.IsNotExist(err) {
		t.Fatalf("bash completion exists after dry-run: stat err = %v", err)
	}
	if !strings.Contains(result.stdout, "Dry run complete. No files changed.") {
		t.Fatalf("stdout missing dry-run completion:\n%s", result.stdout)
	}
	if !strings.Contains(result.stdout, "bash:"+filepath.Join(home, ".local/share/bash-completion/completions/drydock")) {
		t.Fatalf("stdout missing planned completion path:\n%s", result.stdout)
	}
}

func TestInstallDrydockRequiresConfirmationForNonInteractiveInstall(t *testing.T) {
	fixture := newInstallerFixture(t)
	target := filepath.Join(t.TempDir(), "bin", "drydock")

	result := runInstallDrydock(t, fixture, []string{"--target", target, "--no-completions"}, nil)

	if result.err == nil {
		t.Fatalf("installer succeeded, want non-interactive confirmation failure\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	}
	if !strings.Contains(result.stderr, "Refusing to modify the filesystem") {
		t.Fatalf("stderr missing non-interactive refusal:\n%s", result.stderr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target exists after refused install: stat err = %v", err)
	}
}

func TestInstallDrydockClampsExistingTargetModes(t *testing.T) {
	fixture := newInstallerFixture(t)

	t.Run("adds executable bits", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "bin", "drydock")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}

		result := runInstallDrydock(t, fixture, []string{"--target", target, "--yes", "--no-completions"}, nil)

		if result.err != nil {
			t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Fatalf("target mode = %o, want 0755", got)
		}
	})

	t.Run("strips special bits", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "bin", "drydock")
		writeExecutable(t, target, "#!/usr/bin/env bash\necho old\n")
		if err := os.Chmod(target, 0o4755); err != nil {
			t.Fatal(err)
		}

		result := runInstallDrydock(t, fixture, []string{"--target", target, "--yes", "--no-completions"}, nil)

		if result.err != nil {
			t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode(); got&os.ModeSetuid != 0 {
			t.Fatalf("target mode retained setuid bit: %v", got)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Fatalf("target mode = %o, want 0755", got)
		}
	})
}

func TestInstallDrydockIfOutdatedSkipsMatchingInstalledBinary(t *testing.T) {
	fixture := newInstallerFixture(t)
	target := filepath.Join(t.TempDir(), "bin", "drydock")
	writeExecutable(t, target, readFile(t, fixture.fakeBin))

	result := runInstallDrydock(t, fixture, []string{"--if-outdated", "--target", target, "--yes"}, nil)

	if result.err != nil {
		t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	if !strings.Contains(result.stdout, "already matches latest; no update needed") {
		t.Fatalf("stdout missing if-outdated skip:\n%s", result.stdout)
	}
	if strings.Contains(result.stdout, "Completed successfully.") {
		t.Fatalf("stdout shows install after if-outdated skip:\n%s", result.stdout)
	}
}

func TestInstallDrydockCompletionOptions(t *testing.T) {
	fixture := newInstallerFixture(t)

	t.Run("explicit directories use required filenames", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "bin", "drydock")
		bashDir := filepath.Join(root, "bash")
		zshDir := filepath.Join(root, "zsh")
		fishDir := filepath.Join(root, "fish")

		result := runInstallDrydock(t, fixture, []string{
			"--target", target,
			"--bash-completion-dir", bashDir,
			"--zsh-completion-dir", zshDir,
			"--fish-completion-dir", fishDir,
			"--yes",
		}, nil)

		if result.err != nil {
			t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
		}
		assertFileContains(t, filepath.Join(bashDir, "drydock"), "complete -F _drydock drydock")
		assertFileContains(t, filepath.Join(zshDir, "_drydock"), "#compdef drydock")
		assertFileContains(t, filepath.Join(fishDir, "drydock.fish"), "complete -c drydock")
	})

	t.Run("no completions suppresses generation", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "bin", "drydock")
		bashDir := filepath.Join(root, "bash")

		result := runInstallDrydock(t, fixture, []string{
			"--target", target,
			"--bash-completion-dir", bashDir,
			"--no-completions",
			"--yes",
		}, nil)

		if result.err != nil {
			t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
		}
		if _, err := os.Stat(filepath.Join(bashDir, "drydock")); !os.IsNotExist(err) {
			t.Fatalf("bash completion exists with --no-completions: stat err = %v", err)
		}
		if !strings.Contains(result.stdout, "Completions:  disabled") {
			t.Fatalf("stdout missing disabled completion summary:\n%s", result.stdout)
		}
	})

	t.Run("explicit directory failure is hard error", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "bin", "drydock")
		notDir := filepath.Join(root, "not-dir")
		if err := os.WriteFile(notDir, []byte("not a dir"), 0o644); err != nil {
			t.Fatal(err)
		}

		result := runInstallDrydock(t, fixture, []string{
			"--target", target,
			"--bash-completion-dir", notDir,
			"--yes",
		}, nil)

		if result.err == nil {
			t.Fatalf("installer succeeded, want explicit completion dir failure\nstdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
		}
		if !strings.Contains(result.stderr, "explicit bash completion directory") {
			t.Fatalf("stderr missing explicit completion directory error:\n%s", result.stderr)
		}
	})

	t.Run("default completion failure warns and continues", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "bin", "drydock")
		homeFile := filepath.Join(root, "home-file")
		if err := os.WriteFile(homeFile, []byte("not a dir"), 0o644); err != nil {
			t.Fatal(err)
		}

		result := runInstallDrydock(t, fixture, []string{"--target", target, "--yes"}, map[string]string{
			"HOME": homeFile,
		})

		if result.err != nil {
			t.Fatalf("installer failed: %v\nstdout:\n%s\nstderr:\n%s", result.err, result.stdout, result.stderr)
		}
		if !strings.Contains(result.stderr, "could not create default bash completion directory") {
			t.Fatalf("stderr missing default completion warning:\n%s", result.stderr)
		}
		if !strings.Contains(result.stdout, "Completed successfully.") {
			t.Fatalf("stdout missing successful install:\n%s", result.stdout)
		}
	})
}

type installResult struct {
	stdout string
	stderr string
	err    error
}

func runInstallDrydock(t *testing.T, fixture installerFixture, args []string, extraEnv map[string]string) installResult {
	return runInstallDrydockWithPath(t, fixture, args, extraEnv)
}

func runInstallDrydockWithPath(t *testing.T, fixture installerFixture, args []string, extraEnv map[string]string, pathPrefix ...string) installResult {
	t.Helper()

	fakeBin := t.TempDir()
	writeFakeCurl(t, fakeBin)

	pathParts := append([]string{}, pathPrefix...)
	pathParts = append(pathParts, fakeBin, "/usr/bin", "/bin")

	cmd := exec.Command("bash", append([]string{"./install-drydock.sh"}, args...)...)
	cmd.Dir = "."
	cmd.Env = cleanEnv()
	cmd.Env = append(cmd.Env,
		"PATH="+strings.Join(pathParts, string(os.PathListSeparator)),
		"FIXTURE_DIR="+fixture.dir,
		"DRYDOCK_INSTALL_TESTING=1",
		"DRYDOCK_INSTALL_TEST_OS=Darwin",
		"DRYDOCK_INSTALL_TEST_ARCH=arm64",
		"HOME="+t.TempDir(),
		"XDG_DATA_HOME=",
		"XDG_CONFIG_HOME=",
		"ZDOTDIR=",
	)
	for key, value := range extraEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	stdout, stderr := strings.Builder{}, strings.Builder{}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return installResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func cleanEnv() []string {
	keep := []string{}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PATH=") ||
			strings.HasPrefix(entry, "HOME=") ||
			strings.HasPrefix(entry, "XDG_DATA_HOME=") ||
			strings.HasPrefix(entry, "XDG_CONFIG_HOME=") ||
			strings.HasPrefix(entry, "ZDOTDIR=") ||
			strings.HasPrefix(entry, "DRYDOCK_") ||
			strings.HasPrefix(entry, "FIXTURE_DIR=") {
			continue
		}
		keep = append(keep, entry)
	}
	return keep
}

func newInstallerFixture(t *testing.T) installerFixture {
	t.Helper()

	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "drydock")
	fakeScript := `#!/usr/bin/env bash
set -euo pipefail
if [[ -n "${DRYDOCK_FAKE_LOG:-}" ]]; then
  printf '%s\n' "$*" >>"${DRYDOCK_FAKE_LOG}"
fi
case "${1:-}" in
  version)
    printf 'version: v1.2.3\n'
    ;;
  completion)
    case "${2:-}" in
      bash) printf 'complete -F _drydock drydock\n' ;;
      zsh) printf '#compdef drydock\n' ;;
      fish) printf 'complete -c drydock\n' ;;
      *) exit 2 ;;
    esac
    ;;
  *)
    exit 2
    ;;
esac
`
	writeExecutable(t, fakeBin, fakeScript)

	checksums := strings.Builder{}
	for _, archive := range []string{
		"drydock_linux-amd64.tar.gz",
		"drydock_linux-arm64.tar.gz",
		"drydock_darwin-amd64.tar.gz",
		"drydock_darwin-arm64.tar.gz",
	} {
		sum := writeArchive(t, filepath.Join(dir, archive), fakeScript)
		fmt.Fprintf(&checksums, "%s  %s\n", sum, archive)
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(checksums.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	return installerFixture{
		dir:     dir,
		fakeBin: fakeBin,
	}
}

func writeArchive(t *testing.T, path, fakeScript string) string {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.Name = ""
	gzipWriter.ModTime = time.Unix(0, 0)
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{
		Name:    "drydock",
		Mode:    0o755,
		Size:    int64(len(fakeScript)),
		ModTime: time.Unix(0, 0),
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(fakeScript)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	return fileSHA256(t, path)
}

func writeFakeCurl(t *testing.T, dir string) {
	t.Helper()

	script := `#!/usr/bin/env bash
set -euo pipefail
out=""
url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o)
      out="$2"
      shift 2
      ;;
    -*)
      shift
      ;;
    *)
      url="$1"
      shift
      ;;
  esac
done
name="${url##*/}"
src="${FIXTURE_DIR}/${name}"
if [[ -z "${out}" || ! -f "${src}" ]]; then
  exit 22
fi
cp "${src}" "${out}"
`
	writeExecutable(t, filepath.Join(dir, "curl"), script)
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	got := readFile(t, path)
	if !strings.Contains(got, want) {
		t.Fatalf("%s missing %q:\n%s", path, want, got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func countLinesContaining(t *testing.T, path, needle string) int {
	t.Helper()
	count := 0
	for _, line := range strings.Split(readFile(t, path), "\n") {
		if strings.Contains(line, needle) {
			count++
		}
	}
	return count
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(content))
}
