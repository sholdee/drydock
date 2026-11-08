package gittree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// MaxLFSPointerSize is the largest blob that Git LFS reads as a pointer
// (blobSizeCutoff in git-lfs).
const MaxLFSPointerSize = 1024

// LFSPointer is a Git LFS pointer file of a tree.
type LFSPointer struct {
	// Path is the slash path of the pointer file below the top of the
	// checkout.
	Path string
	// OID is the SHA-256 of the content, in lower-case hex.
	OID string
	// Size is the size of the content in bytes.
	Size int64
	// Extensions reports that the pointer names Git LFS extensions, whose
	// content drydock does not rebuild.
	Extensions bool
}

var (
	lfsOIDPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	lfsExtensionKey    = regexp.MustCompile(`^ext-[0-9]+-[a-z0-9]+$`)
	lfsPointerVersions = map[string]bool{
		"https://git-lfs.github.com/spec/v1": true,
		"https://hawser.github.com/spec/v1":  true,
	}
)

// ParseLFSPointer parses data as a Git LFS pointer file
// (docs/spec.md of git-lfs): a version line first, then key/value lines in
// key order, with an oid and a size, each line ending in a newline.
func ParseLFSPointer(data []byte) (LFSPointer, bool) {
	if len(data) > MaxLFSPointerSize {
		return LFSPointer{}, false
	}
	return parseLFSPointerText(data)
}

// parseLFSPointerText is ParseLFSPointer without the size limit.
func parseLFSPointerText(data []byte) (LFSPointer, bool) {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return LFSPointer{}, false
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) < 3 {
		return LFSPointer{}, false
	}
	version, ok := strings.CutPrefix(lines[0], "version ")
	if !ok || !lfsPointerVersions[version] {
		return LFSPointer{}, false
	}
	var parsed lfsPointerFields
	previous := ""
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, " ")
		if !ok || key <= previous || !parsed.add(key, value) {
			return LFSPointer{}, false
		}
		previous = key
	}
	return parsed.pointer, parsed.haveOID && parsed.haveSize
}

// lfsPointerFields collects the key/value lines of a pointer.
type lfsPointerFields struct {
	pointer  LFSPointer
	haveOID  bool
	haveSize bool
}

// add records one line and reports whether the pointer stays valid.
func (f *lfsPointerFields) add(key, value string) bool {
	switch {
	case key == "oid":
		if !lfsOIDPattern.MatchString(value) {
			return false
		}
		f.pointer.OID, f.haveOID = strings.TrimPrefix(value, "sha256:"), true
	case key == "size":
		size, err := strconv.ParseInt(value, 10, 64)
		if err != nil || size < 0 {
			return false
		}
		f.pointer.Size, f.haveSize = size, true
	case lfsExtensionKey.MatchString(key):
		f.pointer.Extensions = true
	default:
		return false
	}
	return true
}

// ReadLFSPointerFile reads the file at path as a Git LFS pointer. It never
// follows a symlink and reads at most MaxLFSPointerSize+1 bytes: a file
// that is not regular or is larger than a pointer is no pointer.
func ReadLFSPointerFile(path string) (LFSPointer, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxLFSPointerSize {
		return LFSPointer{}, false
	}
	in, err := os.OpenFile(path, os.O_RDONLY|openNoFollow, 0)
	if err != nil {
		return LFSPointer{}, false
	}
	defer func() { _ = in.Close() }()
	data, err := io.ReadAll(io.LimitReader(in, MaxLFSPointerSize+1))
	if err != nil {
		return LFSPointer{}, false
	}
	return ParseLFSPointer(data)
}

// ScanLFSPointers lists the Git LFS pointer files of tree, as `git lfs
// ls-files` finds them in the Argo CD repo-server: regular files whose
// content is a pointer. It never looks into a gitlink, since Argo CD runs
// `git lfs checkout` in the superproject only.
func ScanLFSPointers(ctx context.Context, tree *object.Tree) ([]LFSPointer, error) {
	var out []LFSPointer
	err := tree.Files().ForEach(func(file *object.File) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file.Mode == filemode.Symlink || file.Size > MaxLFSPointerSize {
			return nil
		}
		contents, err := file.Contents()
		if err != nil {
			return err
		}
		if pointer, ok := ParseLFSPointer([]byte(contents)); ok {
			pointer.Path = file.Name
			out = append(out, pointer)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// LFSObjectPath returns the path of the object of oid in the Git LFS store
// below lfsDir (a .git/lfs directory): objects/<oid[0:2]>/<oid[2:4]>/<oid>.
func LFSObjectPath(lfsDir, oid string) string {
	return filepath.Join(lfsDir, "objects", oid[0:2], oid[2:4], oid)
}

// ErrLFSObjectMissing reports that no Git LFS store holds the object of a
// pointer.
var ErrLFSObjectMissing = errors.New("git LFS object not found")

// ReplaceLFSPointer writes, at the pointer's path below root, the content
// that the first of lfsDirs holds for it, as `git lfs checkout` does. The
// content must match the pointer's size and SHA-256. It never writes
// through a symlink, and it fails with ErrLFSObjectMissing when no store
// holds a matching object.
func ReplaceLFSPointer(root string, pointer LFSPointer, lfsDirs []string) error {
	if pointer.Extensions {
		return fmt.Errorf("%s: pointer uses Git LFS extensions: %w", pointer.Path, ErrLFSObjectMissing)
	}
	if !ValidRelPath(pointer.Path) {
		return fmt.Errorf("unsafe Git LFS pointer path %q", pointer.Path)
	}
	for _, dir := range lfsDirs {
		ok, err := replaceFromStore(root, pointer, dir)
		if err != nil || ok {
			return err
		}
	}
	return fmt.Errorf("%s: %w", pointer.Path, ErrLFSObjectMissing)
}

func replaceFromStore(root string, pointer LFSPointer, lfsDir string) (bool, error) {
	object := LFSObjectPath(lfsDir, pointer.OID)
	info, err := os.Lstat(object)
	if err != nil || !info.Mode().IsRegular() || info.Size() != pointer.Size {
		return false, nil
	}
	if !lfsObjectMatches(object, pointer.OID) {
		return false, nil
	}
	in, err := os.Open(object)
	if err != nil {
		return false, nil
	}
	defer func() { _ = in.Close() }()
	target := filepath.Join(root, filepath.FromSlash(pointer.Path))
	if err := validateWriteParent(root, filepath.Dir(target)); err != nil {
		return false, err
	}
	mode := os.FileMode(0o644)
	if current, err := os.Lstat(target); err == nil {
		if !current.Mode().IsRegular() {
			return false, fmt.Errorf("git LFS pointer %q is not a regular file", pointer.Path)
		}
		mode = current.Mode().Perm()
	}
	return true, WriteContent(target, in, mode)
}

func lfsObjectMatches(object, oid string) bool {
	in, err := os.Open(object)
	if err != nil {
		return false
	}
	defer func() { _ = in.Close() }()
	sum := sha256.New()
	if _, err := io.Copy(sum, in); err != nil {
		return false
	}
	return hex.EncodeToString(sum.Sum(nil)) == oid
}
