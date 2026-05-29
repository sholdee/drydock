package profile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strings"
	"time"
)

const DefaultOutDir = "drydock-profiles"

type Options struct {
	Mode        string
	OutDir      string
	CommandPath string
	Stderr      io.Writer
	Now         func() time.Time
}

type Session struct {
	mode       string
	path       string
	stderr     io.Writer
	stop       func() error
	stopped    bool
	successMsg string
}

func Start(options Options) (*Session, error) {
	if options.Mode == "" {
		return &Session{}, nil
	}
	mode, err := normalizeMode(options.Mode)
	if err != nil {
		return nil, err
	}
	outDir := options.OutDir
	if outDir == "" {
		outDir = DefaultOutDir
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, fmt.Errorf("create profile output directory: %w", err)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	path := filepath.Join(outDir, filename(options.CommandPath, mode, now().UTC()))
	session := &Session{
		mode:       mode,
		path:       path,
		stderr:     options.Stderr,
		successMsg: fmt.Sprintf("profile %s: wrote %s\n", mode, path),
	}

	switch mode {
	case "cpu":
		file, err := openProfileFile(path)
		if err != nil {
			return nil, err
		}
		if err := pprof.StartCPUProfile(file); err != nil {
			closeErr := file.Close()
			_ = os.Remove(path)
			return nil, errors.Join(fmt.Errorf("start cpu profile: %w", err), closeErr)
		}
		session.stop = func() error {
			pprof.StopCPUProfile()
			return file.Close()
		}
	case "mem":
		session.stop = func() error {
			runtime.GC()
			return writeRuntimeProfile(path, "heap")
		}
	case "block":
		runtime.SetBlockProfileRate(1)
		session.stop = func() error {
			defer runtime.SetBlockProfileRate(0)
			return writeRuntimeProfile(path, "block")
		}
	case "mutex":
		previous := runtime.SetMutexProfileFraction(1)
		session.stop = func() error {
			defer runtime.SetMutexProfileFraction(previous)
			return writeRuntimeProfile(path, "mutex")
		}
	case "trace":
		file, err := openProfileFile(path)
		if err != nil {
			return nil, err
		}
		if err := trace.Start(file); err != nil {
			closeErr := file.Close()
			_ = os.Remove(path)
			return nil, errors.Join(fmt.Errorf("start trace profile: %w", err), closeErr)
		}
		session.stop = func() error {
			trace.Stop()
			return file.Close()
		}
	default:
		return nil, fmt.Errorf("unsupported profile mode %q", mode)
	}

	return session, nil
}

func (session *Session) Stop() error {
	if session == nil || session.stopped {
		return nil
	}
	session.stopped = true
	if session.stop == nil {
		return nil
	}
	if err := session.stop(); err != nil {
		return fmt.Errorf("write %s profile %s: %w", session.mode, session.path, err)
	}
	if session.stderr != nil {
		_, _ = io.WriteString(session.stderr, session.successMsg)
	}
	return nil
}

func normalizeMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return "", nil
	case "cpu", "mem", "block", "mutex", "trace":
		return strings.ToLower(strings.TrimSpace(mode)), nil
	default:
		return "", fmt.Errorf("profile must be cpu, mem, block, mutex, or trace, got %q", mode)
	}
}

func filename(commandPath string, mode string, now time.Time) string {
	extension := "pprof"
	if mode == "trace" {
		extension = "out"
	}
	return fmt.Sprintf("%s-%s.%s.%s", sanitizeCommandPath(commandPath), now.Format("20060102T150405.000000000Z"), mode, extension)
}

func sanitizeCommandPath(commandPath string) string {
	fields := strings.Fields(commandPath)
	if len(fields) == 0 {
		return "drydock"
	}
	var builder strings.Builder
	lastDash := false
	for _, field := range fields {
		if builder.Len() > 0 && !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
		for _, r := range field {
			if sanitized, ok := sanitizeCommandPathRune(r); ok {
				builder.WriteRune(sanitized)
				lastDash = false
				continue
			}
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

func sanitizeCommandPathRune(r rune) (rune, bool) {
	switch {
	case r >= 'A' && r <= 'Z':
		return r + ('a' - 'A'), true
	case r >= 'a' && r <= 'z':
		return r, true
	case r >= '0' && r <= '9':
		return r, true
	case r == '.' || r == '_':
		return r, true
	default:
		return 0, false
	}
}

func openProfileFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create profile file: %w", err)
	}
	return file, nil
}

func writeRuntimeProfile(path string, name string) error {
	profile := pprof.Lookup(name)
	if profile == nil {
		return fmt.Errorf("runtime profile %q is unavailable", name)
	}
	file, err := openProfileFile(path)
	if err != nil {
		return err
	}
	writeErr := profile.WriteTo(file, 0)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}
