package render

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/google/go-jsonnet"
	"github.com/google/go-jsonnet/ast"
	"github.com/google/go-jsonnet/toolutils"
	"github.com/sholdee/drydock/internal/pathsafety"
)

// DirectoryJsonnetImportPaths returns the repository paths outside the
// source path that the Jsonnet files of a directory source import, directly
// or through other imports. Jsonnet import paths are string literals, so
// drydock reads them from the parsed files instead of evaluating them. For each import, the result holds every
// path the importer tries before the one it finds (a new file at an earlier
// path would win), the path it finds, and the symlinks on the way to that
// path's target. It reads every .jsonnet file under the source path, also
// one that recurse or a filter leaves out, so the result is conservative.
// It also follows the imports in code, the extVar and TLA values that the
// renderer evaluates as Jsonnet code (JsonnetCodeValues). Such a value has
// no file, so its relative imports resolve against the source path and the
// libraries only (visitCode). An import that the renderer refuses, and a file or a code
// value that does not parse, add nothing. The scan has a budget of files
// and bytes; when it runs out, the result is ErrJsonnetImportScanBudget. It
// reads a file only after the
// checks of the renderer's importer: its symlinks must resolve to a regular
// file inside the repository, outside .git. It reads at most
// maxJsonnetImportScanBytes of a file, and does not parse a larger one. The
// result is sorted, with forward slashes.
func DirectoryJsonnetImportPaths(ctx context.Context, repoRoot, sourcePath string, libs, code []string) ([]string, error) {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	source := ResolvedSource{RepoRoot: absRoot, Path: sourcePath}
	appPath, err := sourceRoot(source)
	if err != nil {
		return nil, err
	}
	entries, err := directoryJsonnetEntryFiles(appPath)
	if err != nil {
		return nil, err
	}
	// A source without Jsonnet files never resolves its libraries, so a
	// library that does not resolve is no scan error.
	if len(entries) == 0 {
		return nil, nil
	}
	jpaths := []string{appPath}
	for _, lib := range libs {
		resolved, err := resolveJsonnetLib(absRoot, lib)
		if err != nil {
			return nil, err
		}
		jpaths = append(jpaths, resolved)
	}
	// Only the source path is left out of the result. Files found in a
	// library are kept: changed-only selection owns a library through the
	// files it serves, not as a whole directory.
	covered := []string{filepath.Clean(sourcePath)}
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
	for _, value := range code {
		collector.visitCode(value)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if collector.exhausted {
		return nil, fmt.Errorf("%w: source %q imports more than %d files or %d bytes through its import paths", ErrJsonnetImportScanBudget, sourcePath, maxJsonnetImportScanFiles, maxJsonnetImportScanBytes)
	}
	out := make([]string, 0, len(collector.found))
	for rel := range collector.found {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

// DirectoryJsonnetLibInputs returns what the Jsonnet libraries of a
// directory source add to its inputs, when the source has a Jsonnet file.
// Without one, the renderer never resolves the libraries, so it returns
// nothing. dirs are the libraries as the renderer resolves them
// (resolveJsonnetLib), "." for the repository root; a library that does not
// resolve is left out. links are the paths that decide that resolution: each
// library path as written, joined to the root, when it is a symlink or
// missing, and every symlink on the way to its target. A change to one of them can move the library, so it is an
// input even when no imported file changed. All paths are relative to the
// repository root, with forward slashes.
func DirectoryJsonnetLibInputs(repoRoot, sourcePath string, libs []string) (dirs, links []string, err error) {
	if len(libs) == 0 {
		return nil, nil, nil
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, nil, err
	}
	appPath, err := sourceRoot(ResolvedSource{RepoRoot: absRoot, Path: sourcePath})
	if err != nil {
		return nil, nil, err
	}
	entries, err := directoryJsonnetEntryFiles(appPath)
	if err != nil || len(entries) == 0 {
		return nil, nil, err
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, nil, err
	}
	for _, lib := range libs {
		links = appendJsonnetLibLinks(links, absRoot, realRoot, lib)
		resolved, err := resolveJsonnetLib(absRoot, lib)
		if err != nil {
			continue
		}
		if rel, ok := repoRelative(absRoot, resolved); ok {
			dirs = appendUnique(dirs, rel)
		}
	}
	return dirs, links, nil
}

// appendJsonnetLibLinks adds the library path as written, joined to the
// root, when it is a symlink or missing, and every symlink on the way to
// its target.
func appendJsonnetLibLinks(links []string, absRoot, realRoot, lib string) []string {
	joined := filepath.Join(absRoot, filepath.FromSlash(lib))
	// The path as written counts only when it is a symlink or missing.
	// Changed-only ownership is a prefix match, so a real directory would
	// own every file below it, also files no Jsonnet file imports. The
	// import scan already owns the files a library serves.
	if info, err := os.Lstat(joined); err != nil || info.Mode()&os.ModeSymlink != 0 {
		if rel, ok := repoRelative(absRoot, joined); ok && rel != "." && !pathsafety.RelEntersGit(filepath.FromSlash(rel)) {
			links = appendUnique(links, rel)
		}
	}
	visited, _ := traceSymlinkPaths(joined)
	for _, hop := range visited {
		if rel, ok := repoRelative(realRoot, hop); ok && rel != "." && !pathsafety.RelEntersGit(filepath.FromSlash(rel)) {
			links = appendUnique(links, rel)
		}
	}
	return links
}

// DirectoryHasJsonnetFile reports whether the directory source at
// sourcePath has a .jsonnet file, so that the renderer evaluates Jsonnet.
// It reports true when the walk fails: then the inputs are not known.
func DirectoryHasJsonnetFile(repoRoot, sourcePath string) bool {
	appPath, err := sourceRoot(ResolvedSource{RepoRoot: repoRoot, Path: sourcePath})
	if err != nil {
		return true
	}
	entries, err := directoryJsonnetEntryFiles(appPath)
	return err != nil || len(entries) > 0
}

// repoRelative returns path relative to root with forward slashes, and
// reports false when path is outside root.
func repoRelative(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) || pathsafety.RelEscapes(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
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
// scan reads, and how much it reads in total. It is the default repo-server
// limit on the combined size of a directory source.
const maxJsonnetImportScanBytes = 10_000_000

// maxJsonnetImportScanFiles bounds how many files one scan parses. The scan
// keys a file by the path it was imported at, because a ".." in an import
// path is lexical, so a symlink to a directory above it can give one file
// endlessly many paths. Two such symlinks double the paths at each level.
const maxJsonnetImportScanFiles = 1000

// ErrJsonnetImportScanBudget reports that a Jsonnet import scan stopped at
// its budget (maxJsonnetImportScanFiles or maxJsonnetImportScanBytes), so
// its result is not complete. A caller must not trust a cache key or a
// changed-only selection built from it.
var ErrJsonnetImportScanBudget = errors.New("jsonnet import scan budget exhausted")

type jsonnetImportCollector struct {
	ctx      context.Context
	importer *boundedJsonnetImporter
	absRoot  string
	realRoot string
	covered  []string
	found    map[string]struct{}
	visited  map[string]bool
	// parsed and bytesRead count against the scan budget; exhausted is set
	// once either runs out, and stops the scan.
	parsed    int
	bytesRead int64
	exhausted bool
}

// visit parses the Jsonnet file at path, the path the importer found it
// at, and follows its imports. A relative import resolves against the
// directory of that path, also when it is a symlink.
func (c *jsonnetImportCollector) visit(path string) {
	if c.exhausted || c.visited[path] || c.ctx.Err() != nil {
		return
	}
	c.visited[path] = true
	if c.parsed >= maxJsonnetImportScanFiles {
		c.exhausted = true
		return
	}
	c.parsed++
	data, ok := c.read(path)
	if !ok {
		return
	}
	node, err := jsonnet.SnippetToAST(path, string(data))
	if err != nil {
		return
	}
	c.follow(path, node)
}

// visitCode parses one code value of an extVar or TLA and follows its
// imports. go-jsonnet parses such a value with an empty file name
// (codeToPV, imports.go:185-196 at go-jsonnet v0.22.0), and an import
// passes the file name of its node to the importer (interpreter.go:510-520).
// So the importer gets an empty importedFrom. The Argo CD repo-server's
// confinedImporter then tries the import path against its own working
// directory, and then against the Jsonnet paths in reverse order: the
// libraries, last first, and then the source path
// (reposerver/repository/repository.go:2305-2313 and 2339-2378 at v3.6.1).
// drydock's importer tries the Jsonnet paths only (importCandidates), and
// the scan uses that importer. A value that does not parse adds nothing:
// go-jsonnet fails only a render that reads that variable.
func (c *jsonnetImportCollector) visitCode(code string) {
	if c.exhausted || c.ctx.Err() != nil {
		return
	}
	node, err := jsonnet.SnippetToAST("", code)
	if err != nil {
		return
	}
	c.follow("", node)
}

// follow adds the paths that the imports of node, parsed from the file at
// from (empty for a code value), try and find, and visits each Jsonnet file
// that an import finds.
func (c *jsonnetImportCollector) follow(from string, node ast.Node) {
	for _, imported := range jsonnetImportLiterals(node) {
		candidates, err := c.importer.importCandidates(from, imported.path)
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
	if err != nil {
		return nil, false
	}
	if len(data) > maxJsonnetImportScanBytes {
		// The renderer evaluates such a file, so its imports are inputs
		// that the scan cannot list.
		c.exhausted = true
		return nil, false
	}
	c.bytesRead += int64(len(data))
	if c.bytesRead > maxJsonnetImportScanBytes {
		c.exhausted = true
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

// JsonnetCodeImportLiterals returns the import, importstr, and importbin
// paths of one Jsonnet code value, as written, in source order. A value
// that does not parse has none.
func JsonnetCodeImportLiterals(code string) []string {
	node, err := jsonnet.SnippetToAST("", code)
	if err != nil {
		return nil
	}
	literals := jsonnetImportLiterals(node)
	out := make([]string, 0, len(literals))
	for _, literal := range literals {
		out = append(out, literal.path)
	}
	return out
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
