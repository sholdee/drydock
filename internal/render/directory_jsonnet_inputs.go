package render

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/go-jsonnet"
	"github.com/google/go-jsonnet/ast"
	"github.com/google/go-jsonnet/toolutils"
	"github.com/sholdee/drydock/internal/pathsafety"
)

// DirectoryJsonnetImportPaths returns the repository paths outside the
// source path and its Jsonnet libraries that the Jsonnet files of a
// directory source import, directly or through other imports. Jsonnet
// import paths are string literals, so drydock reads them from the parsed
// files instead of evaluating them. For each import, the result holds every
// path the importer tries before the one it finds (a new file at an earlier
// path would win), the path it finds, and the symlinks on the way to that
// path's target. It reads every .jsonnet file under the source path, also
// one that recurse or a filter leaves out, so the result is conservative.
// An import that the renderer refuses, a file that does not parse, and
// imports in extVar or TLA code add nothing. It reads a file only after the
// checks of the renderer's importer: its symlinks must resolve to a regular
// file inside the repository, outside .git. It reads at most
// maxJsonnetImportScanBytes of a file, and does not parse a larger one. The
// result is sorted, with forward slashes.
func DirectoryJsonnetImportPaths(ctx context.Context, repoRoot, sourcePath string, libs []string) ([]string, error) {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	source := ResolvedSource{RepoRoot: absRoot, Path: sourcePath}
	appPath, err := sourceRoot(source)
	if err != nil {
		return nil, err
	}
	jpaths := []string{appPath}
	covered := []string{filepath.Clean(sourcePath)}
	for _, lib := range libs {
		resolved, err := resolveJsonnetLib(absRoot, lib)
		if err != nil {
			return nil, err
		}
		jpaths = append(jpaths, resolved)
		rel, err := filepath.Rel(absRoot, resolved)
		if err != nil {
			return nil, err
		}
		covered = append(covered, rel)
	}
	entries, err := directoryJsonnetEntryFiles(appPath)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, err
	}
	collector := jsonnetImportCollector{
		ctx:      ctx,
		importer: newBoundedJsonnetImporter(absRoot, jpaths),
		absRoot:  absRoot,
		realRoot: realRoot,
		covered:  covered,
		found:    map[string]struct{}{},
		visited:  map[string]bool{},
	}
	for _, entry := range entries {
		collector.visit(entry)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(collector.found))
	for rel := range collector.found {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

// directoryJsonnetEntryFiles returns every .jsonnet file under appPath, as
// the directory walk names them, symlinks included.
func directoryJsonnetEntryFiles(appPath string) ([]string, error) {
	var entries []string
	err := filepath.WalkDir(appPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != appPath && (entry.Name() == drydockCacheMetadataDirName || strings.EqualFold(entry.Name(), ".git")) {
				return filepath.SkipDir
			}
			return nil
		}
		if isJsonnetFile(path) {
			entries = append(entries, path)
		}
		return nil
	})
	return entries, err
}

// maxJsonnetImportScanBytes bounds how much of one Jsonnet file the import
// scan reads. It is the default repo-server limit on the combined size of a
// directory source, which no single manifest file can pass.
const maxJsonnetImportScanBytes = 10_000_000

type jsonnetImportCollector struct {
	ctx      context.Context
	importer *boundedJsonnetImporter
	absRoot  string
	realRoot string
	covered  []string
	found    map[string]struct{}
	visited  map[string]bool
}

// visit parses the Jsonnet file at path, the path the importer found it
// at, and follows its imports. A relative import resolves against the
// directory of that path, also when it is a symlink.
func (c *jsonnetImportCollector) visit(path string) {
	if c.visited[path] || c.ctx.Err() != nil {
		return
	}
	c.visited[path] = true
	data, ok := c.read(path)
	if !ok {
		return
	}
	node, err := jsonnet.SnippetToAST(path, string(data))
	if err != nil {
		return
	}
	for _, imported := range jsonnetImportLiterals(node) {
		candidates, err := c.importer.importCandidates(path, imported.path)
		if err != nil {
			continue
		}
		for _, candidate := range candidates {
			c.add(candidate)
			if _, err := os.Lstat(candidate); err != nil {
				continue
			}
			visited, final := traceSymlinkPaths(candidate)
			for _, item := range visited {
				c.addReal(item)
			}
			if final != "" {
				c.addReal(final)
			}
			if imported.code {
				c.visit(candidate)
			}
			break
		}
	}
}

// read reads the Jsonnet file at path after the checks of the renderer's
// importer (boundedJsonnetImporter.validate), so it never reads outside the
// repository, from .git, or from a file that is not a regular file, such
// as a device that never ends. It does not read a file larger than
// maxJsonnetImportScanBytes.
func (c *jsonnetImportCollector) read(path string) ([]byte, bool) {
	if pathEntersGit(c.importer.boundary, path) {
		return nil, false
	}
	resolved, err := c.importer.validate(path, path)
	if err != nil {
		return nil, false
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, false
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxJsonnetImportScanBytes+1))
	if err != nil || len(data) > maxJsonnetImportScanBytes {
		return nil, false
	}
	return data, true
}

func (c *jsonnetImportCollector) add(path string) {
	rel, err := filepath.Rel(c.absRoot, path)
	if err != nil {
		return
	}
	c.addRel(rel)
}

func (c *jsonnetImportCollector) addReal(path string) {
	rel, err := filepath.Rel(c.realRoot, path)
	if err != nil {
		return
	}
	c.addRel(rel)
}

func (c *jsonnetImportCollector) addRel(rel string) {
	if rel == "." || filepath.IsAbs(rel) || pathsafety.RelEscapes(rel) || relUnderAny(rel, c.covered) {
		return
	}
	c.found[filepath.ToSlash(rel)] = struct{}{}
}

type jsonnetImportLiteral struct {
	path string
	// code is true for import, whose file is Jsonnet with imports of its
	// own, and false for importstr and importbin.
	code bool
}

// jsonnetImportLiterals returns the import, importstr, and importbin paths
// of a parsed Jsonnet file, in source order.
func jsonnetImportLiterals(node ast.Node) []jsonnetImportLiteral {
	var out []jsonnetImportLiteral
	var walk func(ast.Node)
	walk = func(node ast.Node) {
		if node == nil {
			return
		}
		switch typed := node.(type) {
		case *ast.Import:
			if typed.File != nil {
				out = append(out, jsonnetImportLiteral{path: typed.File.Value, code: true})
			}
		case *ast.ImportStr:
			if typed.File != nil {
				out = append(out, jsonnetImportLiteral{path: typed.File.Value})
			}
		case *ast.ImportBin:
			if typed.File != nil {
				out = append(out, jsonnetImportLiteral{path: typed.File.Value})
			}
		}
		for _, child := range toolutils.Children(node) {
			walk(child)
		}
	}
	walk(node)
	return out
}
