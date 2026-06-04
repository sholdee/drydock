package plugincontainer

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestDefaultRunnerAddsDeterministicCacheMountArgsAfterWorkMount(t *testing.T) {
	source := t.TempDir()
	cacheRoot := filepath.Join(t.TempDir(), "plugin-cache")
	fingerprint := strings.Repeat("a", 64)
	process := &recordingProcessRunner{}
	config := basicContainerConfig()
	config.CacheMounts = []pluginpolicy.ContainerCacheMount{
		{Name: "z-cache", Target: "/drydock-cache/z"},
		{Name: "a-cache", Target: "/drydock-cache/a"},
	}

	_, err := (DefaultRunner{ProcessRunner: process, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir:         source,
		PluginName:        "team/plugin",
		PolicyFingerprint: fingerprint,
		Config:            config,
		CacheRoot:         cacheRoot,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(process.requests) != 1 {
		t.Fatalf("process requests = %d, want 1", len(process.requests))
	}
	args := process.requests[0].Args
	wantA := filepath.Join(cacheRoot, fingerprint, sha256Hex("team/plugin"), "a-cache")
	wantZ := filepath.Join(cacheRoot, fingerprint, sha256Hex("team/plugin"), "z-cache")
	assertDeterministicCacheMountArgs(t, args, wantA, wantZ)
	assertCacheMountMetadataAndLock(t, wantA, fingerprint, sha256Hex("team/plugin"))
}

func assertDeterministicCacheMountArgs(t *testing.T, args []string, wantA, wantZ string) {
	t.Helper()
	mounts := mountArgs(args)
	if len(mounts) != 3 {
		t.Fatalf("mounts = %#v, want work plus two cache mounts", mounts)
	}
	if !strings.Contains(mounts[0], "dst=/work") {
		t.Fatalf("first mount = %q, want /work", mounts[0])
	}
	if mounts[1] != "type=bind,src="+wantA+",target=/drydock-cache/a" {
		t.Fatalf("second mount = %q, want a-cache bind", mounts[1])
	}
	if mounts[2] != "type=bind,src="+wantZ+",target=/drydock-cache/z" {
		t.Fatalf("third mount = %q, want z-cache bind", mounts[2])
	}
	if indexOfArg(args, "--workdir") < indexOfMount(args, mounts[2]) {
		t.Fatalf("args = %#v, want cache mounts before --workdir", args)
	}
	if strings.Contains(strings.Join(mounts, "\x00"), "team/plugin") {
		t.Fatalf("cache mount used raw plugin name: %#v", mounts)
	}
}

func assertCacheMountMetadataAndLock(t *testing.T, wantA, fingerprint, pluginHash string) {
	t.Helper()
	metadata := readCacheMetadata(t, wantA)
	if metadata.PolicyFingerprint != fingerprint || metadata.PluginNameSHA256 != pluginHash || metadata.CacheName != "a-cache" || metadata.Target != "/drydock-cache/a" {
		t.Fatalf("metadata = %#v, want cache identity", metadata)
	}
	if mode := statMode(t, wantA); mode != 0o700 {
		t.Fatalf("cache directory mode = %o, want 700", mode)
	}
	if _, err := os.Stat(filepath.Join(wantA, containerCacheLockSuffix)); !os.IsNotExist(err) {
		t.Fatalf("cache lock stat = %v, want lock outside mounted cache directory", err)
	}
	if _, err := os.Stat(containerCacheLockPath(wantA)); err != nil {
		t.Fatalf("cache lock stat = %v, want sibling lock file", err)
	}
}

func TestDefaultRunnerUsesDefaultUserCacheRootForCacheMounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	fingerprint := strings.Repeat("0", 64)
	config := basicContainerConfig()
	config.CacheMounts = []pluginpolicy.ContainerCacheMount{{Name: "tool-cache", Target: "/drydock-cache/tool"}}

	_, err = (DefaultRunner{ProcessRunner: &recordingProcessRunner{}, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir:         source,
		PluginName:        "plugin",
		PolicyFingerprint: fingerprint,
		Config:            config,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := filepath.Join(userCacheDir, "drydock", "plugin-cache", fingerprint, sha256Hex("plugin"), "tool-cache")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("default cache dir stat = %v, want %s", err, want)
	}
}

func TestDefaultRunnerRejectsOfflineDefaultNetworkBeforeCacheCreation(t *testing.T) {
	source := t.TempDir()
	cacheRoot := filepath.Join(t.TempDir(), "plugin-cache")
	config := basicContainerConfig()
	config.Network = pluginpolicy.ContainerNetworkDefault
	config.CacheMounts = []pluginpolicy.ContainerCacheMount{{Name: "tool-cache", Target: "/drydock-cache/tool"}}

	_, err := (DefaultRunner{ProcessRunner: &recordingProcessRunner{}, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir:         source,
		PluginName:        "plugin",
		PolicyFingerprint: strings.Repeat("b", 64),
		Config:            config,
		Offline:           true,
		CacheRoot:         cacheRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "network default") {
		t.Fatalf("Run() error = %v, want network default rejection", err)
	}
	if _, statErr := os.Stat(cacheRoot); !os.IsNotExist(statErr) {
		t.Fatalf("cache root stat = %v, want not created", statErr)
	}
}

func TestDefaultRunnerRejectsCacheInsideProtectedRootBeforeCreation(t *testing.T) {
	source := t.TempDir()
	protected := t.TempDir()
	cacheRoot := filepath.Join(protected, "plugin-cache")
	config := basicContainerConfig()
	config.CacheMounts = []pluginpolicy.ContainerCacheMount{{Name: "tool-cache", Target: "/drydock-cache/tool"}}

	_, err := (DefaultRunner{ProcessRunner: &recordingProcessRunner{}, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir:         source,
		PluginName:        "plugin",
		PolicyFingerprint: strings.Repeat("c", 64),
		Config:            config,
		ForbiddenRoots:    []string{protected},
		CacheRoot:         cacheRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "protected root") {
		t.Fatalf("Run() error = %v, want protected root rejection", err)
	}
	if _, statErr := os.Stat(cacheRoot); !os.IsNotExist(statErr) {
		t.Fatalf("cache root stat = %v, want not created", statErr)
	}
}

func TestDefaultRunnerRejectsFinalCacheDirectorySymlink(t *testing.T) {
	source := t.TempDir()
	cacheRoot := filepath.Join(t.TempDir(), "plugin-cache")
	fingerprint := strings.Repeat("d", 64)
	finalDir := filepath.Join(cacheRoot, fingerprint, sha256Hex("plugin"), "tool-cache")
	if err := os.MkdirAll(filepath.Dir(finalDir), 0o700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, finalDir); err != nil {
		t.Fatal(err)
	}
	config := basicContainerConfig()
	config.CacheMounts = []pluginpolicy.ContainerCacheMount{{Name: "tool-cache", Target: "/drydock-cache/tool"}}

	_, err := (DefaultRunner{ProcessRunner: &recordingProcessRunner{}, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir:         source,
		PluginName:        "plugin",
		PolicyFingerprint: fingerprint,
		Config:            config,
		CacheRoot:         cacheRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Run() error = %v, want symlink rejection", err)
	}
}

func TestValidateContainerCacheMountsForDockerRechecksPreparedSource(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "plugin-cache")
	protected := t.TempDir()
	fingerprint := strings.Repeat("1", 64)
	config := basicContainerConfig()
	config.CacheMounts = []pluginpolicy.ContainerCacheMount{{Name: "tool-cache", Target: "/drydock-cache/tool"}}
	mounts, release, err := prepareContainerCacheMounts(context.Background(), Request{
		SourceDir:         t.TempDir(),
		PluginName:        "plugin",
		PolicyFingerprint: fingerprint,
		Config:            config,
		CacheRoot:         cacheRoot,
	})
	if err != nil {
		t.Fatalf("prepareContainerCacheMounts() error = %v", err)
	}
	defer release()
	if len(mounts) != 1 {
		t.Fatalf("mounts = %#v, want one mount", mounts)
	}
	if err := os.RemoveAll(mounts[0].Source); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(protected, mounts[0].Source); err != nil {
		t.Fatal(err)
	}

	err = validateContainerCacheMountsForDocker(mounts, []string{protected})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("validateContainerCacheMountsForDocker() error = %v, want symlink recheck", err)
	}
}

func TestDefaultRunnerRejectsCacheMetadataMismatch(t *testing.T) {
	source := t.TempDir()
	cacheRoot := filepath.Join(t.TempDir(), "plugin-cache")
	fingerprint := strings.Repeat("e", 64)
	finalDir := filepath.Join(cacheRoot, fingerprint, sha256Hex("plugin"), "tool-cache")
	if err := os.MkdirAll(finalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeContainerCacheMetadata(filepath.Join(finalDir, containerCacheMetadataFile), containerCacheMetadata{
		Version:           containerCacheMetadataV1,
		PolicyFingerprint: fingerprint,
		PluginNameSHA256:  sha256Hex("other-plugin"),
		CacheName:         "tool-cache",
		Target:            "/drydock-cache/tool",
	}); err != nil {
		t.Fatal(err)
	}
	config := basicContainerConfig()
	config.CacheMounts = []pluginpolicy.ContainerCacheMount{{Name: "tool-cache", Target: "/drydock-cache/tool"}}

	_, err := (DefaultRunner{ProcessRunner: &recordingProcessRunner{}, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir:         source,
		PluginName:        "plugin",
		PolicyFingerprint: fingerprint,
		Config:            config,
		CacheRoot:         cacheRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "metadata mismatch") {
		t.Fatalf("Run() error = %v, want metadata mismatch", err)
	}
}

func TestDefaultRunnerRejectsExistingCacheDirectoryMissingMetadata(t *testing.T) {
	source := t.TempDir()
	cacheRoot := filepath.Join(t.TempDir(), "plugin-cache")
	fingerprint := strings.Repeat("e", 64)
	finalDir := filepath.Join(cacheRoot, fingerprint, sha256Hex("plugin"), "tool-cache")
	if err := os.MkdirAll(finalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := basicContainerConfig()
	config.CacheMounts = []pluginpolicy.ContainerCacheMount{{Name: "tool-cache", Target: "/drydock-cache/tool"}}

	_, err := (DefaultRunner{ProcessRunner: &recordingProcessRunner{}, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir:         source,
		PluginName:        "plugin",
		PolicyFingerprint: fingerprint,
		Config:            config,
		CacheRoot:         cacheRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "missing metadata") {
		t.Fatalf("Run() error = %v, want missing metadata", err)
	}
}

func TestDefaultRunnerHoldsCacheLockDuringContainerLifecycle(t *testing.T) {
	source := t.TempDir()
	cacheRoot := filepath.Join(t.TempDir(), "plugin-cache")
	fingerprint := strings.Repeat("f", 64)
	finalDir := filepath.Join(cacheRoot, fingerprint, sha256Hex("plugin"), "tool-cache")
	process := &recordingProcessRunner{
		onRun: func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
			defer cancel()
			lock, err := lockContainerCacheDirectory(ctx, finalDir)
			if err == nil {
				lock.Close()
				return errors.New("cache lock was available during container run")
			}
			if !strings.Contains(err.Error(), "context deadline exceeded") {
				return err
			}
			return nil
		},
	}
	config := basicContainerConfig()
	config.CacheMounts = []pluginpolicy.ContainerCacheMount{{Name: "tool-cache", Target: "/drydock-cache/tool"}}

	_, err := (DefaultRunner{ProcessRunner: process, DockerPath: "/usr/bin/docker"}).Run(context.Background(), Request{
		SourceDir:         source,
		PluginName:        "plugin",
		PolicyFingerprint: fingerprint,
		Config:            config,
		CacheRoot:         cacheRoot,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
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
	onRun                 func() error
}

func (r *recordingProcessRunner) Run(_ context.Context, request ProcessRequest) error {
	r.requests = append(r.requests, cloneProcessRequest(request))
	if r.onRun != nil {
		return r.onRun()
	}
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

func mountArgs(args []string) []string {
	var mounts []string
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--mount" {
			mounts = append(mounts, args[index+1])
		}
	}
	return mounts
}

func indexOfArg(args []string, arg string) int {
	for index, value := range args {
		if value == arg {
			return index
		}
	}
	return -1
}

func indexOfMount(args []string, mount string) int {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--mount" && args[index+1] == mount {
			return index
		}
	}
	return -1
}

func readCacheMetadata(t *testing.T, dir string) containerCacheMetadata {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, containerCacheMetadataFile))
	if err != nil {
		t.Fatal(err)
	}
	var metadata containerCacheMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	return metadata
}

func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
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
