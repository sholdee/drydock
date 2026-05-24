package cacheevent

import (
	"strings"
	"testing"
)

func TestRecorderSkipsWhenDisabled(t *testing.T) {
	recorder := NewRecorder(false)
	recorder.Record(Event{Source: SourceGit, Action: ActionHit, Target: "https://github.com/example/repo"})
	if got := recorder.Events(); len(got) != 0 {
		t.Fatalf("Events = %#v, want none", got)
	}
}

func TestRecorderRedactsTargetsAndCopiesEvents(t *testing.T) {
	recorder := NewRecorder(true)
	recorder.Record(Event{
		Source:          SourceGit,
		Action:          ActionFetch,
		Target:          "https://user:secret@example.test/repo.git?token=abc#frag",
		Revision:        "main",
		CacheHit:        false,
		Offline:         false,
		Refresh:         true,
		Error:           "fetch https://user:secret@example.test/repo.git?token=abc#frag failed with secret-password",
		SensitiveValues: []string{"secret-password"},
	})

	events := recorder.Events()
	if len(events) != 1 {
		t.Fatalf("Events = %#v, want one event", events)
	}
	if got := events[0].Target; got != "https://example.test/repo.git" {
		t.Fatalf("Target = %q, want redacted URL", got)
	}
	for _, leaked := range []string{"user", "secret", "token", "abc", "frag"} {
		if contains(events[0].Target, leaked) || contains(events[0].Error, leaked) {
			t.Fatalf("Event = %#v leaked %q", events[0], leaked)
		}
	}
	events[0].Target = "mutated"
	if got := recorder.Events()[0].Target; got == "mutated" {
		t.Fatalf("Events returned mutable backing slice")
	}
}

func TestRecorderRedactsErrorTargetVariants(t *testing.T) {
	recorder := NewRecorder(true)
	recorder.Record(Event{
		Source: SourceRemote,
		Action: ActionError,
		Target: "https://user:secret@example.test/repo.git?token=abc#frag",
		RawTargets: []string{
			"git::https://user:secret@example.test/repo.git?token=abc#frag",
			"https://user:secret@example.test/other.git?token=def#other-frag",
		},
		Error: strings.Join([]string{
			"fetch https://user:secret@example.test/repo.git?token=abc failed",
			"retry https://user:secret@example.test/repo.git failed",
			"remote https://example.test/repo.git?token=abc rejected",
			"stripped https://user:secret@example.test/repo?token=abc failed",
			"principal user:secret@ leaked",
			"masked principal user:***@ leaked",
			"account user@ leaked",
			"query token=abc leaked",
			"suffix frag leaked",
			"include git::https://user:secret@example.test/repo.git?token=abc#frag failed",
			"include https://user:secret@example.test/other.git?token=def#other-frag failed",
		}, "\n"),
	})

	events := recorder.Events()
	if len(events) != 1 {
		t.Fatalf("Events = %#v, want one event", events)
	}
	for _, leaked := range []string{"user", "secret", "token", "abc", "frag", "def", "other-frag"} {
		if strings.Contains(events[0].Error, leaked) {
			t.Fatalf("Error = %q leaked %q", events[0].Error, leaked)
		}
	}
	if strings.Contains(events[0].Error, "\n") {
		t.Fatalf("Error = %q contains newline", events[0].Error)
	}
}

func TestRecorderRedactsMultilineSensitiveValues(t *testing.T) {
	privateKey := "-----BEGIN PRIVATE KEY-----\nline-one\nline-two\n-----END PRIVATE KEY-----"
	recorder := NewRecorder(true)
	recorder.Record(Event{
		Source:          SourceGit,
		Action:          ActionError,
		Target:          "ssh://git@example.test/repo.git",
		Error:           "ssh auth failed with key:\n" + privateKey,
		SensitiveValues: []string{privateKey},
	})

	events := recorder.Events()
	if len(events) != 1 {
		t.Fatalf("Events = %#v, want one event", events)
	}
	for _, leaked := range []string{"PRIVATE KEY", "line-one", "line-two"} {
		if strings.Contains(events[0].Error, leaked) {
			t.Fatalf("Error = %q leaked %q", events[0].Error, leaked)
		}
	}
}

func TestRecorderRedactsSCPStyleTargetComponents(t *testing.T) {
	recorder := NewRecorder(true)
	recorder.Record(Event{
		Source: SourceGit,
		Action: ActionError,
		Target: "git@example.test:org/repo.git?token=abc#frag",
		Error:  "scp fetch failed token=abc frag",
	})

	events := recorder.Events()
	if len(events) != 1 {
		t.Fatalf("Events = %#v, want one event", events)
	}
	for _, leaked := range []string{"token=abc", "frag"} {
		if strings.Contains(events[0].Error, leaked) {
			t.Fatalf("Error = %q leaked %q", events[0].Error, leaked)
		}
	}
}

func TestRecorderRedactsBareUserInfoPassword(t *testing.T) {
	recorder := NewRecorder(true)
	recorder.Record(Event{
		Source: SourceGit,
		Action: ActionError,
		Target: "https://user:standalone-secret@example.test/repo.git",
		Error:  "authentication failed with standalone-secret",
	})

	events := recorder.Events()
	if len(events) != 1 {
		t.Fatalf("Events = %#v, want one event", events)
	}
	if strings.Contains(events[0].Error, "standalone-secret") {
		t.Fatalf("Error = %q leaked userinfo password", events[0].Error)
	}
}

func TestRecorderRedactsStandaloneQueryValues(t *testing.T) {
	recorder := NewRecorder(true)
	recorder.Record(Event{
		Source: SourceGit,
		Action: ActionError,
		Target: "https://example.test/repo.git?token=abc&encoded=space%20value#frag",
		Error:  "token abc rejected and encoded space value rejected",
	})

	events := recorder.Events()
	if len(events) != 1 {
		t.Fatalf("Events = %#v, want one event", events)
	}
	for _, leaked := range []string{"abc", "space value"} {
		if strings.Contains(events[0].Error, leaked) {
			t.Fatalf("Error = %q leaked query value %q", events[0].Error, leaked)
		}
	}
}

func contains(value, fragment string) bool {
	return strings.Contains(value, fragment)
}
