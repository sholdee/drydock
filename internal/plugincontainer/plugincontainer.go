package plugincontainer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/pluginpolicy"
)

const (
	workMount = "/work"
)

type Runner interface {
	Run(ctx context.Context, request Request) (pluginexec.Result, error)
}

type ProcessRunner interface {
	Run(ctx context.Context, request ProcessRequest) error
}

type DefaultRunner struct {
	ProcessRunner ProcessRunner
	DockerPath    string
}

type Request struct {
	SourceDir       string
	RepositoryDir   string
	SourceRelPath   string
	Config          pluginpolicy.ContainerConfig
	Offline         bool
	EnvLookup       func(string) (string, bool)
	ExtraEnv        []string
	SensitiveValues []string
}

type ProcessRequest struct {
	Path    string
	Args    []string
	Dir     string
	Env     []string
	Timeout time.Duration
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

func (r DefaultRunner) Run(ctx context.Context, request Request) (pluginexec.Result, error) {
	return runWithProcess(ctx, request, r.ProcessRunner, r.DockerPath)
}

func runWithProcess(ctx context.Context, request Request, processRunner ProcessRunner, dockerPath string) (pluginexec.Result, error) {
	if err := ctx.Err(); err != nil {
		return pluginexec.Result{}, err
	}
	if processRunner == nil {
		processRunner = defaultProcessRunner{}
	}
	if strings.TrimSpace(request.SourceDir) == "" {
		return pluginexec.Result{}, &pluginexec.Error{Reason: "source directory is required"}
	}
	if request.Offline && request.Config.Network == pluginpolicy.ContainerNetworkDefault {
		return pluginexec.Result{}, &pluginexec.Error{Reason: "container network default is not allowed when offline"}
	}
	dockerClientConfigDir, cleanupDockerClientConfig, err := prepareDockerClientConfig(request)
	if err != nil {
		return pluginexec.Result{}, err
	}
	defer cleanupDockerClientConfig()
	workspace, err := pluginexec.PrepareWorkspace(pluginexec.Request{
		SourceDir:     request.SourceDir,
		RepositoryDir: request.RepositoryDir,
		SourceRelPath: request.SourceRelPath,
		Config:        request.Config.Lifecycle,
	})
	if err != nil {
		return pluginexec.Result{}, err
	}
	defer workspace.Cleanup()
	if err := makeWorkspaceWritable(workspace); err != nil {
		return pluginexec.Result{}, err
	}
	containerEnv, dockerEnv, err := containerProcessEnv(request, dockerClientConfigDir)
	if err != nil {
		return pluginexec.Result{}, err
	}
	return runContainerLifecycle(ctx, request, processRunner, dockerPath, workspace, dockerEnv, containerEnv)
}

func prepareDockerClientConfig(request Request) (string, func(), error) {
	if !request.Offline {
		return "", func() {}, nil
	}
	if err := rejectOfflineDockerClientRemoteConfig(request.EnvLookup); err != nil {
		return "", nil, &pluginexec.Error{Reason: "offline container plugin requires a local Docker client", Err: err}
	}
	dir, cleanup, err := createOfflineDockerClientConfig()
	if err != nil {
		return "", nil, &pluginexec.Error{Reason: "offline container plugin requires isolated Docker client config", Err: err}
	}
	return dir, cleanup, nil
}

func containerProcessEnv(request Request, dockerClientConfigDir string) ([]string, []string, error) {
	containerEnv, err := pluginexec.BuildEnv(request.Config.Lifecycle.Env, request.EnvLookup, request.ExtraEnv)
	if err != nil {
		return nil, nil, err
	}
	dockerEnv := dockerClientEnv(request.EnvLookup, dockerClientConfigDir)
	return containerEnv, dockerEnv, nil
}

func runContainerLifecycle(ctx context.Context, request Request, processRunner ProcessRunner, dockerPath string, workspace pluginexec.Workspace, dockerEnv, containerEnv []string) (pluginexec.Result, error) {
	var executions []pluginexec.Execution
	if request.Config.Lifecycle.Init != nil {
		result, err := runConfiguredContainerCommand(ctx, processRunner, dockerPath, "init", *request.Config.Lifecycle.Init, nil, workspace, request.Config, request.Offline, dockerEnv, containerEnv)
		if err != nil {
			return pluginexec.Result{}, err
		}
		executions = append(executions, result.Execution)
	}
	result, err := runConfiguredContainerCommand(ctx, processRunner, dockerPath, "generate", request.Config.Lifecycle.Generate, nil, workspace, request.Config, request.Offline, dockerEnv, containerEnv)
	if err != nil {
		return pluginexec.Result{}, err
	}
	stdout := result.Stdout
	executions = append(executions, result.Execution)
	for index, command := range request.Config.Lifecycle.PostRenderers {
		result, err := runConfiguredContainerCommand(ctx, processRunner, dockerPath, fmt.Sprintf("post-renderer %d", index), command, stdout, workspace, request.Config, request.Offline, dockerEnv, containerEnv)
		if err != nil {
			return pluginexec.Result{}, err
		}
		stdout = result.Stdout
		executions = append(executions, result.Execution)
	}
	return pluginexec.Result{Stdout: stdout, Executions: executions}, nil
}

type commandResult struct {
	Stdout    []byte
	Execution pluginexec.Execution
}

func runConfiguredContainerCommand(
	ctx context.Context,
	processRunner ProcessRunner,
	dockerPath string,
	phase string,
	command pluginpolicy.ExecCommand,
	stdin []byte,
	workspace pluginexec.Workspace,
	config pluginpolicy.ContainerConfig,
	offline bool,
	dockerEnv []string,
	containerEnv []string,
) (commandResult, error) {
	if err := validateContainerCommand(command.Command); err != nil {
		return commandResult{}, &pluginexec.Error{Phase: phase, Command: safeCommandName(command.Command), Reason: "invalid command", Err: err}
	}
	envFile, cleanupEnvFile, err := writeContainerEnvFile(containerEnv)
	if err != nil {
		return commandResult{}, &pluginexec.Error{Phase: phase, Command: safeCommandName(command.Command), Reason: "invalid container env", Err: err}
	}
	defer cleanupEnvFile()
	cidFile, cleanupCIDFile, err := createCIDFile()
	if err != nil {
		return commandResult{}, &pluginexec.Error{Phase: phase, Command: safeCommandName(command.Command), Reason: "invalid cidfile", Err: err}
	}
	defer cleanupCIDFile()
	docker, err := resolveDockerPath(dockerPath)
	if err != nil {
		return commandResult{}, &pluginexec.Error{Phase: phase, Command: "docker", Reason: "invalid runtime", Err: err}
	}
	stdout := &limitBuffer{limit: config.Lifecycle.Output.MaxStdoutBytes}
	stderr := &limitBuffer{limit: config.Lifecycle.Output.MaxStderrBytes}
	args := dockerRunArgs(config, workspace, offline, envFile, cidFile, command.Command)
	started := time.Now()
	err = processRunner.Run(ctx, ProcessRequest{
		Path:    docker,
		Args:    append([]string{docker}, args...),
		Dir:     workspace.Workdir,
		Env:     dockerEnv,
		Timeout: command.Timeout,
		Stdin:   bytes.NewReader(stdin),
		Stdout:  stdout,
		Stderr:  stderr,
	})
	execution := pluginexec.Execution{
		Phase:    phase,
		Command:  safeCommandName(command.Command),
		Duration: time.Since(started),
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			_ = cleanupContainer(ctx, processRunner, docker, dockerEnv, cidFile)
			return commandResult{}, err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			_ = cleanupContainer(ctx, processRunner, docker, dockerEnv, cidFile)
			return commandResult{}, &pluginexec.Error{Phase: phase, Command: execution.Command, Reason: "command timed out"}
		}
		var exitErr exitError
		if errors.As(err, &exitErr) {
			code := exitErr.Code
			return commandResult{}, &pluginexec.Error{Phase: phase, Command: execution.Command, Reason: "container command failed; stderr omitted to avoid leaking secrets", ExitCode: &code}
		}
		return commandResult{}, &pluginexec.Error{Phase: phase, Command: execution.Command, Reason: "container command failed; stderr omitted to avoid leaking secrets", Err: err}
	}
	if stdout.overflow {
		return commandResult{}, &pluginexec.Error{Phase: phase, Command: execution.Command, Reason: "stdout limit exceeded"}
	}
	if stderr.overflow {
		return commandResult{}, &pluginexec.Error{Phase: phase, Command: execution.Command, Reason: "stderr limit exceeded; stderr omitted to avoid leaking secrets"}
	}
	return commandResult{Stdout: stdout.Bytes(), Execution: execution}, nil
}

func dockerRunArgs(config pluginpolicy.ContainerConfig, workspace pluginexec.Workspace, offline bool, envFile string, cidFile string, command []string) []string {
	network := string(config.Network)
	if network == "" {
		network = string(pluginpolicy.DefaultContainerNetwork)
	}
	args := []string{
		"run",
		"--rm",
		"--interactive",
		"--cidfile", cidFile,
		"--network", network,
		"--mount", "type=bind,src=" + workspace.ArgumentRoot + ",dst=" + workMount,
		"--workdir", containerWorkdir(workspace),
	}
	if offline {
		args = append(args, "--pull", "never")
	}
	if envFile != "" {
		args = append(args, "--env-file", envFile)
	}
	args = append(args, "--entrypoint", command[0], config.Image)
	args = append(args, command[1:]...)
	return args
}

func writeContainerEnvFile(env []string) (string, func(), error) {
	var lines []string
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" || name == "PATH" {
			continue
		}
		if strings.ContainsAny(value, "\r\n") {
			return "", func() {}, fmt.Errorf("env value %s contains a newline", name)
		}
		lines = append(lines, name+"="+value)
	}
	if len(lines) == 0 {
		return "", func() {}, nil
	}
	file, err := os.CreateTemp("", "drydock-container-env-*")
	if err != nil {
		return "", func() {}, err
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	_, writeErr := file.WriteString(strings.Join(lines, "\n") + "\n")
	closeErr := file.Close()
	if writeErr != nil {
		cleanup()
		return "", func() {}, writeErr
	}
	if closeErr != nil {
		cleanup()
		return "", func() {}, closeErr
	}
	if err := os.Chmod(path, 0o600); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func createCIDFile() (string, func(), error) {
	dir, err := os.MkdirTemp("", "drydock-container-cid-*")
	if err != nil {
		return "", func() {}, err
	}
	path := filepath.Join(dir, "container.cid")
	cleanup := func() { _ = os.RemoveAll(dir) }
	return path, cleanup, nil
}

func cleanupContainer(ctx context.Context, processRunner ProcessRunner, docker string, dockerEnv []string, cidFile string) error {
	data, err := os.ReadFile(cidFile)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return err
	}
	containerID := string(bytes.TrimSpace(data))
	cleanupCtx := context.WithoutCancel(ctx)
	return processRunner.Run(cleanupCtx, ProcessRequest{
		Path:    docker,
		Args:    []string{docker, "rm", "-f", containerID},
		Env:     dockerEnv,
		Timeout: 5 * time.Second,
		Stdin:   bytes.NewReader(nil),
		Stdout:  io.Discard,
		Stderr:  io.Discard,
	})
}

func containerWorkdir(workspace pluginexec.Workspace) string {
	if workspace.ArgumentRoot == workspace.Workdir {
		return workMount
	}
	rel, err := filepath.Rel(workspace.ArgumentRoot, workspace.Workdir)
	if err != nil || rel == "." {
		return workMount
	}
	return filepath.ToSlash(filepath.Join(workMount, rel))
}

func validateContainerCommand(command []string) error {
	if len(command) == 0 {
		return fmt.Errorf("command must not be empty")
	}
	for index, token := range command {
		if strings.TrimSpace(token) == "" {
			return fmt.Errorf("command[%d] must not be empty", index)
		}
		if hasCredentialBearingURL(token) {
			return fmt.Errorf("argument contains credential-bearing URL")
		}
		if name, value, ok := strings.Cut(token, "="); ok && strings.HasPrefix(name, "--") && hasCredentialBearingURL(value) {
			return fmt.Errorf("argument contains credential-bearing URL")
		}
	}
	return nil
}

func createOfflineDockerClientConfig() (string, func(), error) {
	dir, err := os.MkdirTemp("", "drydock-docker-client-*")
	if err != nil {
		return "", func() {}, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func dockerClientEnv(lookup func(string) (string, bool), isolatedConfigDir string) []string {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	env := []string{"PATH=" + pluginexec.ControlledPath}
	if isolatedConfigDir != "" {
		env = append(env, "HOME="+isolatedConfigDir, "DOCKER_CONFIG="+isolatedConfigDir)
	}
	for _, name := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if isolatedConfigDir != "" && name == "DOCKER_CONFIG" {
			continue
		}
		if value, ok := lookup(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func rejectOfflineDockerClientRemoteConfig(lookup func(string) (string, bool)) error {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	for _, name := range []string{"DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if value, ok := lookup(name); ok && strings.TrimSpace(value) != "" {
			return fmt.Errorf("%s is not allowed in offline mode", name)
		}
	}
	if value, ok := lookup("DOCKER_HOST"); ok && strings.TrimSpace(value) != "" {
		host := strings.TrimSpace(value)
		if strings.HasPrefix(host, "unix://") || strings.HasPrefix(host, "npipe://") {
			return nil
		}
		return fmt.Errorf("DOCKER_HOST must be local unix:// or npipe:// in offline mode")
	}
	return nil
}

func resolveDocker() (string, error) {
	for _, dir := range filepath.SplitList(pluginexec.ControlledPath) {
		candidate := filepath.Join(dir, "docker")
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("docker not found on controlled PATH %q", pluginexec.ControlledPath)
}

func resolveDockerPath(path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return path, nil
	}
	return resolveDocker()
}

func makeWorkspaceWritable(workspace pluginexec.Workspace) error {
	for _, root := range []string{workspace.ArgumentRoot, workspace.Workdir} {
		if root == "" {
			continue
		}
		if err := chmodTree(root); err != nil {
			return err
		}
	}
	return nil
}

func chmodTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, info.Mode().Perm()|0o777)
		}
		if info.Mode().IsRegular() {
			return os.Chmod(path, info.Mode().Perm()|0o666)
		}
		return nil
	})
}

type defaultProcessRunner struct{}

func (defaultProcessRunner) Run(ctx context.Context, request ProcessRequest) error {
	return runProcess(ctx, request)
}

type exitError struct {
	Code int
}

func (e exitError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}

type execExitError interface {
	ExitCode() int
}

func runProcess(ctx context.Context, request ProcessRequest) error {
	runCtx := ctx
	cancel := func() {}
	if request.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, request.Timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(runCtx, request.Path, request.Args[1:]...)
	cmd.Dir = request.Dir
	cmd.Env = request.Env
	cmd.Stdin = request.Stdin
	cmd.Stdout = request.Stdout
	cmd.Stderr = request.Stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		var exitErr execExitError
		if errors.As(err, &exitErr) {
			return exitError{Code: exitErr.ExitCode()}
		}
		return err
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ctx.Err()
}

func safeCommandName(command []string) string {
	if len(command) == 0 {
		return ""
	}
	return filepath.Base(command[0])
}

type limitBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	overflow bool
}

func (b *limitBuffer) Write(data []byte) (int, error) {
	if b.limit <= 0 {
		b.overflow = true
		return len(data), nil
	}
	remaining := b.limit - int64(b.buffer.Len())
	if remaining <= 0 {
		b.overflow = true
		return len(data), nil
	}
	if int64(len(data)) > remaining {
		b.overflow = true
		_, _ = b.buffer.Write(data[:int(remaining)])
		return len(data), nil
	}
	_, _ = b.buffer.Write(data)
	return len(data), nil
}

func (b *limitBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}

func hasCredentialBearingURL(value string) bool {
	for _, scheme := range []string{"http://", "https://", "ssh://", "git://"} {
		_, rest, ok := strings.Cut(value, scheme)
		if !ok {
			continue
		}
		beforeAt, _, ok := strings.Cut(rest, "@")
		if ok && strings.Contains(beforeAt, ":") {
			return true
		}
	}
	return false
}
