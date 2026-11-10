package gitlfs

import (
	"strings"
	"testing"
)

// TestFindEndpointFollowsGitLFS pins the endpoint rules of git-lfs 3.8.1
// for the remote "origin", as `git lfs env` prints them: lfs.url, then
// remote.origin.lfsurl, from .lfsconfig, then the repository URL with
// .git/info/lfs. A git:// URL takes lfs.gitprotocol (https by default) and
// keeps its port; an SSH URL gives an SSH remote whose https URL drops the
// port.
func TestFindEndpointFollowsGitLFS(t *testing.T) {
	for _, tc := range []struct {
		name, repoURL, lfsconfig, want string
		ssh                            bool
	}{
		{"https with .git", "https://git.example.test/org/repo.git", "", "https://git.example.test/org/repo.git/info/lfs", false},
		{"https without .git", "https://git.example.test/org/repo", "", "https://git.example.test/org/repo.git/info/lfs", false},
		{"trailing slash", "https://git.example.test/org/repo/", "", "https://git.example.test/org/repo.git/info/lfs", false},
		{"port", "http://127.0.0.1:8080/repo.git", "", "http://127.0.0.1:8080/repo.git/info/lfs", false},
		{"git scheme", "git://git.example.test/org/repo.git", "", "https://git.example.test/org/repo.git/info/lfs", false},
		{"git scheme with gitprotocol", "git://git.example.test/org/repo.git", "[lfs]\n\tgitprotocol = http\n", "http://git.example.test/org/repo.git/info/lfs", false},
		{"lfs.url", "https://git.example.test/org/repo.git", "[lfs]\n\turl = https://lfs.example.test/api/\n", "https://lfs.example.test/api", false},
		{"lfs.url upper-case scheme", "https://git.example.test/org/repo.git", "[lfs]\n\turl = HTTPS://lfs.example.test/api\n", "https://lfs.example.test/api", false},
		{"git lfs.url keeps the port", "https://git.example.test/r.git", "[lfs]\n\turl = git://lfs.example.test:9418/x\n", "https://lfs.example.test:9418/x", false},
		{"git lfs.url with gitprotocol", "https://git.example.test/r.git", "[lfs]\n\turl = git://lfs.example.test/x\n\tgitprotocol = http\n", "http://lfs.example.test/x", false},
		{"lfs.url last wins", "https://git.example.test/org/repo.git", "[lfs]\n\turl = https://a.example.test\n\turl = https://b.example.test\n", "https://b.example.test", false},
		{"remote lfsurl", "https://git.example.test/org/repo.git", "[remote \"origin\"]\n\tlfsurl = https://lfs.example.test/r\n", "https://lfs.example.test/r", false},
		{"lfs.url over lfsurl", "https://git.example.test/org/repo.git", "[remote \"origin\"]\n\tlfsurl = https://b.example.test\n[lfs]\n\turl = https://a.example.test\n", "https://a.example.test", false},
		{"other remote ignored", "https://git.example.test/org/repo.git", "[remote \"fork\"]\n\tlfsurl = https://b.example.test\n", "https://git.example.test/org/repo.git/info/lfs", false},
		{"include not followed", "https://git.example.test/org/repo.git", "[include]\n\tpath = other\n", "https://git.example.test/org/repo.git/info/lfs", false},
		{"scp remote", "git@git.example.test:org/repo.git", "", "https://git.example.test/org/repo.git/info/lfs", true},
		{"ssh remote drops the port", "ssh://git@git.example.test:2222/org/repo", "", "https://git.example.test/org/repo.git/info/lfs", true},
		{"ssh lfs.url", "https://git.example.test/r.git", "[lfs]\n\turl = ssh://git@lfs.example.test/x\n", "https://lfs.example.test/x", true},
		{"https lfs.url for an ssh remote", "git@git.example.test:org/repo.git", "[lfs]\n\turl = https://lfs.example.test\n", "https://lfs.example.test", false},
	} {
		got, err := FindEndpoint(tc.repoURL, []byte(tc.lfsconfig))
		if err != nil || got.URL != tc.want || (got.SSH != nil) != tc.ssh {
			t.Errorf("%s: FindEndpoint() = %+v, %v; want %q, SSH %t", tc.name, got, err, tc.want, tc.ssh)
		}
	}
}

// TestFindEndpointSSHPathAsGitLFSPassesIt pins the path that drydock
// passes to git-lfs-authenticate: as written after the colon of the scp
// form, and with its leading "/" for an ssh URL, as git-lfs 3.8.1 does.
func TestFindEndpointSSHPathAsGitLFSPassesIt(t *testing.T) {
	for repoURL, want := range map[string]SSHRemote{
		"git@git.example.test:org/repo.git":         {User: "git", Host: "git.example.test", Port: 22, Path: "org/repo.git"},
		"git@git.example.test:org/repo/":            {User: "git", Host: "git.example.test", Port: 22, Path: "org/repo/"},
		"ssh://git@git.example.test:2222/org/r.git": {User: "git", Host: "git.example.test", Port: 2222, Path: "/org/r.git"},
		"ssh://git.example.test/~user/repo":         {User: "", Host: "git.example.test", Port: 22, Path: "/~user/repo"},
	} {
		got, err := FindEndpoint(repoURL, nil)
		if err != nil || got.SSH == nil {
			t.Fatalf("FindEndpoint(%q) = %+v, %v", repoURL, got, err)
		}
		want.URL = repoURL
		if want.User == "" {
			want.User = got.SSH.User
		}
		if *got.SSH != want {
			t.Errorf("FindEndpoint(%q).SSH = %+v, want %+v", repoURL, *got.SSH, want)
		}
	}
	if quoted := shellQuote("/~user/it's"); quoted != `'/~user/it'\''s'` {
		t.Errorf("shellQuote() = %s", quoted)
	}
}

// TestFindEndpointRefusesWhatDrydockCannotUse pins the endpoints that
// drydock refuses as unsupported, and those that git-lfs cannot use
// either, which fail: an empty lfs.url or remote.origin.lfsurl, also next
// to the other key, and a .lfsconfig file that is not valid. No error
// quotes the URL, and an error names the key that holds the value.
func TestFindEndpointRefusesWhatDrydockCannotUse(t *testing.T) {
	for _, tc := range []struct {
		name, repoURL, lfsconfig string
		unsupported              bool
		mention                  string
	}{
		{"git+ssh", "git+ssh://git.example.test/org/repo.git", "", true, ""},
		{"file", "file:///srv/repo.git", "", true, ""},
		{"local path", "/srv/repo.git", "", true, ""},
		{"file lfs.url", "https://git.example.test/r.git", "[lfs]\n\turl = file:///etc\n", true, "local Git LFS store"},
		{"bare path lfs.url", "https://git.example.test/r.git", "[lfs]\n\turl = /srv/lfs\n", true, "local Git LFS store"},
		{"userinfo", "https://git.example.test/r.git", "[lfs]\n\turl = https://user:hunter2@lfs.example.test\n", true, "lfs.url"},
		{"query", "https://git.example.test/r.git", "[lfs]\n\turl = https://lfs.example.test/?token=hunter2\n", true, ""},
		{"lfsurl label", "https://git.example.test/r.git", "[remote \"origin\"]\n\tlfsurl = https://user:hunter2@lfs.example.test\n", true, "remote.origin.lfsurl"},
		{"bad gitprotocol", "git://git.example.test/r.git", "[lfs]\n\tgitprotocol = ftp\n", true, ""},
		{"huge config", "https://git.example.test/r.git", "[lfs]\n#" + strings.Repeat("x", MaxLFSConfigSize), true, ""},
		{"empty lfs.url", "https://git.example.test/r.git", "[lfs]\n\turl =\n[remote \"origin\"]\n\tlfsurl = https://b.example.test\n", false, "lfs.url"},
		{"empty lfsurl", "https://git.example.test/r.git", "[remote \"origin\"]\n\tlfsurl =\n", false, "remote.origin.lfsurl"},
		{"bad config", "https://git.example.test/r.git", "[lfs\n", false, ""},
	} {
		got, err := FindEndpoint(tc.repoURL, []byte(tc.lfsconfig))
		if err == nil {
			t.Errorf("%s: FindEndpoint() = %+v, want an error", tc.name, got)
			continue
		}
		if IsUnsupported(err) != tc.unsupported {
			t.Errorf("%s: FindEndpoint() error = %v, want unsupported %t", tc.name, err, tc.unsupported)
		}
		if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "example.test") {
			t.Errorf("%s: FindEndpoint() error %q quotes the URL", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.mention) {
			t.Errorf("%s: FindEndpoint() error %q, want it to name %q", tc.name, err, tc.mention)
		}
	}
}
