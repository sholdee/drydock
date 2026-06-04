package pluginexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/pluginpolicy"
)

const pluginExecCommandTimeout = 10 * time.Second

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
				Timeout: pluginExecCommandTimeout,
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
	if len(result.Executions) != 1 {
		t.Fatalf("Executions = %#v, want one generate execution", result.Executions)
	}
	execution := result.Executions[0]
	if execution.Phase != "generate" || execution.Command == "" || strings.Contains(execution.Command, string(filepath.Separator)) {
		t.Fatalf("Execution = %#v, want generate with command basename", execution)
	}
	if execution.Duration <= 0 {
		t.Fatalf("Execution.Duration = %s, want positive duration", execution.Duration)
	}
	if _, err := os.Stat(filepath.Join(source, "generated.txt")); !os.IsNotExist(err) {
		t.Fatalf("original source generated.txt exists or unexpected stat error: %v", err)
	}
}

func TestDefaultRunnerSourceCopyScopeSkipsGitMetadata(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "marker.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".git", "config"), []byte("private\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	_, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir: source,
		Config:    sourceCopyTestConfig(t, ".git/config"),
		ProtectedRoots: []string{
			source,
		},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want skipped .git/config read failure")
	}
	if strings.Contains(err.Error(), ".git metadata") {
		t.Fatalf("Run() error = %v, want .git metadata skipped rather than rejected during copy", err)
	}
	var pluginErr *Error
	if !errors.As(err, &pluginErr) || pluginErr.ExitCode == nil || *pluginErr.ExitCode != 3 {
		t.Fatalf("Run() error = %v, want helper exit code 3 for missing staged .git/config", err)
	}
}

func TestDefaultRunnerRejectsProtectedArgumentsAndCredentialURLs(t *testing.T) {
	source := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		arg  string
		want string
	}{
		{name: "protected absolute", arg: filepath.Join(source, "secret.yaml"), want: "protected root"},
		{name: "unprotected absolute", arg: outside, want: "escapes plugin workdir"},
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
						Timeout: pluginExecCommandTimeout,
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

func TestDefaultRunnerAddsExtraEnv(t *testing.T) {
	source := t.TempDir()
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	result, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ExecConfig{
			Workdir: pluginpolicy.ExecWorkdirSource,
			Generate: pluginpolicy.ExecCommand{
				Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "env"},
				Timeout: pluginExecCommandTimeout,
			},
			Env: pluginpolicy.ExecEnv{Allow: []string{"DRYDOCK_PLUGINEXEC_HELPER"}},
			Output: pluginpolicy.ExecOutput{
				MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
				MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
			},
		},
		ProtectedRoots: []string{source},
		ExtraEnv:       []string{"PARAM_PATH=components/app.pkl"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(string(result.Stdout), "components/app.pkl") {
		t.Fatalf("Stdout = %q, want extra env value", result.Stdout)
	}
}

func TestDefaultRunnerRepositoryCopyScopeAllowsIncludedRepositoryFileArgument(t *testing.T) {
	repository := t.TempDir()
	source := filepath.Join(repository, "app")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "shared.txt"), []byte("shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	result, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir:     source,
		RepositoryDir: repository,
		SourceRelPath: "app",
		Config:        repositoryCopyTestConfig(t, []string{"shared.txt"}, "../shared.txt"),
		ProtectedRoots: []string{
			repository,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := string(result.Stdout); got != "shared\n" {
		t.Fatalf("Stdout = %q, want included repository file", got)
	}
}

func TestDefaultRunnerSourceCopyScopeRejectsRepositoryRelativeArgument(t *testing.T) {
	repository := t.TempDir()
	source := filepath.Join(repository, "app")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "shared.txt"), []byte("shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	_, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir: source,
		Config:    sourceCopyTestConfig(t, "../shared.txt"),
		ProtectedRoots: []string{
			repository,
		},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want source-scope argument rejection")
	}
	if !strings.Contains(err.Error(), "escapes plugin workdir") {
		t.Fatalf("Run() error = %v, want workdir escape rejection", err)
	}
}

func TestDefaultRunnerRepositoryCopyScopeRejectsArgumentEscapingRepository(t *testing.T) {
	repository := t.TempDir()
	source := filepath.Join(repository, "app")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	_, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir:     source,
		RepositoryDir: repository,
		SourceRelPath: "app",
		Config:        repositoryCopyTestConfig(t, nil, "../../escape.txt"),
		ProtectedRoots: []string{
			repository,
		},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want repository-scope argument rejection")
	}
	if !strings.Contains(err.Error(), "escapes plugin repository") {
		t.Fatalf("Run() error = %v, want repository escape rejection", err)
	}
}

func TestDefaultRunnerRepositoryCopyScopeRejectsIncludedFileSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior differs on Windows test hosts")
	}
	repository := t.TempDir()
	source := filepath.Join(repository, "app")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "target.txt"), []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(repository, "shared.txt")); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	_, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir:     source,
		RepositoryDir: repository,
		SourceRelPath: "app",
		Config:        repositoryCopyTestConfig(t, []string{"shared.txt"}, "../shared.txt"),
		ProtectedRoots: []string{
			repository,
		},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want included symlink rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Run() error = %v, want symlink rejection", err)
	}
}

func TestDefaultRunnerRepositoryCopyScopeRejectsIncludedDirectorySymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior differs on Windows test hosts")
	}
	repository := t.TempDir()
	source := filepath.Join(repository, "app")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repository, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(repository, "linked")); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	_, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir:     source,
		RepositoryDir: repository,
		SourceRelPath: "app",
		Config:        repositoryCopyTestConfig(t, []string{"linked/**"}, "../linked/file.txt"),
		ProtectedRoots: []string{
			repository,
		},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want included directory symlink rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Run() error = %v, want symlink rejection", err)
	}
}

func TestDefaultRunnerRepositoryCopyScopeRejectsIncludedGitMetadata(t *testing.T) {
	repository := t.TempDir()
	source := filepath.Join(repository, "app")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".git", "config"), []byte("private\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	_, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir:     source,
		RepositoryDir: repository,
		SourceRelPath: "app",
		Config:        repositoryCopyTestConfig(t, []string{"**"}, "../shared.txt"),
		ProtectedRoots: []string{
			repository,
		},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want .git metadata rejection")
	}
	if !strings.Contains(err.Error(), ".git metadata") {
		t.Fatalf("Run() error = %v, want .git metadata rejection", err)
	}
}

func TestDefaultRunnerRepositoryCopyScopeRejectsBackslashInclude(t *testing.T) {
	repository := t.TempDir()
	source := filepath.Join(repository, "app")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	_, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir:     source,
		RepositoryDir: repository,
		SourceRelPath: "app",
		Config:        repositoryCopyTestConfig(t, []string{`shared\**`}, "../shared/file.txt"),
		ProtectedRoots: []string{
			repository,
		},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want backslash include rejection")
	}
	if !strings.Contains(err.Error(), "slash-normalized") {
		t.Fatalf("Run() error = %v, want slash-normalized rejection", err)
	}
}

func TestDefaultRunnerRedactsSensitiveArguments(t *testing.T) {
	source := t.TempDir()
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	const secret = "top-secret-argument"
	_, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ExecConfig{
			Workdir: pluginpolicy.ExecWorkdirSource,
			Generate: pluginpolicy.ExecCommand{
				Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "manifest", "../" + secret},
				Timeout: pluginExecCommandTimeout,
			},
			Env: pluginpolicy.ExecEnv{Allow: []string{"DRYDOCK_PLUGINEXEC_HELPER"}},
			Output: pluginpolicy.ExecOutput{
				MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
				MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
			},
		},
		ProtectedRoots:  []string{source},
		SensitiveValues: []string{secret},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want argument rejection")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Run() error leaked sensitive argument: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("Run() error = %v, want redacted marker", err)
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
				Timeout: pluginExecCommandTimeout,
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
				Timeout: pluginExecCommandTimeout,
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
	if !strings.Contains(err.Error(), ControlledPath) || !strings.Contains(err.Error(), "install the executable") || !strings.Contains(err.Error(), "absolute trusted executable path") {
		t.Fatalf("Run() error = %v, want actionable missing executable guidance", err)
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
				Timeout: pluginExecCommandTimeout,
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
				Timeout: pluginExecCommandTimeout,
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

func TestDefaultRunnerChainsPostRenderers(t *testing.T) {
	source := t.TempDir()
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	result, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ExecConfig{
			Workdir: pluginpolicy.ExecWorkdirSource,
			Generate: pluginpolicy.ExecCommand{
				Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "raw"},
				Timeout: pluginExecCommandTimeout,
			},
			PostRenderers: []pluginpolicy.ExecCommand{
				{
					Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "append", "first"},
					Timeout: pluginExecCommandTimeout,
				},
				{
					Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "append", "second"},
					Timeout: pluginExecCommandTimeout,
				},
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
	got := string(result.Stdout)
	for _, want := range []string{"base", "first", "second"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Stdout = %q, want %q", got, want)
		}
	}
	if len(result.Executions) != 3 {
		t.Fatalf("Executions = %#v, want generate plus two post-renderers", result.Executions)
	}
	wantPhases := []string{"generate", "post-renderer 0", "post-renderer 1"}
	for index, want := range wantPhases {
		if result.Executions[index].Phase != want {
			t.Fatalf("Executions[%d].Phase = %q, want %q", index, result.Executions[index].Phase, want)
		}
	}
}

func TestDefaultRunnerReportsPostRendererFailure(t *testing.T) {
	source := t.TempDir()
	t.Setenv("DRYDOCK_PLUGINEXEC_HELPER", "1")
	_, err := (DefaultRunner{}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ExecConfig{
			Workdir: pluginpolicy.ExecWorkdirSource,
			Generate: pluginpolicy.ExecCommand{
				Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "raw"},
				Timeout: pluginExecCommandTimeout,
			},
			PostRenderers: []pluginpolicy.ExecCommand{{
				Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "fail"},
				Timeout: pluginExecCommandTimeout,
			}},
			Env: pluginpolicy.ExecEnv{Allow: []string{"DRYDOCK_PLUGINEXEC_HELPER"}},
			Output: pluginpolicy.ExecOutput{
				MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
				MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
			},
		},
		ProtectedRoots: []string{source},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want post-renderer failure")
	}
	if !strings.Contains(err.Error(), "post-renderer 0") {
		t.Fatalf("Run() error = %v, want post-renderer phase", err)
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
				Timeout: pluginExecCommandTimeout,
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
				Timeout: pluginExecCommandTimeout,
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
	args := helperProcessArgs(os.Args)
	if len(args) < 2 {
		os.Exit(2)
	}
	os.Exit(runHelperProcess(args))
}

func helperProcessArgs(args []string) []string {
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	return args
}

func runHelperProcess(args []string) int {
	switch args[1] {
	case "manifest":
		return runHelperManifest()
	case "raw":
		return runHelperRaw()
	case "append":
		return runHelperAppend(args)
	case "env":
		return runHelperEnv()
	case "read":
		return runHelperRead(args)
	case "sleep":
		return runHelperSleep()
	case "fail":
		return runHelperFail()
	default:
		return 2
	}
}

func runHelperManifest() int {
	if _, err := os.Stat("marker.txt"); err == nil {
		_ = os.WriteFile("generated.txt", []byte("temp"), 0o644)
	}
	fmt.Println("apiVersion: v1")
	fmt.Println("kind: ConfigMap")
	fmt.Println("metadata:")
	fmt.Println("  name: rendered")
	return 0
}

func runHelperRaw() int {
	fmt.Println("base")
	return 0
}

func runHelperAppend(args []string) int {
	input, _ := io.ReadAll(os.Stdin)
	fmt.Print(string(input))
	if len(args) > 2 {
		fmt.Println(args[2])
	}
	return 0
}

func runHelperEnv() int {
	fmt.Println(os.Getenv("PARAM_PATH"))
	return 0
}

func runHelperRead(args []string) int {
	for _, name := range args[2:] {
		data, err := os.ReadFile(name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		fmt.Print(string(data))
	}
	return 0
}

func runHelperSleep() int {
	time.Sleep(5 * time.Second)
	return 0
}

func runHelperFail() int {
	fmt.Fprintln(os.Stderr, "stderr secret:", os.Getenv("DRYDOCK_PLUGINEXEC_SECRET"))
	return 7
}

func helperPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func sourceCopyTestConfig(t *testing.T, readPath string) pluginpolicy.ExecConfig {
	t.Helper()
	return pluginpolicy.ExecConfig{
		Workdir: pluginpolicy.ExecWorkdirSource,
		Generate: pluginpolicy.ExecCommand{
			Command: []string{helperPath(t), "-test.run=TestHelperProcess", "--", "read", readPath},
			Timeout: pluginExecCommandTimeout,
		},
		Env: pluginpolicy.ExecEnv{Allow: []string{"DRYDOCK_PLUGINEXEC_HELPER"}},
		Output: pluginpolicy.ExecOutput{
			MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
			MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
		},
	}
}

func repositoryCopyTestConfig(t *testing.T, include []string, readPath string) pluginpolicy.ExecConfig {
	t.Helper()
	config := sourceCopyTestConfig(t, readPath)
	config.Copy = pluginpolicy.ExecCopy{
		Scope:   pluginpolicy.ExecCopyScopeRepository,
		Include: include,
	}
	return config
}
