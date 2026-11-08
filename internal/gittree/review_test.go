package gittree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

// TestClassifySubmoduleURLRefusesWhatGitReadsAsSSH pins that a URL that git
// reads as ssh to a host with a space, and that go-git would read as a
// local path, is refused. A .gitmodules file could otherwise make drydock
// read any repository on the machine over the file transport.
func TestClassifySubmoduleURLRefusesWhatGitReadsAsSSH(t *testing.T) {
	for _, value := range []string{
		"x y:/../../../../tmp/secret.git",
		"git@x y:/../../../../tmp/secret.git",
		"a\tb:x.git",
		"git@[::1]:x.git",
		"git+ssh://host/x.git",
		"ssh+git://host/x.git",
		"ext::sh -c touch% /tmp/pwned",
		"https:///x.git",
		"ssh://host",
		"file:///tmp/x.git",
		"/tmp/x.git",
	} {
		if endpoint, err := ClassifySubmoduleURL(value, false); err == nil {
			t.Fatalf("ClassifySubmoduleURL(%q) = %+v, want it refused", value, endpoint)
		}
	}
	for value, want := range map[string]string{
		"https://github.com/org/x.git": "https",
		"http://host:8080/x.git":       "http",
		"git://host/x.git":             "git",
		"ssh://git@host:2222/x.git":    "ssh",
		"git@github.com:org/x.git":     "ssh",
	} {
		endpoint, err := ClassifySubmoduleURL(value, false)
		if err != nil || endpoint.Protocol != want {
			t.Fatalf("ClassifySubmoduleURL(%q) = %+v, %v; want %s", value, endpoint, err, want)
		}
	}
	if endpoint, err := ClassifySubmoduleURL("/tmp/x.git", true); err != nil || endpoint.Protocol != "file" {
		t.Fatalf("ClassifySubmoduleURL(file, allowed) = %+v, %v", endpoint, err)
	}
}

// TestSubmoduleTransportReadsRemoteHelpersOnlyAfterAScheme pins that
// "<scheme>::" runs a remote helper in git, while "::" after anything else,
// such as an IPv6 address, does not.
func TestSubmoduleTransportReadsRemoteHelpersOnlyAfterAScheme(t *testing.T) {
	for value, want := range map[string]string{
		"ext::sh":         "remote-helper:ext",
		"fd::3":           "remote-helper:fd",
		"git@[::1]:x.git": "ssh",
		"[::1]:x":         "ssh",
	} {
		if got := SubmoduleTransport(value); got != want {
			t.Fatalf("SubmoduleTransport(%q) = %q, want %q", value, got, want)
		}
	}
}

// TestCheckEndpointTransportRefusesAnotherProtocol pins the check that
// runs right before a clone or fetch.
func TestCheckEndpointTransportRefusesAnotherProtocol(t *testing.T) {
	if err := CheckEndpointTransport("x y:/tmp/secret.git", "ssh"); err == nil {
		t.Fatal("CheckEndpointTransport() error = nil, want a local path refused for ssh")
	}
	if err := CheckEndpointTransport("ssh://git@host/x.git", "ssh"); err != nil {
		t.Fatalf("CheckEndpointTransport() error = %v", err)
	}
}

// TestSameCredentialScopeNeedsHostPortAndNoDowngrade pins where HTTP
// credentials of the superproject may go.
func TestSameCredentialScopeNeedsHostPortAndNoDowngrade(t *testing.T) {
	for sub, want := range map[string]bool{
		"https://git.example.test/org/sub.git":      true,
		"https://GIT.example.test:443/org/sub.git":  true,
		"https://git.example.test:8443/org/sub.git": false,
		"http://git.example.test/org/sub.git":       false,
		"https://other.example.test/org/sub.git":    false,
		"git@git.example.test:org/sub.git":          false,
	} {
		if got := SameCredentialScope("https://git.example.test/org/repo.git", sub); got != want {
			t.Fatalf("SameCredentialScope(%q) = %t, want %t", sub, got, want)
		}
	}
	if !SameCredentialScope("http://h/x.git", "http://h/y.git") {
		t.Fatal("SameCredentialScope(http, http) = false, want true")
	}
}

// TestParseModulesKeepsTheLastModuleOfAPath pins git's choice when two
// modules name one path.
func TestParseModulesKeepsTheLastModuleOfAPath(t *testing.T) {
	modules, err := ParseModules([]byte("[submodule \"a\"]\n\tpath = sub\n\turl = ../a.git\n[submodule \"b\"]\n\tpath = sub\n\turl = ../b.git\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := modules["sub"]; got.Name != "b" || got.URL != "../b.git" {
		t.Fatalf("module of sub = %+v, want the last one", got)
	}
}

// TestValidateModuleUpdateMatchesGit pins the update values git accepts.
func TestValidateModuleUpdateMatchesGit(t *testing.T) {
	for _, value := range []string{"", "checkout", "merge", "rebase", "none"} {
		if err := ValidateModuleUpdate(Module{Name: "sub", Update: value}); err != nil {
			t.Fatalf("ValidateModuleUpdate(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"!touch /tmp/x", "None", "NONE", "bogus", " none"} {
		err := ValidateModuleUpdate(Module{Name: "sub", Update: value})
		if err == nil || !strings.Contains(err.Error(), "invalid value for 'submodule.sub.update'") {
			t.Fatalf("ValidateModuleUpdate(%q) error = %v, want it refused", value, err)
		}
	}
	if ModuleSkipped(Module{Update: "None"}) || !ModuleSkipped(Module{Update: "none"}) {
		t.Fatal("ModuleSkipped must match none exactly")
	}
}

// TestCheckoutSubmodulesFailsForNestedGitlinkWithoutModules pins that
// below the top level, a gitlink without .gitmodules fails, as `git
// submodule update --recursive` fails with "No url found".
func TestCheckoutSubmodulesFailsForNestedGitlinkWithoutModules(t *testing.T) {
	child, childCommit := memoryCommit(t, nil, map[string]plumbing.Hash{"orphan": plumbing.NewHash(strings.Repeat("1", 40))})
	_, parentTree := memoryTree(t, map[string]string{".gitmodules": "[submodule \"sub\"]\n\tpath = sub\n\turl = https://h/child.git\n"}, map[string]plumbing.Hash{"sub": childCommit})
	opener := SubmoduleOpenerFunc(func(context.Context, SubmoduleRef) (*git.Repository, error) { return child, nil })
	_, err := CheckoutSubmodules(context.Background(), SubmoduleRequest{Root: t.TempDir(), Tree: parentTree, RemoteURL: "https://h/parent.git", Opener: opener})
	if err == nil || !strings.Contains(err.Error(), `no url found for submodule path "sub/orphan"`) {
		t.Fatalf("CheckoutSubmodules() error = %v, want the nested gitlink without a module refused", err)
	}
}

// TestCheckoutSubmodulesKeepsMissingContent pins KeepMissing: a submodule
// whose commit no opener has stays empty and is listed, while a
// configuration that git refuses still fails.
func TestCheckoutSubmodulesKeepsMissingContent(t *testing.T) {
	_, tree := memoryTree(t, map[string]string{".gitmodules": "[submodule \"sub\"]\n\tpath = sub\n\turl = https://h/child.git\n"}, map[string]plumbing.Hash{"sub": plumbing.NewHash(strings.Repeat("2", 40))})
	missing := SubmoduleOpenerFunc(func(context.Context, SubmoduleRef) (*git.Repository, error) { return nil, os.ErrNotExist })
	root := t.TempDir()
	result, err := CheckoutSubmodules(context.Background(), SubmoduleRequest{Root: root, Tree: tree, RemoteURL: "https://h/parent.git", Opener: missing, KeepMissing: true})
	if err != nil || len(result.Missing) != 1 || result.Missing[0].Path != "sub" {
		t.Fatalf("CheckoutSubmodules() = %+v, %v; want sub missing", result, err)
	}
	_, bad := memoryTree(t, map[string]string{".gitmodules": "[submodule \"sub\"]\n\tpath = sub\n\turl = https://h/child.git\n\tupdate = bogus\n"}, map[string]plumbing.Hash{"sub": plumbing.NewHash(strings.Repeat("2", 40))})
	if _, err := CheckoutSubmodules(context.Background(), SubmoduleRequest{Root: t.TempDir(), Tree: bad, RemoteURL: "https://h/parent.git", Opener: missing, KeepMissing: true}); err == nil {
		t.Fatal("CheckoutSubmodules(bogus update) error = nil, want it refused")
	}
}

// TestReadLFSPointerFileNeverFollowsSymlinksOrReadsLargeFiles pins the
// bounded read of a working-tree pointer.
func TestReadLFSPointerFileNeverFollowsSymlinksOrReadsLargeFiles(t *testing.T) {
	dir := t.TempDir()
	text, _ := lfsPointerText("x")
	pointerPath := filepath.Join(dir, "pointer")
	if err := os.WriteFile(pointerPath, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadLFSPointerFile(pointerPath); !ok {
		t.Fatal("ReadLFSPointerFile(pointer) = false, want true")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(pointerPath, link); err != nil {
		t.Skip(err)
	}
	if _, ok := ReadLFSPointerFile(link); ok {
		t.Fatal("ReadLFSPointerFile(symlink) = true, want the symlink not followed")
	}
	// A valid pointer padded past the size limit with extension lines, so
	// only the size limit refuses it.
	var padded strings.Builder
	padded.WriteString("version https://git-lfs.github.com/spec/v1\n")
	for i := 0; padded.Len() <= MaxLFSPointerSize; i++ {
		fmt.Fprintf(&padded, "ext-%03d-x sha256:%064d\n", i, i)
	}
	padded.WriteString(strings.TrimPrefix(text, "version https://git-lfs.github.com/spec/v1\n"))
	if _, ok := parseLFSPointerText([]byte(padded.String())); !ok || padded.Len() <= MaxLFSPointerSize {
		t.Fatal("padded pointer is not a valid pointer past the size limit")
	}
	large := filepath.Join(dir, "large")
	if err := os.WriteFile(large, []byte(padded.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadLFSPointerFile(large); ok {
		t.Fatal("ReadLFSPointerFile(large) = true, want false")
	}
}

// memoryTree builds a tree with files and gitlinks in an in-memory
// repository.
func memoryTree(t *testing.T, files map[string]string, links map[string]plumbing.Hash) (*git.Repository, *object.Tree) {
	t.Helper()
	repo, commit := memoryCommit(t, files, links)
	object, err := repo.CommitObject(commit)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := object.Tree()
	if err != nil {
		t.Fatal(err)
	}
	return repo, tree
}

// memoryCommit commits files and gitlinks at the top of a new in-memory
// repository and returns it with the commit.
func memoryCommit(t *testing.T, files map[string]string, links map[string]plumbing.Hash) (*git.Repository, plumbing.Hash) {
	t.Helper()
	repo, err := git.Init(memory.NewStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]object.TreeEntry, 0, len(files)+len(links))
	for name, body := range files {
		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		writer, _ := blob.Writer()
		_, _ = writer.Write([]byte(body))
		_ = writer.Close()
		hash, err := repo.Storer.SetEncodedObject(blob)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: hash})
	}
	for name, hash := range links {
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Submodule, Hash: hash})
	}
	sortTreeEntries(entries)
	tree := &object.Tree{Entries: entries}
	treeObject := repo.Storer.NewEncodedObject()
	if err := tree.Encode(treeObject); err != nil {
		t.Fatal(err)
	}
	treeHash, err := repo.Storer.SetEncodedObject(treeObject)
	if err != nil {
		t.Fatal(err)
	}
	commit := &object.Commit{TreeHash: treeHash, Message: "m"}
	commitObject := repo.Storer.NewEncodedObject()
	if err := commit.Encode(commitObject); err != nil {
		t.Fatal(err)
	}
	hash, err := repo.Storer.SetEncodedObject(commitObject)
	if err != nil {
		t.Fatal(err)
	}
	return repo, hash
}

func sortTreeEntries(entries []object.TreeEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].Name < entries[j-1].Name; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

// TestClassifySubmoduleURLRefusesFuzzMismatches pins cases from the review
// fuzz (review-gitsub2/fuzz-abs.txt) where git and go-git read another
// transport, user, host, port, or path, or where git refuses the URL and
// go-git does not. Each must be refused.
func TestClassifySubmoduleURLRefusesFuzzMismatches(t *testing.T) {
	for _, value := range fuzzMismatchURLs {
		if endpoint, err := ClassifySubmoduleURL(value, false); err == nil {
			t.Fatalf("ClassifySubmoduleURL(%q) = %+v, want it refused", value, endpoint)
		}
	}
}

// fuzzMismatchURLs are the review fuzz cases.
var fuzzMismatchURLs = []string{
	// Remote helpers and schemes that git matches only in lower case.
	"192.0.2.1::repo.git",
	"192.0.2.1::/repo.git",
	"HTTPS://host/org/repo.git",
	"Ssh://host/org/repo.git",
	"GIT://host/repo.git",
	// scp-like forms where go-git finds a port, or another host.
	"host:22:path",
	"host:2222:repo.git",
	"host:22::repo.git",
	"user:pass@host:repo.git",
	"user:pass@192.0.2.1:99999:x",
	"a@b@host:p",
	"git@:repo.git",
	"@höst:22:p",
	"git@host::~user/repo",
	"git@host:o~rg/repo.git",
	"user:pass@[host]/repo.git",
	"user:pass@[host]:",
	// URL forms whose authority git and go-git split differently.
	"ssh://u%40@~user:99999/org/repo",
	"ssh://@~user/repo.git",
	"ssh://user:pass@host/repo.git",
	"ssh://host:99999/repo.git",
	// Paths and hosts that git refuses.
	"host:-x",
	"192.0.2.1:-x",
	"-oProxyCommand=x@host:repo.git",
	"ssh://-oProxyCommand=x/repo.git",
	// Control characters.
	"h:\x00x",
	"https:\x00//host/org/repo.git",
}
