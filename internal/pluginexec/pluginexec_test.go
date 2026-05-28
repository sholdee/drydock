package pluginexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/pluginpolicy"
)

func TestDefaultRunnerRunsGenerateFromTempSource(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "marker.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	result, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ExecConfig{
			Workdir: pluginpolicy.ExecWorkdirSource,
			Generate: pluginpolicy.ExecCommand{
				Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "manifest"},
				Timeout: time.Second,
			},
			Env: pluginpolicy.ExecEnv{Allow: []string{"DRYDOCK_PLUGINEXEC_HELPER"}},
			Output: pluginpolicy.ExecOutput{
				MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
				MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
			},
		},
		ProtectedRoots: []string{source},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(string(result.Stdout), "kind: ConfigMap") {
		t.Fatalf("Stdout = %q, want manifest", result.Stdout)
	}
	if _, err := os.Stat(filepath.Join(source, "generated.txt")); !os.IsNotExist(err) {
		t.Fatalf("original source generated.txt exists or unexpected stat error: %v", err)
	}
}

func TestDefaultRunnerRejectsProtectedArgumentsAndCredentialURLs(t *testing.T) {
	source := t.TempDir()
	for _, tt := range []struct {
		name string
		arg  string
		want string
	}{
		{name: "protected absolute", arg: filepath.Join(source, "secret.yaml"), want: "protected root"},
		{name: "relative escape", arg: "../escape.yaml", want: "escapes plugin workdir"},
		{name: "flag protected", arg: "--config=" + filepath.Join(source, "secret.yaml"), want: "protected root"},
		{name: "credential url", arg: "https://user:pass@example.test/repo", want: "credential-bearing URL"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
			_, err := (DefaultRunner{}).Run(context.Background(), Request{
				SourceDir: source,
				Config: pluginpolicy.ExecConfig{
					Workdir: pluginpolicy.ExecWorkdirSource,
					Generate: pluginpolicy.ExecCommand{
						Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "manifest", tt.arg},
						Timeout: time.Second,
					},
					Env: pluginpolicy.ExecEnv{Allow: []string{"DRYDOCK_PLUGINEXEC_HELPER"}},
					Output: pluginpolicy.ExecOutput{
						MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
						MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
					},
				},
				ProtectedRoots: []string{source},
			})
			if err == nil {
				t.Fatal("Run() error = nil, want argument rejection")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDefaultRunnerOmitsStderrFromFailure(t *testing.T) {
	source := t.TempDir()
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	t.Setenv("DRYDOCK_PLUGINEXEC_SECRET", "top-secret")
	_, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ExecConfig{
			Workdir: pluginpolicy.ExecWorkdirSource,
			Generate: pluginpolicy.ExecCommand{
				Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "fail"},
				Timeout: time.Second,
			},
			Env: pluginpolicy.ExecEnv{Allow: []string{"DRYDOCK_PLUGINEXEC_HELPER", "DRYDOCK_PLUGINEXEC_SECRET"}},
			Output: pluginpolicy.ExecOutput{
				MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
				MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
			},
		},
		ProtectedRoots: []string{source},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want failure")
	}
	if strings.Contains(err.Error(), "top-secret") || strings.Contains(err.Error(), "stderr secret") {
		t.Fatalf("Run() error leaked stderr: %v", err)
	}
	if !strings.Contains(err.Error(), "stderr omitted") {
		t.Fatalf("Run() error = %v, want stderr omitted note", err)
	}
}

func TestDefaultRunnerRejectsMissingCommand(t *testing.T) {
	source := t.TempDir()
	_, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ExecConfig{
			Workdir: pluginpolicy.ExecWorkdirSource,
			Generate: pluginpolicy.ExecCommand{
				Command: []string{"drydock-missing-test-command"},
				Timeout: time.Second,
			},
			Output: pluginpolicy.ExecOutput{
				MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
				MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
			},
		},
		ProtectedRoots: []string{source},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want missing command error")
	}
	if !strings.Contains(err.Error(), "not found on controlled PATH") {
		t.Fatalf("Run() error = %v, want controlled PATH lookup failure", err)
	}
}

func TestDefaultRunnerHonorsTimeoutAndCallerCancellation(t *testing.T) {
	source := t.TempDir()
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	_, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ExecConfig{
			Workdir: pluginpolicy.ExecWorkdirSource,
			Generate: pluginpolicy.ExecCommand{
				Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "sleep"},
				Timeout: time.Nanosecond,
			},
			Env: pluginpolicy.ExecEnv{Allow: []string{"DRYDOCK_PLUGINEXEC_HELPER"}},
			Output: pluginpolicy.ExecOutput{
				MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
				MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
			},
		},
		ProtectedRoots: []string{source},
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Run() error = %v, want timeout", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (DefaultRunner{}).Run(ctx, Request{
		SourceDir: source,
		Config: pluginpolicy.ExecConfig{
			Workdir: pluginpolicy.ExecWorkdirSource,
			Generate: pluginpolicy.ExecCommand{
				Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "manifest"},
				Timeout: time.Second,
			},
			Env: pluginpolicy.ExecEnv{Allow: []string{"DRYDOCK_PLUGINEXEC_HELPER"}},
			Output: pluginpolicy.ExecOutput{
				MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
				MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
			},
		},
		ProtectedRoots: []string{source},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestDefaultRunnerEnforcesOutputLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("printf is not available on the controlled Windows PATH")
	}
	source := t.TempDir()
	result, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ExecConfig{
			Workdir: pluginpolicy.ExecWorkdirSource,
			Generate: pluginpolicy.ExecCommand{
				Command: []string{"printf", strings.Repeat("x", 1024)},
				Timeout: time.Second,
			},
			Output: pluginpolicy.ExecOutput{
				MaxStdoutBytes: 8,
				MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
			},
		},
		ProtectedRoots: []string{source},
	})
	if err == nil || !strings.Contains(err.Error(), "stdout limit exceeded") {
		t.Fatalf("Run() error = %v, stdout len = %d, want stdout limit", err, len(result.Stdout))
	}
}

func TestDefaultRunnerRejectsSymlinkSourceAndSourceCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and executable mode behavior differs on Windows test hosts")
	}
	source := t.TempDir()
	if err := os.Symlink("/tmp", filepath.Join(source, "link")); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	_, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ExecConfig{
			Workdir: pluginpolicy.ExecWorkdirSource,
			Generate: pluginpolicy.ExecCommand{
				Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "manifest"},
				Timeout: time.Second,
			},
			Output: pluginpolicy.ExecOutput{
				MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
				MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
			},
		},
		ProtectedRoots: []string{source},
	})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Run() error = %v, want symlink rejection", err)
	}

	source = t.TempDir()
	command := filepath.Join(source, "renderer")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ExecConfig{
			Workdir: pluginpolicy.ExecWorkdirSource,
			Generate: pluginpolicy.ExecCommand{
				Command: []string{command},
				Timeout: time.Second,
			},
			Output: pluginpolicy.ExecOutput{
				MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
				MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
			},
		},
		ProtectedRoots: []string{source},
	})
	if err == nil || !strings.Contains(err.Error(), "protected root") {
		t.Fatalf("Run() error = %v, want protected command rejection", err)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("DRYDOCK_PLUGINEXEC_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(2)
	}
	switch args[1] {
	case "manifest":
		if _, err := os.Stat("marker.txt"); err == nil {
			_ = os.WriteFile("generated.txt", []byte("temp"), 0o644)
		}
		fmt.Println("apiVersion: v1")
		fmt.Println("kind: ConfigMap")
		fmt.Println("metadata:")
		fmt.Println("  name: rendered")
		os.Exit(0)
	case "sleep":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "stderr secret:", os.Getenv("DRYDOCK_PLUGINEXEC_SECRET"))
		os.Exit(7)
	default:
		os.Exit(2)
	}
}

func helperPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return path
}
