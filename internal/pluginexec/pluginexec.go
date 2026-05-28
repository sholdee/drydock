package pluginexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sholdee/drydock/internal/filecopy"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/pluginpolicy"
)

const (
	ControlledPath = "/usr/local/bin:/usr/bin:/bin"

	MaxEnvValueBytes = 16 * 1024
)

type Runner interface {
	Run(ctx context.Context, request Request) (Result, error)
}

type DefaultRunner struct{}

type Request struct {
	SourceDir      string
	Config         pluginpolicy.ExecConfig
	ProtectedRoots []string
	EnvLookup      func(string) (string, bool)
}

type Result struct {
	Stdout     []byte
	Executions []Execution
}

type Execution struct {
	Phase    string
	Command  string
	Duration time.Duration
}

type Error struct {
	Phase    string
	Reason   string
	Command  string
	ExitCode *int
	Err      error
}

func (e *Error) Error() string {
	var parts []string
	if e.Phase != "" {
		parts = append(parts, e.Phase)
	}
	if e.Command != "" {
		parts = append(parts, "command "+e.Command)
	}
	if e.Reason != "" {
		parts = append(parts, e.Reason)
	}
	if e.ExitCode != nil {
		parts = append(parts, fmt.Sprintf("exit code %d", *e.ExitCode))
	}
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}
	if len(parts) == 0 {
		return "exec plugin failed"
	}
	return strings.Join(parts, ": ")
}

func (e *Error) Unwrap() error {
	return e.Err
}

func (DefaultRunner) Run(ctx context.Context, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(request.SourceDir) == "" {
		return Result{}, &Error{Reason: "source directory is required"}
	}
	workdir, cleanup, err := copySourceDir(request.SourceDir)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()

	protectedRoots := append([]string(nil), request.ProtectedRoots...)
	protectedRoots = append(protectedRoots, request.SourceDir, workdir)
	env, err := buildEnv(request.Config.Env, request.EnvLookup)
	if err != nil {
		return Result{}, err
	}
	var executions []Execution
	if request.Config.Init != nil {
		result, err := runConfiguredCommand(ctx, "init", *request.Config.Init, nil, workdir, protectedRoots, env, request.Config.Output)
		if err != nil {
			return Result{}, err
		}
		executions = append(executions, result.Execution)
	}
	result, err := runConfiguredCommand(ctx, "generate", request.Config.Generate, nil, workdir, protectedRoots, env, request.Config.Output)
	if err != nil {
		return Result{}, err
	}
	stdout := result.Stdout
	executions = append(executions, result.Execution)
	for index, command := range request.Config.PostRenderers {
		result, err := runConfiguredCommand(ctx, fmt.Sprintf("post-renderer %d", index), command, stdout, workdir, protectedRoots, env, request.Config.Output)
		if err != nil {
			return Result{}, err
		}
		stdout = result.Stdout
		executions = append(executions, result.Execution)
	}
	return Result{Stdout: stdout, Executions: executions}, nil
}

type commandResult struct {
	Stdout    []byte
	Execution Execution
}

func runConfiguredCommand(ctx context.Context, phase string, command pluginpolicy.ExecCommand, stdin []byte, workdir string, protectedRoots []string, env []string, output pluginpolicy.ExecOutput) (commandResult, error) {
	resolved, err := resolveCommand(command.Command[0], protectedRoots)
	if err != nil {
		return commandResult{}, &Error{Phase: phase, Command: safeCommandName(command.Command[0]), Reason: "invalid command", Err: err}
	}
	if err := validateArguments(command.Command[1:], workdir, protectedRoots); err != nil {
		return commandResult{}, &Error{Phase: phase, Command: safeCommandName(resolved), Reason: "invalid arguments", Err: err}
	}
	stdout := &limitBuffer{limit: output.MaxStdoutBytes}
	stderr := &limitBuffer{limit: output.MaxStderrBytes}
	started := time.Now()
	err = runProcess(ctx, processRequest{
		Path:    resolved,
		Args:    append([]string{resolved}, command.Command[1:]...),
		Dir:     workdir,
		Env:     env,
		Timeout: command.Timeout,
		Stdin:   bytes.NewReader(stdin),
		Stdout:  stdout,
		Stderr:  stderr,
	})
	execution := Execution{
		Phase:    phase,
		Command:  safeCommandName(resolved),
		Duration: time.Since(started),
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return commandResult{}, err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return commandResult{}, &Error{Phase: phase, Command: safeCommandName(resolved), Reason: "command timed out"}
		}
		var exitErr exitError
		if errors.As(err, &exitErr) {
			code := exitErr.Code
			return commandResult{}, &Error{Phase: phase, Command: safeCommandName(resolved), Reason: "command failed; stderr omitted to avoid leaking secrets", ExitCode: &code}
		}
		return commandResult{}, &Error{Phase: phase, Command: safeCommandName(resolved), Reason: "command failed; stderr omitted to avoid leaking secrets", Err: err}
	}
	if stdout.overflow {
		return commandResult{}, &Error{Phase: phase, Command: safeCommandName(resolved), Reason: "stdout limit exceeded"}
	}
	if stderr.overflow {
		return commandResult{}, &Error{Phase: phase, Command: safeCommandName(resolved), Reason: "stderr limit exceeded; stderr omitted to avoid leaking secrets"}
	}
	return commandResult{Stdout: stdout.Bytes(), Execution: execution}, nil
}

func copySourceDir(sourceDir string) (string, func(), error) {
	tempRoot, err := os.MkdirTemp("", "drydock-plugin-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tempRoot) }
	workdir := filepath.Join(tempRoot, "source")
	if err := filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(workdir, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin source path %q is a symlink", path)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, mode.Perm())
		}
		if mode.IsRegular() {
			return filecopy.CopyRegularFile(path, target, mode.Perm())
		}
		return fmt.Errorf("plugin source path %q is not a regular file or directory", path)
	}); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return workdir, cleanup, nil
}

func buildEnv(policy pluginpolicy.ExecEnv, lookup func(string) (string, bool)) ([]string, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	env := []string{"PATH=" + ControlledPath}
	for _, name := range policy.Allow {
		value, ok := lookup(name)
		if !ok {
			continue
		}
		if len(value) > MaxEnvValueBytes {
			return nil, fmt.Errorf("env value %s exceeds %d bytes", name, MaxEnvValueBytes)
		}
		env = append(env, name+"="+value)
	}
	return env, nil
}

func resolveCommand(command string, protectedRoots []string) (string, error) {
	if filepath.IsAbs(command) {
		return validateResolvedCommand(command, protectedRoots)
	}
	for _, dir := range filepath.SplitList(ControlledPath) {
		candidate := filepath.Join(dir, command)
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return validateResolvedCommand(candidate, protectedRoots)
		}
	}
	return "", fmt.Errorf("command %q not found on controlled PATH %q; install the executable or configure an absolute trusted executable path in plugin policy", command, ControlledPath)
}

func validateResolvedCommand(command string, protectedRoots []string) (string, error) {
	resolved, err := filepath.EvalSymlinks(command)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("command %q is not executable", command)
	}
	if inside, root, err := pathsafety.IsInsideAny(resolved, protectedRoots); err != nil {
		return "", err
	} else if inside {
		return "", fmt.Errorf("command %q is inside protected root %q", command, root)
	}
	return resolved, nil
}

func validateArguments(args []string, workdir string, protectedRoots []string) error {
	for _, arg := range args {
		if err := validateArgumentValue(arg, workdir, protectedRoots); err != nil {
			return err
		}
		if name, value, ok := strings.Cut(arg, "="); ok && strings.HasPrefix(name, "--") {
			if err := validateArgumentValue(value, workdir, protectedRoots); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateArgumentValue(value string, workdir string, protectedRoots []string) error {
	if value == "" {
		return nil
	}
	if hasCredentialBearingURL(value) {
		return fmt.Errorf("argument contains credential-bearing URL")
	}
	if filepath.IsAbs(value) {
		if inside, root, err := pathsafety.IsInsideAny(value, protectedRoots); err != nil {
			return err
		} else if inside {
			return fmt.Errorf("argument path %q is inside protected root %q", value, root)
		}
		return nil
	}
	if !isPathLikeArgument(value) {
		return nil
	}
	clean := filepath.Clean(value)
	if pathsafety.RelEscapes(clean) {
		return fmt.Errorf("argument path %q escapes plugin workdir", value)
	}
	target := filepath.Join(workdir, clean)
	if inside, root, err := pathsafety.IsInsideAny(target, protectedRoots); err != nil {
		return err
	} else if inside && root != workdir {
		return fmt.Errorf("argument path %q is inside protected root %q", value, root)
	}
	return nil
}

func isPathLikeArgument(value string) bool {
	return value == "." || value == ".." || strings.ContainsAny(value, `/\`)
}

func hasCredentialBearingURL(value string) bool {
	if !strings.Contains(value, "://") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return false
	}
	if parsed.User != nil {
		return true
	}
	for key := range parsed.Query() {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "credential") {
			return true
		}
	}
	return false
}

func safeCommandName(command string) string {
	base := filepath.Base(command)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "<unknown>"
	}
	return base
}

type limitBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	overflow bool
}

func (b *limitBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.overflow = true
		return len(p), nil
	}
	remaining := b.limit - int64(b.buffer.Len())
	if remaining <= 0 {
		b.overflow = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		b.overflow = true
		_, _ = b.buffer.Write(p[:int(remaining)])
		return len(p), nil
	}
	_, _ = b.buffer.Write(p)
	return len(p), nil
}
