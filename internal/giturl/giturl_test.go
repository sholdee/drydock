package giturl

import "testing"

func TestRedactInMessageKeepsSSHUsernameWordsIntact(t *testing.T) {
	got := RedactInMessage(
		"git SSH auth for repository ssh://git@github.com/o/r.git failed",
		"ssh://git@github.com/o/r.git",
		"ssh://github.com/o/r.git",
	)
	want := "git SSH auth for repository ssh://github.com/o/r.git failed"
	if got != want {
		t.Fatalf("RedactInMessage() = %q, want %q", got, want)
	}
}

func TestRedactInMessageRedactsEmbeddedCredentials(t *testing.T) {
	got := RedactInMessage(
		"clone https://alice:secret@host/o/r.git failed",
		"https://alice:secret@host/o/r.git",
		"https://host/o/r.git",
	)
	want := "clone https://host/o/r.git failed"
	if got != want {
		t.Fatalf("RedactInMessage() = %q, want %q", got, want)
	}
}
