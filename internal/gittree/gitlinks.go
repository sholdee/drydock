package gittree

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	formatconfig "github.com/go-git/go-git/v5/plumbing/format/config"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Gitlink is a submodule entry of a Git tree: its slash path and the
// commit that the tree pins.
type Gitlink struct {
	Path   string
	Commit plumbing.Hash
}

// Links lists the gitlinks of tree, in tree order. tree.Files skips them,
// so they are read with a tree walker, which never descends into one.
func Links(tree *object.Tree) ([]Gitlink, error) {
	walker := object.NewTreeWalker(tree, true, nil)
	defer walker.Close()
	var out []Gitlink
	for {
		name, entry, err := walker.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if entry.Mode == filemode.Submodule {
			out = append(out, Gitlink{Path: name, Commit: entry.Hash})
		}
	}
}

// Module is one submodule of a .gitmodules file.
type Module struct {
	Name   string
	Path   string
	URL    string
	Update string
}

// gitmodulesFile is the name of the submodule configuration file at the top
// of a tree.
const gitmodulesFile = ".gitmodules"

// maxGitmodulesSize bounds the .gitmodules blob that drydock parses.
const maxGitmodulesSize = 1 << 20

// ReadModules reads the .gitmodules file at the top of tree. found is false
// when tree has no such regular file, as `git submodule` then finds no
// submodule (util/git/client.go:812 checks for the file). The modules are
// keyed by path. A module whose name git refuses (check_submodule_name in
// git's submodule-config.c: empty, or with a ".." component) is left out,
// as git ignores it. When two modules name one path, the last one wins, as
// in git.
func ReadModules(tree *object.Tree) (modules map[string]Module, found bool, err error) {
	entry, err := tree.FindEntry(gitmodulesFile)
	if err != nil {
		if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !entry.Mode.IsFile() || entry.Mode == filemode.Symlink {
		return nil, false, nil
	}
	file, err := tree.TreeEntryFile(entry)
	if err != nil {
		return nil, false, err
	}
	if file.Size > maxGitmodulesSize {
		return nil, true, fmt.Errorf(".gitmodules is larger than %d bytes", maxGitmodulesSize)
	}
	contents, err := file.Contents()
	if err != nil {
		return nil, true, err
	}
	modules, err = ParseModules([]byte(contents))
	return modules, true, err
}

// ParseModules parses the content of a .gitmodules file. See ReadModules.
func ParseModules(data []byte) (map[string]Module, error) {
	cfg := formatconfig.New()
	if err := formatconfig.NewDecoder(bytes.NewReader(data)).Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse .gitmodules: %w", err)
	}
	out := map[string]Module{}
	for _, sub := range cfg.Section("submodule").Subsections {
		if !validModuleName(sub.Name) {
			continue
		}
		module := Module{
			Name:   sub.Name,
			Path:   sub.Option("path"),
			URL:    sub.Option("url"),
			Update: sub.Option("update"),
		}
		// git ignores a path or URL that looks like a command-line option
		// (submodule-config.c parse_config).
		if strings.HasPrefix(module.URL, "-") {
			module.URL = ""
		}
		if module.Path == "" || strings.HasPrefix(module.Path, "-") {
			continue
		}
		// git keeps the last module for a path.
		out[module.Path] = module
	}
	return out, nil
}

// validModuleName mirrors check_submodule_name in git: a name is refused
// when it is empty or when one of its components (split at "/" or "\") is
// "..".
func validModuleName(name string) bool {
	if name == "" {
		return false
	}
	for component := range strings.FieldsFuncSeq(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if component == ".." {
			return false
		}
	}
	return true
}
