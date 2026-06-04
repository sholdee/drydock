package plugincontainer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/pluginpolicy"
)

func TestDefaultRunnerRunsDockerWithTempWorkspaceAndEnvFile(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "index.pkl"), []byte("input"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".git", "config"), []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	process := &recordingProcessRunner{
		stdout: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: rendered\n"),
	}
	t.Setenv("PARAM_PATH", "index.pkl")

	result, err := (DefaultRunner{ProcessRunner: process, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ContainerConfig{
			Runtime: pluginpolicy.ContainerRuntimeDocker,
			Image:   "registry.example.test/plugins/pkl@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Network: pluginpolicy.ContainerNetworkNone,
			Lifecycle: pluginpolicy.ExecConfig{
				Generate: pluginpolicy.ExecCommand{
					Command: []string{"pkl", "eval", "index.pkl"},
					Timeout: time.Second,
				},
				Env: pluginpolicy.ExecEnv{Allow: []string{"PARAM_PATH"}},
				Output: pluginpolicy.ExecOutput{
					MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
					MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(result.Stdout) != "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: rendered\n" {
		t.Fatalf("Stdout = %q, want rendered manifest", result.Stdout)
	}
	if len(process.requests) != 1 {
		t.Fatalf("process requests = %d, want 1", len(process.requests))
	}
	request := process.requests[0]
	assertArgSequence(t, request.Args, "--network", "none")
	assertArgSequence(t, request.Args, "--entrypoint", "pkl")
	if strings.Join(request.Args, "\x00") == "" || !strings.Contains(strings.Join(request.Args, "\x00"), "registry.example.test/plugins/pkl@sha256:") {
		t.Fatalf("docker args = %#v, want image reference", request.Args)
	}
	if strings.Contains(strings.Join(request.Args, "\x00"), "PARAM_PATH=index.pkl") {
		t.Fatalf("docker args leaked env value: %#v", request.Args)
	}
	if string(process.envFileData) != "PARAM_PATH=index.pkl\n" {
		t.Fatalf("env file = %q, want allowed env", process.envFileData)
	}
	if _, err := os.Stat(filepath.Join(mountSrc(t, request.Args), ".git", "config")); !os.IsNotExist(err) {
		t.Fatalf("container .git/config stat = %v, want skipped metadata", err)
	}
	if strings.Join(request.Env, "\x00") != "PATH=/usr/local/bin:/usr/bin:/bin" {
		t.Fatalf("docker client env = %#v, want controlled PATH only", request.Env)
	}
}

func TestDefaultRunnerOfflineAddsPullNeverAndRejectsDefaultNetwork(t *testing.T) {
	source := t.TempDir()
	process := &recordingProcessRunner{}
	config := pluginpolicy.ContainerConfig{
		Runtime: pluginpolicy.ContainerRuntimeDocker,
		Image:   "registry.example.test/plugins/pkl@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Network: pluginpolicy.ContainerNetworkNone,
		Lifecycle: pluginpolicy.ExecConfig{
			Generate: pluginpolicy.ExecCommand{Command: []string{"pkl"}, Timeout: time.Second},
			Output: pluginpolicy.ExecOutput{
				MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
				MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
			},
		},
	}
	if _, err := (DefaultRunner{ProcessRunner: process, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{SourceDir: source, Config: config, Offline: true}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	assertArgSequence(t, process.requests[0].Args, "--pull", "never")

	config.Network = pluginpolicy.ContainerNetworkDefault
	_, err := (DefaultRunner{ProcessRunner: process, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{SourceDir: source, Config: config, Offline: true})
	if err == nil {
		t.Fatal("Run() error = nil, want offline default network rejection")
	}
	if !strings.Contains(err.Error(), "network default") {
		t.Fatalf("Run() error = %v, want network default rejection", err)
	}
}

func TestDefaultRunnerOfflineRejectsRemoteDockerClientEnv(t *testing.T) {
	source := t.TempDir()
	config := basicContainerConfig()
	for _, tt := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "tcp host", env: map[string]string{"DOCKER_HOST": "tcp://docker.example.test:2376"}, want: "DOCKER_HOST"},
		{name: "context", env: map[string]string{"DOCKER_CONTEXT": "remote"}, want: "DOCKER_CONTEXT"},
		{name: "config", env: map[string]string{"DOCKER_CONFIG": "/tmp/docker"}, want: "DOCKER_CONFIG"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (DefaultRunner{ProcessRunner: &recordingProcessRunner{}, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
				SourceDir: source,
				Config:    config,
				Offline:   true,
				EnvLookup: mapLookup(tt.env),
			})
			if err == nil {
				t.Fatal("Run() error = nil, want remote Docker client env rejection")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestDefaultRunnerOfflineIsolatesAmbientDockerClientConfig(t *testing.T) {
	source := t.TempDir()
	ambientHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ambientHome, ".docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ambientHome, ".docker", "config.json"), []byte(`{"currentContext":"remote"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", ambientHome)
	process := &recordingProcessRunner{}

	_, err := (DefaultRunner{ProcessRunner: process, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir: source,
		Config:    basicContainerConfig(),
		Offline:   true,
		EnvLookup: mapLookup(nil),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(process.requests) != 1 {
		t.Fatalf("process requests = %d, want 1", len(process.requests))
	}
	env := envMap(process.requests[0].Env)
	home := env["HOME"]
	dockerConfig := env["DOCKER_CONFIG"]
	if home == "" || dockerConfig == "" {
		t.Fatalf("docker client env = %#v, want isolated HOME and DOCKER_CONFIG", process.requests[0].Env)
	}
	if home == ambientHome || dockerConfig == filepath.Join(ambientHome, ".docker") {
		t.Fatalf("docker client env = %#v, used ambient Docker config", process.requests[0].Env)
	}
	if home != dockerConfig {
		t.Fatalf("HOME = %q, DOCKER_CONFIG = %q, want same isolated config dir", home, dockerConfig)
	}
	if process.dockerConfigHadConfig {
		t.Fatalf("docker client env = %#v, isolated Docker config unexpectedly had config.json", process.requests[0].Env)
	}
	if _, err := os.Stat(filepath.Join(home, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("isolated Docker config stat = %v, want empty config dir", err)
	}
}

func TestDefaultRunnerCleansUpContainerOnTimeout(t *testing.T) {
	source := t.TempDir()
	process := &recordingProcessRunner{err: context.DeadlineExceeded, cid: "container-123", assertCIDFileAbsent: true}
	_, err := (DefaultRunner{ProcessRunner: process, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir: source,
		Config:    basicContainerConfig(),
	})
	if err == nil {
		t.Fatal("Run() error = nil, want timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Run() error = %v, want timeout", err)
	}
	if len(process.requests) != 2 {
		t.Fatalf("process requests = %d, want docker run plus cleanup", len(process.requests))
	}
	if process.cidFileAlreadyExist {
		t.Fatal("cidfile existed before docker run wrote container id")
	}
	cleanup := process.requests[1]
	want := []string{"/usr/bin/docker", "rm", "-f", "container-123"}
	if strings.Join(cleanup.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("cleanup args = %#v, want %#v", cleanup.Args, want)
	}
}

func TestDefaultRunnerEnforcesOutputLimits(t *testing.T) {
	source := t.TempDir()
	config := basicContainerConfig()
	config.Lifecycle.Output.MaxStdoutBytes = 4
	_, err := (DefaultRunner{ProcessRunner: &recordingProcessRunner{stdout: []byte("12345")}, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir: source,
		Config:    config,
	})
	if err == nil || !strings.Contains(err.Error(), "stdout limit exceeded") {
		t.Fatalf("Run() error = %v, want stdout limit exceeded", err)
	}

	config = basicContainerConfig()
	config.Lifecycle.Output.MaxStderrBytes = 4
	_, err = (DefaultRunner{ProcessRunner: &recordingProcessRunner{stderr: []byte("12345")}, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir: source,
		Config:    config,
	})
	if err == nil || !strings.Contains(err.Error(), "stderr limit exceeded") {
		t.Fatalf("Run() error = %v, want stderr limit exceeded", err)
	}
}

func TestDefaultRunnerRejectsCredentialBearingArguments(t *testing.T) {
	source := t.TempDir()
	_, err := (DefaultRunner{ProcessRunner: &recordingProcessRunner{}, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ContainerConfig{
			Runtime: pluginpolicy.ContainerRuntimeDocker,
			Image:   "registry.example.test/plugins/pkl@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Network: pluginpolicy.ContainerNetworkNone,
			Lifecycle: pluginpolicy.ExecConfig{
				Generate: pluginpolicy.ExecCommand{
					Command: []string{"pkl", "eval", "https://user:pass@example.test/repo"},
					Timeout: time.Second,
				},
				Output: pluginpolicy.ExecOutput{
					MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
					MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
				},
			},
		},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want credential-bearing URL rejection")
	}
	if !strings.Contains(err.Error(), "credential-bearing URL") {
		t.Fatalf("Run() error = %v, want credential-bearing URL rejection", err)
	}
}

type recordingProcessRunner struct {
	requests              []ProcessRequest
	stdout                []byte
	stderr                []byte
	err                   error
	cid                   string
	assertCIDFileAbsent   bool
	cidFileAlreadyExist   bool
	dockerConfigHadConfig bool
	envFileData           []byte
}

func (r *recordingProcessRunner) Run(_ context.Context, request ProcessRequest) error {
	r.requests = append(r.requests, cloneProcessRequest(request))
	if dockerConfig := envMap(request.Env)["DOCKER_CONFIG"]; dockerConfig != "" {
		if _, err := os.Stat(filepath.Join(dockerConfig, "config.json")); err == nil {
			r.dockerConfigHadConfig = true
		}
	}
	if cidFile := optionalArgAfter(request.Args, "--cidfile"); cidFile != "" && r.cid != "" {
		if r.assertCIDFileAbsent {
			if _, err := os.Stat(cidFile); err == nil || !os.IsNotExist(err) {
				r.cidFileAlreadyExist = true
			}
		}
		_ = os.WriteFile(cidFile, []byte(r.cid), 0o600)
	}
	if envFile := optionalArgAfter(request.Args, "--env-file"); envFile != "" {
		r.envFileData, _ = os.ReadFile(envFile)
	}
	if len(r.stdout) > 0 {
		_, _ = request.Stdout.Write(r.stdout)
	}
	if len(r.stderr) > 0 {
		_, _ = request.Stderr.Write(r.stderr)
	}
	return r.err
}

func cloneProcessRequest(request ProcessRequest) ProcessRequest {
	var stdin bytes.Buffer
	if request.Stdin != nil {
		_, _ = io.Copy(&stdin, request.Stdin)
	}
	return ProcessRequest{
		Path:    request.Path,
		Args:    append([]string(nil), request.Args...),
		Dir:     request.Dir,
		Env:     append([]string(nil), request.Env...),
		Timeout: request.Timeout,
		Stdin:   bytes.NewReader(stdin.Bytes()),
		Stdout:  request.Stdout,
		Stderr:  request.Stderr,
	}
}

func assertArgSequence(t *testing.T, args []string, left, right string) {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == left && args[index+1] == right {
			return
		}
	}
	t.Fatalf("args = %#v, missing %s %s", args, left, right)
}

func argAfter(t *testing.T, args []string, flag string) string {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag {
			return args[index+1]
		}
	}
	t.Fatalf("args = %#v, missing %s", args, flag)
	return ""
}

func optionalArgAfter(args []string, flag string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag {
			return args[index+1]
		}
	}
	return ""
}

func mountSrc(t *testing.T, args []string) string {
	t.Helper()
	value := argAfter(t, args, "--mount")
	for part := range strings.SplitSeq(value, ",") {
		if src, ok := strings.CutPrefix(part, "src="); ok {
			return src
		}
	}
	t.Fatalf("mount = %q, missing src", value)
	return ""
}

func TestDefaultRunnerReportsProcessExit(t *testing.T) {
	source := t.TempDir()
	_, err := (DefaultRunner{
		ProcessRunner: &recordingProcessRunner{err: exitError{Code: 7}},
		DockerPath:    "/usr/bin/docker",
	}).Run(context.Background(), Request{
		SourceDir: source,
		Config: pluginpolicy.ContainerConfig{
			Runtime: pluginpolicy.ContainerRuntimeDocker,
			Image:   "registry.example.test/plugins/pkl@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Network: pluginpolicy.ContainerNetworkNone,
			Lifecycle: pluginpolicy.ExecConfig{
				Generate: pluginpolicy.ExecCommand{Command: []string{"pkl"}, Timeout: time.Second},
				Output: pluginpolicy.ExecOutput{
					MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
					MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
				},
			},
		},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want process exit")
	}
	var pluginErr *pluginexec.Error
	if !errors.As(err, &pluginErr) || pluginErr.ExitCode == nil || *pluginErr.ExitCode != 7 {
		t.Fatalf("Run() error = %v, want plugin exec exit code 7", err)
	}
}

func basicContainerConfig() pluginpolicy.ContainerConfig {
	return pluginpolicy.ContainerConfig{
		Runtime: pluginpolicy.ContainerRuntimeDocker,
		Image:   "registry.example.test/plugins/pkl@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Network: pluginpolicy.ContainerNetworkNone,
		Lifecycle: pluginpolicy.ExecConfig{
			Generate: pluginpolicy.ExecCommand{Command: []string{"pkl"}, Timeout: time.Second},
			Output: pluginpolicy.ExecOutput{
				MaxStdoutBytes: pluginpolicy.DefaultMaxStdoutBytes,
				MaxStderrBytes: pluginpolicy.DefaultMaxStderrBytes,
			},
		},
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			out[name] = value
		}
	}
	return out
}
