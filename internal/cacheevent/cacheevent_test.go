package cacheevent

import (
	"fmt"
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

func TestRecorderPassesRenderTargetThroughVerbatim(t *testing.T) {
	recorder := NewRecorder(true)
	recorder.Record(Event{Source: SourceRender, Action: ActionSkipped, Target: "argocd/urlvalues", Reason: "pin-unstable"})
	recorder.Record(Event{Source: SourceGit, Action: ActionFetch, Target: "https://user:secret@example.test/repo.git"})

	events := recorder.Events()
	if len(events) != 2 {
		t.Fatalf("Events = %#v, want two events", events)
	}
	if got := events[0].Target; got != "argocd/urlvalues" {
		t.Fatalf("render Target = %q, want application name passed through verbatim", got)
	}
	if got := events[1].Target; got != "https://example.test/repo.git" {
		t.Fatalf("git Target = %q, want redacted URL", got)
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

func TestRecorderRedactsRawEscapedUserInfoPassword(t *testing.T) {
	recorder := NewRecorder(true)
	recorder.Record(Event{
		Source: SourceGit,
		Action: ActionError,
		Target: "https://user:p%40ss@example.test/repo.git",
		Error:  "authentication failed with p%40ss and p@ss",
	})

	events := recorder.Events()
	if len(events) != 1 {
		t.Fatalf("Events = %#v, want one event", events)
	}
	for _, leaked := range []string{"p%40ss", "p@ss"} {
		if strings.Contains(events[0].Error, leaked) {
			t.Fatalf("Error = %q leaked userinfo password %q", events[0].Error, leaked)
		}
	}
}

func TestRecorderRedactsStandaloneQueryValues(t *testing.T) {
	recorder := NewRecorder(true)
	recorder.Record(Event{
		Source: SourceGit,
		Action: ActionError,
		Target: "https://example.test/repo.git?token=abc&encoded=space%20value&slash=abc%2Fdef#frag",
		Error:  "token abc rejected and encoded space%20value space value abc%2Fdef abc/def rejected",
	})

	events := recorder.Events()
	if len(events) != 1 {
		t.Fatalf("Events = %#v, want one event", events)
	}
	for _, leaked := range []string{"abc", "space%20value", "space value", "abc%2Fdef", "abc/def"} {
		if strings.Contains(events[0].Error, leaked) {
			t.Fatalf("Error = %q leaked query value %q", events[0].Error, leaked)
		}
	}
}

func TestActionForAcquisition(t *testing.T) {
	tests := []struct {
		name      string
		fromCache bool
		network   bool
		refresh   bool
		want      Action
	}{
		{name: "cache hit", fromCache: true, want: ActionHit},
		{name: "refresh fetch", network: true, refresh: true, want: ActionRefresh},
		{name: "network fetch", network: true, want: ActionFetch},
		{name: "local fetch fallback", want: ActionFetch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ActionForAcquisition(tt.fromCache, tt.network, tt.refresh); got != tt.want {
				t.Fatalf("ActionForAcquisition() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestActionForError(t *testing.T) {
	if got := ActionForError(fmt.Errorf("offline cache miss for chart")); got != ActionMiss {
		t.Fatalf("ActionForError(cache miss) = %s, want %s", got, ActionMiss)
	}
	if got := ActionForError(fmt.Errorf("permission denied")); got != ActionError {
		t.Fatalf("ActionForError(other) = %s, want %s", got, ActionError)
	}
}

func TestCompactSensitiveValues(t *testing.T) {
	got := CompactSensitiveValues("", " user ", "token")
	if diff := strings.Join(got, ","); diff != "user,token" {
		t.Fatalf("CompactSensitiveValues() = %q, want user,token", diff)
	}
}

func TestNewAcquisitionEventBuildsSuccessEvent(t *testing.T) {
	event := NewAcquisitionEvent(AcquisitionEventInput{
		Source:   SourceChart,
		Target:   "https://charts.example.test",
		Revision: "1.2.3",
		Refresh:  true,
		Network:  true,
	})
	if event.Action != ActionRefresh {
		t.Fatalf("Action = %s, want %s", event.Action, ActionRefresh)
	}
	if event.Revision != "1.2.3" {
		t.Fatalf("Revision = %q, want 1.2.3", event.Revision)
	}
}

func TestNewAcquisitionErrorRedactsEventAndReturnedMessage(t *testing.T) {
	result := NewAcquisitionError(AcquisitionEventInput{
		Source: SourceGit,
		Target: "https://user:secret@example.test/repo.git?token=abc",
		Err:    fmt.Errorf("offline cache miss for https://user:secret@example.test/repo.git?token=abc"),
		RawTargets: []string{
			"https://user:secret@example.test/repo.git?token=abc",
		},
		SensitiveValues: []string{"secret"},
	})
	if result.Event.Action != ActionMiss {
		t.Fatalf("Action = %s, want %s", result.Event.Action, ActionMiss)
	}
	if strings.Contains(result.RedactedError, "secret") || strings.Contains(result.RedactedError, "abc") {
		t.Fatalf("RedactedError = %q leaked credentials", result.RedactedError)
	}
	recorder := NewRecorder(true)
	recorder.Record(result.Event)
	events := recorder.Events()
	if len(events) != 1 {
		t.Fatalf("Events = %#v, want one event", events)
	}
	if strings.Contains(events[0].Error, "secret") || strings.Contains(events[0].Error, "abc") {
		t.Fatalf("Event error = %q leaked credentials", events[0].Error)
	}
}

func TestRecordRenderEventErrorKeepsApplicationLabel(t *testing.T) {
	recorder := NewRecorder(true)
	recorder.Record(Event{Source: SourceRender, Action: ActionError, Target: "argocd/demo", Error: "render argocd/demo failed"})
	events := recorder.Events()
	if len(events) != 1 || events[0].Error != "render argocd/demo failed" {
		t.Fatalf("render event = %#v, want the namespace/name label preserved in Error", events)
	}
}

func contains(value, fragment string) bool {
	return strings.Contains(value, fragment)
}
