package cacheevent

import (
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/home-operations/argocd-local/internal/remote"
)

type Source string

const (
	SourceGit    Source = "git"
	SourceChart  Source = "chart"
	SourceRemote Source = "remote"
)

type Action string

const (
	ActionLocal    Action = "local"
	ActionMapped   Action = "mapped"
	ActionHit      Action = "hit"
	ActionFetch    Action = "fetch"
	ActionRefresh  Action = "refresh"
	ActionMiss     Action = "miss"
	ActionError    Action = "error"
	ActionDisabled Action = "disabled"
)

type Event struct {
	Source          Source   `json:"source" yaml:"source"`
	Action          Action   `json:"action" yaml:"action"`
	Target          string   `json:"target,omitempty" yaml:"target,omitempty"`
	Revision        string   `json:"revision,omitempty" yaml:"revision,omitempty"`
	CacheHit        bool     `json:"cacheHit,omitempty" yaml:"cacheHit,omitempty"`
	Offline         bool     `json:"offline,omitempty" yaml:"offline,omitempty"`
	Refresh         bool     `json:"refresh,omitempty" yaml:"refresh,omitempty"`
	Error           string   `json:"error,omitempty" yaml:"error,omitempty"`
	RawTargets      []string `json:"-" yaml:"-"`
	SensitiveValues []string `json:"-" yaml:"-"`
}

type Recorder struct {
	enabled bool
	mu      sync.Mutex
	events  []Event
}

func NewRecorder(enabled bool) *Recorder {
	return &Recorder{enabled: enabled}
}

func (r *Recorder) Enabled() bool {
	return r != nil && r.enabled
}

func (r *Recorder) Record(event Event) {
	if !r.Enabled() {
		return
	}
	rawTargets := append([]string{event.Target}, event.RawTargets...)
	event.Target = RedactTarget(event.Target)
	event.Error = RedactEventError(event.Error, event.Target, rawTargets, event.SensitiveValues...)
	event.RawTargets = nil
	event.SensitiveValues = nil
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *Recorder) Events() []Event {
	if !r.Enabled() {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

func RedactTarget(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return remote.RedactGitRepoURL(raw)
}

func RedactEventError(value string, redactedTarget string, rawTargets []string, sensitiveValues ...string) string {
	if value == "" {
		return ""
	}
	message := strings.ReplaceAll(value, "\n", " ")
	for _, rawTarget := range rawTargets {
		for _, candidate := range targetVariants(rawTarget) {
			redacted := redactedVariant(candidate, redactedTarget)
			if candidate == "" || candidate == redacted {
				continue
			}
			message = strings.ReplaceAll(message, candidate, redacted)
		}
	}
	for _, secret := range sensitiveValues {
		for _, candidate := range sensitiveValueVariants(secret) {
			message = strings.ReplaceAll(message, candidate, "[redacted]")
		}
	}
	return message
}

func sensitiveValueVariants(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var variants []string
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		if _, exists := seen[candidate]; exists {
			return
		}
		seen[candidate] = struct{}{}
		variants = append(variants, candidate)
	}
	add(value)
	normalized := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(value)
	add(normalized)
	for _, line := range strings.FieldsFunc(value, func(r rune) bool { return r == '\r' || r == '\n' }) {
		add(line)
	}
	sort.SliceStable(variants, func(i, j int) bool {
		return len(variants[i]) > len(variants[j])
	})
	return variants
}

func redactedVariant(raw string, fallback string) string {
	candidate := strings.TrimPrefix(strings.TrimSpace(raw), "git::")
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "[redacted]"
	}
	redacted := RedactTarget(candidate)
	if redacted == "" {
		redacted = fallback
	}
	if redacted == "" {
		return "[redacted]"
	}
	return redacted
}

func targetVariants(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	collector := newVariantCollector()
	collector.add(raw)
	withoutGitPrefix := strings.TrimPrefix(raw, "git::")
	collector.add(withoutGitPrefix)
	collector.addURLVariants("", withoutGitPrefix)
	if strings.HasPrefix(raw, "git::") {
		collector.addURLVariants("git::", withoutGitPrefix)
	}
	collector.addSCPStyleComponents(withoutGitPrefix)
	variants := collector.values()
	sort.SliceStable(variants, func(i, j int) bool {
		return len(variants[i]) > len(variants[j])
	})
	return variants
}

type variantCollector struct {
	seen       map[string]struct{}
	valuesList []string
}

func newVariantCollector() *variantCollector {
	return &variantCollector{seen: map[string]struct{}{}}
}

func (c *variantCollector) add(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if _, exists := c.seen[value]; exists {
		return
	}
	c.seen[value] = struct{}{}
	c.valuesList = append(c.valuesList, value)
}

func (c *variantCollector) values() []string {
	return c.valuesList
}

func (c *variantCollector) addURLVariants(prefix string, rawURL string) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return
	}
	for _, base := range parsedURLBases(*parsed) {
		c.addURLComponentCombinations(prefix, base)
	}
	c.addUserInfoVariants(parsed.User)
	c.add(parsed.RawQuery)
	c.addRawQueryValues(parsed.RawQuery)
	c.add(parsed.Fragment)
}

func parsedURLBases(parsed url.URL) []url.URL {
	baseURLs := []url.URL{parsed}
	if strippedPath := strings.TrimSuffix(parsed.Path, ".git"); strippedPath != parsed.Path {
		stripped := parsed
		stripped.Path = strippedPath
		baseURLs = append(baseURLs, stripped)
	}
	return baseURLs
}

func (c *variantCollector) addURLComponentCombinations(prefix string, base url.URL) {
	for _, dropUser := range []bool{false, true} {
		for _, dropQuery := range []bool{false, true} {
			for _, dropFragment := range []bool{false, true} {
				clone := base
				if dropUser {
					clone.User = nil
				}
				if dropQuery {
					clone.RawQuery = ""
					clone.ForceQuery = false
				}
				if dropFragment {
					clone.Fragment = ""
				}
				c.add(prefix + clone.String())
			}
		}
	}
}

func (c *variantCollector) addUserInfoVariants(userInfo *url.Userinfo) {
	if userInfo == nil {
		return
	}
	username := userInfo.Username()
	password, hasPassword := userInfo.Password()
	rawUserInfo := userInfo.String()
	c.add(rawUserInfo + "@")
	rawUsername, rawPassword, rawHasPassword := strings.Cut(rawUserInfo, ":")
	if username != "" {
		c.add(rawUsername)
		c.add(username + ":***@")
		c.add(username + "@")
	}
	if hasPassword {
		if rawHasPassword {
			c.add(rawPassword)
		}
		c.add(password)
	}
}

func (c *variantCollector) addSCPStyleComponents(raw string) {
	c.addRawQueryAndFragment(raw)
	if parsed, err := url.Parse("ssh://" + raw); err == nil && parsed.Host != "" {
		c.add(parsed.RawQuery)
		c.addRawQueryValues(parsed.RawQuery)
		c.add(parsed.Fragment)
		c.addUserInfoVariants(parsed.User)
	}
}

func (c *variantCollector) addRawQueryAndFragment(raw string) {
	withoutFragment, fragment, hasFragment := strings.Cut(raw, "#")
	if hasFragment {
		c.add(fragment)
	}
	_, rawQuery, hasQuery := strings.Cut(withoutFragment, "?")
	if hasQuery {
		c.add(rawQuery)
		c.addRawQueryValues(rawQuery)
	}
}

func (c *variantCollector) addRawQueryValues(rawQuery string) {
	for _, rawPart := range strings.Split(rawQuery, "&") {
		_, rawValue, ok := strings.Cut(rawPart, "=")
		if ok {
			c.add(rawValue)
		}
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return
	}
	for _, queryValues := range values {
		for _, value := range queryValues {
			c.add(value)
		}
	}
}
