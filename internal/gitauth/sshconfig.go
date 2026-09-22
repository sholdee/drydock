package gitauth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/kevinburke/ssh_config"
)

// sshConfigFiles parses the user and system ssh_config files on first use and
// answers lookups from the parsed result.
//
// Only ssh_config.Decode and (*Config).GetAll are used. The package-level
// ssh_config.Get/GetAll helpers resolve the home directory through os/user
// instead of $HOME, cache behind a package sync.Once, synthesize a default
// identity when no Host matches, and swallow parse errors.
type sshConfigFiles struct {
	mu     sync.Mutex
	files  []labeledFile
	parsed map[string]*ssh_config.Config
	errs   map[string]error
	loaded bool
}

type labeledFile struct {
	path  string
	label string
}

func fileSSHConfig(homeDir, systemPath string) func(host, key string) ([]string, error) {
	files := &sshConfigFiles{parsed: map[string]*ssh_config.Config{}, errs: map[string]error{}}
	if homeDir != "" {
		files.files = append(files.files, labeledFile{path: filepath.Join(homeDir, ".ssh", "config"), label: userSSHConfigLabel})
	}
	if systemPath != "" {
		files.files = append(files.files, labeledFile{path: systemPath, label: systemPath})
	}
	return files.getAll
}

// getAll returns every value configured for host and key, user file first. A
// file that cannot be parsed or queried is reported through the error while the
// remaining files still contribute their values.
func (s *sshConfigFiles) getAll(host, key string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()

	var values []string
	var firstErr error
	for _, file := range s.files {
		if err := s.errs[file.path]; err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		config := s.parsed[file.path]
		if config == nil {
			continue
		}
		found, err := configGetAll(config, host, key)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", file.label, err)
			}
			continue
		}
		values = append(values, found...)
	}
	return values, firstErr
}

func (s *sshConfigFiles) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	for _, file := range s.files {
		config, found, err := decodeSSHConfigFile(file.path)
		switch {
		case err != nil:
			s.errs[file.path] = fmt.Errorf("%s: %w", file.label, err)
		case found:
			s.parsed[file.path] = config
		}
	}
}

// decodeSSHConfigFile reports found == false when the file does not exist.
func decodeSSHConfigFile(path string) (*ssh_config.Config, bool, error) {
	handle, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, errors.New(fileErrorReason(err))
	}
	defer func() { _ = handle.Close() }()
	config, err := ssh_config.Decode(handle)
	if err != nil {
		return nil, false, err
	}
	return config, true, nil
}

// configGetAll converts the panic ssh_config v1.2.0 raises for the Match
// directives it cannot evaluate into an error, so one unsupported stanza does
// not take the process down.
func configGetAll(config *ssh_config.Config, host, key string) (values []string, err error) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		if runtimeErr, ok := recovered.(runtime.Error); ok {
			panic(runtimeErr)
		}
		values = nil
		err = fmt.Errorf("%v", recovered)
	}()
	return config.GetAll(host, key)
}
