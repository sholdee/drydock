package pluginexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
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
	SourceDir       string
	RepositoryDir   string
	SourceRelPath   string
	Config          pluginpolicy.ExecConfig
	ProtectedRoots  []string
	EnvLookup       func(string) (string, bool)
	ExtraEnv        []string
	SensitiveValues []string
}

type Result struct {
	Stdout     []byte
	Executions []Execution
}

type Workspace struct {
	Workdir        string
	ArgumentRoot   string
	ProtectedRoots []string
	Cleanup        func()
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
	workspace, err := PrepareWorkspace(request)
	if err != nil {
		return Result{}, err
	}
	defer workspace.Cleanup()

	protectedRoots := append([]string(nil), request.ProtectedRoots...)
	protectedRoots = append(protectedRoots, request.SourceDir)
	protectedRoots = append(protectedRoots, workspace.ProtectedRoots...)
	if request.RepositoryDir != "" {
		protectedRoots = append(protectedRoots, request.RepositoryDir)
	}
	env, err := BuildEnv(request.Config.Env, request.EnvLookup, request.ExtraEnv)
	if err != nil {
		return Result{}, err
	}
	redactor := newRedactor(request.SensitiveValues)
	var executions []Execution
	if request.Config.Init != nil {
		result, err := runConfiguredCommand(ctx, "init", *request.Config.Init, nil, workspace.Workdir, workspace.ArgumentRoot, protectedRoots, env, request.Config.Output, redactor)
		if err != nil {
			return Result{}, err
		}
		executions = append(executions, result.Execution)
	}
	result, err := runConfiguredCommand(ctx, "generate", request.Config.Generate, nil, workspace.Workdir, workspace.ArgumentRoot, protectedRoots, env, request.Config.Output, redactor)
	if err != nil {
		return Result{}, err
	}
	stdout := result.Stdout
	executions = append(executions, result.Execution)
	for index, command := range request.Config.PostRenderers {
		result, err := runConfiguredCommand(ctx, fmt.Sprintf("post-renderer %d", index), command, stdout, workspace.Workdir, workspace.ArgumentRoot, protectedRoots, env, request.Config.Output, redactor)
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

func runConfiguredCommand(ctx context.Context, phase string, command pluginpolicy.ExecCommand, stdin []byte, workdir string, argumentRoot string, protectedRoots []string, env []string, output pluginpolicy.ExecOutput, redactor redactor) (commandResult, error) {
	resolved, err := resolveCommand(command.Command[0], protectedRoots)
	if err != nil {
		return commandResult{}, &Error{Phase: phase, Command: safeCommandName(command.Command[0]), Reason: "invalid command", Err: err}
	}
	if err := validateArguments(command.Command[1:], workdir, argumentRoot, protectedRoots, redactor); err != nil {
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
		if exitErr, ok := errors.AsType[exitError](err); ok {
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

type workspace struct {
	workdir        string
	argumentRoot   string
	protectedRoots []string
}

func PrepareWorkspace(request Request) (Workspace, error) {
	tempRoot, err := os.MkdirTemp("", "drydock-plugin-*")
	if err != nil {
		return Workspace{}, err
	}
	cleanup := func() { _ = os.RemoveAll(tempRoot) }
	staged, err := copyWorkspaceInto(tempRoot, request)
	if err != nil {
		cleanup()
		return Workspace{}, err
	}
	return Workspace{
		Workdir:        staged.workdir,
		ArgumentRoot:   staged.argumentRoot,
		ProtectedRoots: append([]string(nil), staged.protectedRoots...),
		Cleanup:        cleanup,
	}, nil
}

func copyWorkspaceInto(tempRoot string, request Request) (workspace, error) {
	scope := request.Config.Copy.Scope
	if scope == "" {
		scope = pluginpolicy.ExecCopyScopeSource
	}
	switch scope {
	case pluginpolicy.ExecCopyScopeSource:
		if len(request.Config.Copy.Include) > 0 {
			return workspace{}, fmt.Errorf("plugin copy.include requires copy.scope %q", pluginpolicy.ExecCopyScopeRepository)
		}
		workdir := filepath.Join(tempRoot, "source")
		if err := copyTree(request.SourceDir, workdir); err != nil {
			return workspace{}, err
		}
		return workspace{
			workdir:        workdir,
			argumentRoot:   workdir,
			protectedRoots: []string{workdir},
		}, nil
	case pluginpolicy.ExecCopyScopeRepository:
		if strings.TrimSpace(request.RepositoryDir) == "" {
			return workspace{}, fmt.Errorf("repository directory is required for plugin copy.scope %q", scope)
		}
		sourceRel, err := requestSourceRelPath(request)
		if err != nil {
			return workspace{}, err
		}
		tempRepo := filepath.Join(tempRoot, "repository")
		workdir := filepath.Join(tempRepo, filepath.FromSlash(sourceRel))
		if err := copyTree(request.SourceDir, workdir); err != nil {
			return workspace{}, err
		}
		if err := copyRepositoryIncludes(request.RepositoryDir, tempRepo, request.Config.Copy.Include); err != nil {
			return workspace{}, err
		}
		return workspace{
			workdir:        workdir,
			argumentRoot:   tempRepo,
			protectedRoots: []string{tempRepo, workdir},
		}, nil
	default:
		return workspace{}, fmt.Errorf("plugin copy.scope %q is unsupported", request.Config.Copy.Scope)
	}
}

func requestSourceRelPath(request Request) (string, error) {
	if strings.TrimSpace(request.SourceRelPath) != "" {
		return cleanSlashRelPath(request.SourceRelPath, "source path", true)
	}
	rel, err := filepath.Rel(request.RepositoryDir, request.SourceDir)
	if err != nil {
		return "", err
	}
	return cleanSlashRelPath(filepath.ToSlash(rel), "source path", true)
}

func cleanSlashRelPath(raw string, label string, allowDot bool) (string, error) {
	normalized := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	clean := path.Clean(strings.Trim(normalized, "/"))
	if clean == "." {
		if allowDot {
			return ".", nil
		}
		return "", fmt.Errorf("%s must not be empty", label)
	}
	if strings.HasPrefix(normalized, "/") || hasParentComponent(normalized) || pathsafety.SlashRelEscapes(clean) || hasDotGitComponent(clean) {
		return "", fmt.Errorf("%s %q must be repository-relative and non-escaping", label, raw)
	}
	return clean, nil
}

func copyTree(sourceDir string, targetRoot string) error {
	return filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if hasDotGitComponent(filepath.ToSlash(rel)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(targetRoot, rel)
		info, err := os.Lstat(path)
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
	})
}

func copyRepositoryIncludes(repositoryDir string, tempRepo string, include []string) error {
	patterns, err := normalizeIncludePatterns(include)
	if err != nil {
		return err
	}
	if len(patterns) == 0 {
		return nil
	}
	copiedDirs := map[string]struct{}{}
	return filepath.WalkDir(repositoryDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(repositoryDir, path)
		if err != nil {
			return err
		}
		slashRel := filepath.ToSlash(rel)
		if slashRel == "." {
			return nil
		}
		return copyRepositoryIncludePath(path, rel, slashRel, entry, tempRepo, patterns, copiedDirs)
	})
}

func copyRepositoryIncludePath(
	path string,
	rel string,
	slashRel string,
	entry os.DirEntry,
	tempRepo string,
	patterns []string,
	copiedDirs map[string]struct{},
) error {
	if handled, err := handleRepositoryMetadataInclude(path, slashRel, entry, patterns); handled {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return handleRepositoryIncludeSymlink(path, slashRel, patterns)
	}
	if entry.IsDir() {
		return copyRepositoryIncludeDir(path, rel, slashRel, tempRepo, patterns, copiedDirs)
	}
	if !info.Mode().IsRegular() || !includePatternsMatch(patterns, slashRel) {
		return nil
	}
	if wasCopiedFromParentInclude(copiedDirs, slashRel) {
		return nil
	}
	return filecopy.CopyRegularFile(path, filepath.Join(tempRepo, rel), info.Mode().Perm())
}

func handleRepositoryMetadataInclude(path string, slashRel string, entry os.DirEntry, patterns []string) (bool, error) {
	if !hasDotGitComponent(slashRel) {
		return false, nil
	}
	if includePatternsTouchPath(patterns, slashRel) {
		return true, fmt.Errorf("plugin repository include path %q includes .git metadata", path)
	}
	if entry.IsDir() {
		return true, filepath.SkipDir
	}
	return true, nil
}

func handleRepositoryIncludeSymlink(path string, slashRel string, patterns []string) error {
	if includePatternsTouchPath(patterns, slashRel) {
		return fmt.Errorf("plugin repository include path %q is a symlink", path)
	}
	return nil
}

func copyRepositoryIncludeDir(
	path string,
	rel string,
	slashRel string,
	tempRepo string,
	patterns []string,
	copiedDirs map[string]struct{},
) error {
	if !includePatternsMatch(patterns, slashRel) {
		return nil
	}
	if err := copyTree(path, filepath.Join(tempRepo, rel)); err != nil {
		return err
	}
	copiedDirs[slashRel] = struct{}{}
	return filepath.SkipDir
}

func wasCopiedFromParentInclude(copiedDirs map[string]struct{}, slashRel string) bool {
	for copied := range copiedDirs {
		if slashRel == copied || strings.HasPrefix(slashRel, copied+"/") {
			return true
		}
	}
	return false
}

func normalizeIncludePatterns(include []string) ([]string, error) {
	out := make([]string, 0, len(include))
	seen := map[string]struct{}{}
	for _, raw := range include {
		if strings.Contains(raw, `\`) {
			return nil, fmt.Errorf("plugin copy.include glob %q must use slash-normalized paths", raw)
		}
		clean, err := cleanSlashRelPath(raw, "plugin copy.include glob", false)
		if err != nil {
			return nil, err
		}
		if !doublestar.ValidatePattern(clean) {
			return nil, fmt.Errorf("plugin copy.include glob %q is invalid", raw)
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out, nil
}

func includePatternsMatch(patterns []string, rel string) bool {
	for _, pattern := range patterns {
		if doublestar.MatchUnvalidated(pattern, rel) {
			return true
		}
	}
	return false
}

func includePatternsTouchPath(patterns []string, rel string) bool {
	if includePatternsMatch(patterns, rel) {
		return true
	}
	probe := rel + "/__drydock_probe__"
	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, rel+"/") || doublestar.MatchUnvalidated(pattern, probe) {
			return true
		}
	}
	return false
}

func hasDotGitComponent(rel string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(rel), "/"), ".git")
}

func hasParentComponent(rel string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(rel), "/"), "..")
}

func BuildEnv(policy pluginpolicy.ExecEnv, lookup func(string) (string, bool), extraEnv []string) ([]string, error) {
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
	for _, entry := range extraEnv {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("extra env entry is invalid")
		}
		if len(value) > MaxEnvValueBytes {
			return nil, fmt.Errorf("env value %s exceeds %d bytes", name, MaxEnvValueBytes)
		}
		env = append(env, entry)
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

func validateArguments(args []string, workdir string, argumentRoot string, protectedRoots []string, redactor redactor) error {
	for _, arg := range args {
		if err := validateArgumentValue(arg, workdir, argumentRoot, protectedRoots, redactor); err != nil {
			return err
		}
		if name, value, ok := strings.Cut(arg, "="); ok && strings.HasPrefix(name, "--") {
			if err := validateArgumentValue(value, workdir, argumentRoot, protectedRoots, redactor); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateArgumentValue(value string, workdir string, argumentRoot string, protectedRoots []string, redactor redactor) error {
	if value == "" {
		return nil
	}
	if hasCredentialBearingURL(value) {
		return fmt.Errorf("argument contains credential-bearing URL")
	}
	if filepath.IsAbs(value) {
		return validateAbsoluteArgumentPath(value, workdir, argumentRoot, protectedRoots, redactor)
	}
	if !isPathLikeArgument(value) {
		return nil
	}
	return validateRelativeArgumentPath(value, workdir, argumentRoot, protectedRoots, redactor)
}

func validateAbsoluteArgumentPath(value string, workdir string, argumentRoot string, protectedRoots []string, redactor redactor) error {
	if inside, _, err := pathsafety.IsInsideAny(value, []string{argumentRoot}); err != nil {
		return err
	} else if inside {
		return nil
	}
	if inside, root, err := pathsafety.IsInsideAny(value, protectedRoots); err != nil {
		return err
	} else if inside {
		return fmt.Errorf("argument path %q is inside protected root %q", redactor.argument(value), root)
	}
	return escapedArgumentPathError(value, workdir, argumentRoot, redactor)
}

func validateRelativeArgumentPath(value string, workdir string, argumentRoot string, protectedRoots []string, redactor redactor) error {
	clean := filepath.Clean(value)
	target := filepath.Join(workdir, clean)
	if inside, _, err := pathsafety.IsInsideAny(target, []string{argumentRoot}); err != nil {
		return err
	} else if !inside {
		return escapedArgumentPathError(value, workdir, argumentRoot, redactor)
	}
	if inside, root, err := pathsafety.IsInsideAny(target, protectedRoots); err != nil {
		return err
	} else if inside && !samePath(root, argumentRoot) && !samePath(root, workdir) {
		return fmt.Errorf("argument path %q is inside protected root %q", redactor.argument(value), root)
	}
	return nil
}

func escapedArgumentPathError(value string, workdir string, argumentRoot string, redactor redactor) error {
	if samePath(argumentRoot, workdir) {
		return fmt.Errorf("argument path %q escapes plugin workdir", redactor.argument(value))
	}
	return fmt.Errorf("argument path %q escapes plugin repository", redactor.argument(value))
}

func samePath(left string, right string) bool {
	leftAbs, err := filepath.Abs(left)
	if err != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	rightAbs, err := filepath.Abs(right)
	if err != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

type redactor struct {
	values []string
}

func newRedactor(values []string) redactor {
	out := redactor{}
	for _, value := range values {
		if value == "" {
			continue
		}
		out.values = append(out.values, value)
	}
	return out
}

func (r redactor) argument(value string) string {
	for _, sensitive := range r.values {
		if strings.Contains(value, sensitive) {
			return "<redacted>"
		}
	}
	return value
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
