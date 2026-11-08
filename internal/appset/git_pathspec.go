package appset

import (
	"fmt"
	"math"
	"strings"
)

// The Argo CD v3.6.1 Git files generator lists its files with
// `git ls-files --full-name -z -- <pattern>` in the root of the checkout
// (util/git/client.go:763-770), unless
// applicationsetcontroller.enable.new.git.file.globbing is on. So each
// pattern is one git pathspec. This file ports what git does with that
// pathspec: parse_pathspec and init_pathspec_item (pathspec.c),
// prefix_path_gently and normalize_path_copy_len (path.c, abspath.c),
// match_pathspec_item and git_fnmatch (dir.c), and wildmatch (wildmatch.c).
// A plain pathspec differs from a shell glob: "*" and "?" match "/" too, and
// a pathspec that names a directory matches every file below it.

// Pathspec magic bits, as in pathspec.h.
const (
	pathspecFromTop = 1 << iota
	pathspecLiteral
	pathspecGlob
	pathspecICase
	pathspecExclude
	pathspecAttr
)

// gitPathspecItem is one parsed pathspec (struct pathspec_item).
type gitPathspecItem struct {
	match          string
	magic          int
	nowildcardLen  int
	oneStar        bool
	originalString string
	// prefix is the length of the leading part of match that the prefix
	// magic marks. Under icase magic it still matches case-sensitively.
	prefix int
}

// gitPathspec is a parsed pathspec list (struct pathspec). The Git files
// generator passes one pattern, which can still hold two items: an
// exclude-only pattern gets an implicit item that matches everything.
type gitPathspec struct {
	items []gitPathspecItem
}

// gitPathspecError is a pathspec that git refuses: `git ls-files` exits
// with "fatal:" and the message, and the generator fails.
type gitPathspecError struct {
	message string
}

func (e *gitPathspecError) Error() string {
	return e.message
}

// gitPathspecUnsupportedError is a pathspec that git accepts but drydock
// does not evaluate.
type gitPathspecUnsupportedError struct {
	message string
}

func (e *gitPathspecUnsupportedError) Error() string {
	return e.message
}

// parseGitPathspec parses one ls-files argument run in the top level of the
// worktree (prefix ""), as parse_pathspec with PATHSPEC_PREFER_CWD does.
// The Argo CD repo-server turns an empty pattern into "." before it runs git
// (reposerver/repository/repository.go:3310-3312), so pattern is never empty
// here.
func parseGitPathspec(pattern string) (gitPathspec, error) {
	if pattern == "" {
		return gitPathspec{}, &gitPathspecError{message: "empty string is not a valid pathspec. please use . instead if you meant to match all paths"}
	}
	item, err := parseGitPathspecItem(pattern)
	if err != nil {
		return gitPathspec{}, err
	}
	spec := gitPathspec{items: []gitPathspecItem{item}}
	if item.magic&pathspecExclude != 0 {
		// parse_pathspec: when every item is an exclude, add one item that
		// matches everything.
		all, err := parseGitPathspecItem("")
		if err != nil {
			return gitPathspec{}, err
		}
		spec.items = append(spec.items, all)
	}
	return spec, nil
}

//nolint:gocyclo // A copy of git init_pathspec_item keeps its shape.
func parseGitPathspecItem(elt string) (gitPathspecItem, error) {
	magic := 0
	copyFrom := elt
	pathspecPrefix := -1
	if strings.HasPrefix(elt, ":") {
		var err error
		if strings.HasPrefix(elt, ":(") {
			copyFrom, magic, pathspecPrefix, err = parseGitLongMagic(elt)
		} else {
			copyFrom, magic, err = parseGitShortMagic(elt)
		}
		if err != nil {
			return gitPathspecItem{}, err
		}
	}
	if magic&pathspecLiteral != 0 && magic&pathspecGlob != 0 {
		return gitPathspecItem{}, &gitPathspecError{message: fmt.Sprintf("%s: 'literal' and 'glob' are incompatible", elt)}
	}
	if magic&pathspecAttr != 0 {
		return gitPathspecItem{}, &gitPathspecUnsupportedError{message: fmt.Sprintf("git files pattern %q uses the attr pathspec magic, which drydock does not evaluate", elt)}
	}

	var match string
	prefixLen := 0
	switch {
	case pathspecPrefix >= 0:
		// init_pathspec_item copies a pathspec with prefix magic as written.
		match = copyFrom
		prefixLen = pathspecPrefix
	case magic&pathspecFromTop != 0:
		// init_pathspec_item copies a "top" pathspec as written.
		match = copyFrom
	default:
		normalized, ok := gitPrefixPath(copyFrom)
		if !ok {
			return gitPathspecItem{}, &gitPathspecError{message: fmt.Sprintf("%s: '%s' is outside repository", elt, copyFrom)}
		}
		match = normalized
	}

	item := gitPathspecItem{match: match, magic: magic, originalString: elt, prefix: prefixLen}
	if magic&pathspecLiteral != 0 {
		item.nowildcardLen = len(match)
	} else {
		item.nowildcardLen = max(gitSimpleLength(match), prefixLen)
	}
	if item.nowildcardLen > len(match) || item.prefix > len(match) {
		// git calls BUG("error initializing pathspec_item") and aborts.
		return gitPathspecItem{}, &gitPathspecError{message: "BUG: error initializing pathspec_item"}
	}
	if magic&pathspecGlob == 0 && item.nowildcardLen < len(match) &&
		match[item.nowildcardLen] == '*' && gitNoWildcard(match[item.nowildcardLen+1:]) {
		item.oneStar = true
	}
	return item, nil
}

// gitPathspecMagicChars is GIT_PATHSPEC_MAGIC (ctype.c): the characters
// that can be short magic.
const gitPathspecMagicChars = "!\"#%&',-/:;<=>@_`~"

// parseGitShortMagic is parse_short_magic.
func parseGitShortMagic(elt string) (string, int, error) {
	magic := 0
	pos := 1
	for ; pos < len(elt) && elt[pos] != ':'; pos++ {
		ch := elt[pos]
		if ch == '^' {
			magic |= pathspecExclude
			continue
		}
		if !strings.ContainsRune(gitPathspecMagicChars, rune(ch)) {
			break
		}
		switch ch {
		case '/':
			magic |= pathspecFromTop
		case '!':
			magic |= pathspecExclude
		default:
			return "", 0, &gitPathspecError{message: fmt.Sprintf("Unimplemented pathspec magic '%c' in '%s'", ch, elt)}
		}
	}
	if pos < len(elt) && elt[pos] == ':' {
		pos++
	}
	return elt[pos:], magic, nil
}

// parseGitLongMagic is parse_long_magic.
//
//nolint:gocyclo // A copy of git parse_long_magic keeps its shape.
func parseGitLongMagic(elt string) (string, int, int, error) {
	magic := 0
	prefixLen := -1
	pos := 2
	for pos < len(elt) && elt[pos] != ')' {
		length := gitStrcspnEscaped(elt[pos:], ",)")
		next := pos + length
		if next < len(elt) && elt[next] == ',' {
			next++
		}
		word := elt[pos : pos+length]
		pos = next
		if length == 0 {
			continue
		}
		switch {
		case strings.HasPrefix(word, "prefix:"):
			value, end := gitStrtol(word[len("prefix:"):])
			if len("prefix:")+end != len(word) {
				return "", 0, -1, &gitPathspecError{message: "invalid parameter for pathspec magic 'prefix'"}
			}
			prefixLen = value
			continue
		case strings.HasPrefix(word, "attr:"):
			magic |= pathspecAttr
			continue
		}
		switch word {
		case "top":
			magic |= pathspecFromTop
		case "literal":
			magic |= pathspecLiteral
		case "glob":
			magic |= pathspecGlob
		case "icase":
			magic |= pathspecICase
		case "exclude":
			magic |= pathspecExclude
		case "attr":
			magic |= pathspecAttr
		default:
			return "", 0, -1, &gitPathspecError{message: fmt.Sprintf("Invalid pathspec magic '%s' in '%s'", word, elt)}
		}
	}
	if pos >= len(elt) || elt[pos] != ')' {
		return "", 0, -1, &gitPathspecError{message: fmt.Sprintf("Missing ')' at the end of pathspec magic in '%s'", elt)}
	}
	return elt[pos+1:], magic, prefixLen, nil
}

// gitStrtol is strtol(s, &end, 10) as git calls it for the prefix magic:
// leading white space, an optional sign, and decimal digits. end is the
// length that strtol read, or 0 when it read no digits. strtol clamps to
// LONG_MAX and LONG_MIN, and git stores the long in an int, which keeps its
// low 32 bits.
//
//nolint:gocyclo // A copy of strtol keeps its shape.
func gitStrtol(s string) (int, int) {
	i := 0
	for i < len(s) && (s[i] == ' ' || (s[i] >= '\t' && s[i] <= '\r')) {
		i++
	}
	negative := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		negative = s[i] == '-'
		i++
	}
	start := i
	var magnitude uint64
	overflow := false
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		digit := uint64(s[i] - '0')
		if magnitude > (math.MaxUint64-digit)/10 {
			overflow = true
		} else {
			magnitude = magnitude*10 + digit
		}
		i++
	}
	if i == start {
		return 0, 0
	}
	var value int64
	switch {
	case negative && (overflow || magnitude > uint64(math.MaxInt64)+1):
		value = math.MinInt64
	case negative:
		value = int64(-magnitude)
	case overflow || magnitude > math.MaxInt64:
		value = math.MaxInt64
	default:
		value = int64(magnitude)
	}
	return int(int32(value)), i
}

// gitStrcspnEscaped is strcspn_escaped: the length of the leading part of s
// without a byte of reject, where a backslash escapes the next byte.
func gitStrcspnEscaped(s, reject string) int {
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			i += 2
			continue
		}
		if strings.IndexByte(reject, s[i]) >= 0 {
			break
		}
		i++
	}
	return i
}

// gitPrefixPath is prefix_path_gently with an empty prefix: an absolute path
// names a place outside the checkout (the repo-server checks out into a
// temporary directory that a pattern cannot name), and a relative path is
// normalized by normalize_path_copy_len, which fails when ".." climbs above
// the top level.
func gitPrefixPath(raw string) (string, bool) {
	if strings.HasPrefix(raw, "/") {
		return "", false
	}
	return gitNormalizePath(raw)
}

// gitNormalizePath is normalize_path_copy_len for a relative path: it
// removes "." components and repeated slashes, and resolves "..". A
// trailing slash stays.
//
//nolint:gocyclo // A copy of git normalize_path_copy_len keeps its shape.
func gitNormalizePath(src string) (string, bool) {
	dst := make([]byte, 0, len(src))
	i := 0
	for i < len(src) && src[i] == '/' {
		i++
	}
	for {
		if i < len(src) && src[i] == '.' {
			switch {
			case i+1 == len(src):
				// "." at the end.
				i++
			case src[i+1] == '/':
				// "./".
				i += 2
				for i < len(src) && src[i] == '/' {
					i++
				}
				continue
			case src[i+1] == '.' && (i+2 == len(src) || src[i+2] == '/'):
				// ".." at the end or "../".
				if i+2 == len(src) {
					i += 2
				} else {
					i += 3
					for i < len(src) && src[i] == '/' {
						i++
					}
				}
				// Go up one level: dst ends with "/" here.
				if len(dst) == 0 {
					return "", false
				}
				dst = dst[:len(dst)-1]
				for len(dst) > 0 && dst[len(dst)-1] != '/' {
					dst = dst[:len(dst)-1]
				}
				continue
			}
		}
		// Copy up to the next slash, and eat all slashes.
		for i < len(src) && src[i] != '/' {
			dst = append(dst, src[i])
			i++
		}
		if i < len(src) {
			dst = append(dst, '/')
			for i < len(src) && src[i] == '/' {
				i++
			}
			continue
		}
		break
	}
	return string(dst), true
}

// gitIsGlobSpecial is is_glob_special.
func gitIsGlobSpecial(ch byte) bool {
	return ch == '*' || ch == '?' || ch == '[' || ch == '\\'
}

// gitSimpleLength is simple_length: the length of the leading part without
// a glob special character.
func gitSimpleLength(match string) int {
	for i := range len(match) {
		if gitIsGlobSpecial(match[i]) {
			return i
		}
	}
	return len(match)
}

// gitNoWildcard is no_wildcard.
func gitNoWildcard(s string) bool {
	return gitSimpleLength(s) == len(s)
}

// matchesEntry reports whether git lists name, an entry of the index, for
// the pathspec: it matches a positive item and no exclude item
// (match_pathspec_with_flags). ls-files matches a gitlink as a directory
// (DO_MATCH_DIRECTORY), so a pattern with its name and a trailing slash
// matches it too.
func (spec gitPathspec) matchesEntry(name string, isDir bool) bool {
	positive := false
	for _, item := range spec.items {
		if item.magic&pathspecExclude == 0 && item.matchesEntry(name, isDir) {
			positive = true
			break
		}
	}
	if !positive {
		return false
	}
	for _, item := range spec.items {
		if item.magic&pathspecExclude != 0 && item.matchesEntry(name, isDir) {
			return false
		}
	}
	return true
}

// matches is match_pathspec_item for a file of the index, with no common
// prefix cut off and without DO_MATCH_DIRECTORY.
// matchesEntry is match_pathspec_item, with DO_MATCH_DIRECTORY when isDir.
//
//nolint:gocyclo // A copy of git match_pathspec_item keeps its shape.
func (item gitPathspecItem) matchesEntry(name string, isDir bool) bool {
	match := item.match
	icase := item.magic&pathspecICase != 0
	if item.prefix > 0 && icase && gitStrncmp(match, name, item.prefix) != 0 {
		return false
	}
	if match == "" {
		return true
	}
	if len(match) <= len(name) && gitStrEqual(match, name[:len(match)], icase) {
		if len(match) == len(name) {
			return true
		}
		if match[len(match)-1] == '/' || name[len(match)] == '/' {
			return true
		}
	} else if isDir && match[len(match)-1] == '/' && len(name) == len(match)-1 &&
		gitStrEqual(match[:len(name)], name, icase) {
		return true
	}
	if item.nowildcardLen < len(match) && item.fnmatch(name) {
		return true
	}
	return false
}

// fnmatch is git_fnmatch, which reports a match where git's returns 0.
func (item gitPathspecItem) fnmatch(name string) bool {
	icase := item.magic&pathspecICase != 0
	pattern := item.match
	text := name
	if prefix := item.nowildcardLen; prefix > 0 {
		if len(text) < prefix || !gitStrEqual(pattern[:prefix], text[:prefix], icase) {
			return false
		}
		pattern = pattern[prefix:]
		text = text[prefix:]
	}
	if item.oneStar {
		suffix := pattern[1:]
		if len(text) < len(suffix) {
			return false
		}
		return gitStrEqual(suffix, text[len(text)-len(suffix):], icase)
	}
	flags := 0
	if icase {
		flags |= wmCaseFold
	}
	if item.magic&pathspecGlob != 0 {
		flags |= wmPathname
	}
	return gitWildmatch(pattern, text, flags)
}

// gitStrncmp is strncmp over Go strings, which end where C strings have
// their NUL byte.
func gitStrncmp(a, b string, n int) int {
	for i := range n {
		ca, cb := at(a, i), at(b, i)
		if ca != cb {
			return int(ca) - int(cb)
		}
		if ca == 0 {
			return 0
		}
	}
	return 0
}

// gitStrEqual is ps_strncmp over equal lengths: strncmp, or strncasecmp
// for icase magic.
func gitStrEqual(a, b string, icase bool) bool {
	if icase {
		return gitASCIILower(a) == gitASCIILower(b)
	}
	return a == b
}

func gitASCIILower(s string) string {
	b := []byte(s)
	for i, ch := range b {
		if ch >= 'A' && ch <= 'Z' {
			b[i] = ch + 'a' - 'A'
		}
	}
	return string(b)
}

// wildmatch flags, as in wildmatch.h.
const (
	wmCaseFold = 1
	wmPathname = 2
)

// dowild results, as in wildmatch.c.
const (
	wmMatch           = 0
	wmNoMatch         = 1
	wmAbortAll        = -1
	wmAbortToStarStar = -2
)

// gitWildmatch is wildmatch: it reports whether text matches pattern.
func gitWildmatch(pattern, text string, flags int) bool {
	return gitDowild(pattern, 0, text, 0, flags) == wmMatch
}

func gitIsUpper(ch byte) bool { return ch >= 'A' && ch <= 'Z' }
func gitIsLower(ch byte) bool { return ch >= 'a' && ch <= 'z' }
func gitToLower(ch byte) byte {
	if gitIsUpper(ch) {
		return ch + 'a' - 'A'
	}
	return ch
}
func gitToUpper(ch byte) byte {
	if gitIsLower(ch) {
		return ch - ('a' - 'A')
	}
	return ch
}

// at returns the byte at i, or 0 past the end, as reading a C string does.
func at(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

// gitDowild is dowild (wildmatch.c), with indexes into the strings in place
// of C pointers. at returns the terminating NUL past the end.
//
//nolint:gocyclo // A copy of git dowild keeps its shape.
func gitDowild(pattern string, p int, text string, t int, flags int) int {
	for ; at(pattern, p) != 0; t, p = t+1, p+1 {
		pCh := at(pattern, p)
		tCh := at(text, t)
		if tCh == 0 && pCh != '*' {
			return wmAbortAll
		}
		if flags&wmCaseFold != 0 {
			tCh = gitToLower(tCh)
			pCh = gitToLower(pCh)
		}
		switch pCh {
		case '\\':
			// Literal match with the following character. wildmatch does
			// not fold the case of an escaped character.
			p++
			pCh = at(pattern, p)
			if tCh != pCh {
				return wmNoMatch
			}
			continue
		case '?':
			// Match anything but '/' with WM_PATHNAME.
			if flags&wmPathname != 0 && tCh == '/' {
				return wmNoMatch
			}
			continue
		case '*':
			var matchSlash bool
			p++
			if at(pattern, p) == '*' {
				prevP := p - 2
				for {
					p++
					if at(pattern, p) != '*' {
						break
					}
				}
				switch {
				case flags&wmPathname == 0:
					// Without WM_PATHNAME, '*' == '**'.
					matchSlash = true
				case (prevP < 0 || pattern[prevP] == '/') &&
					(at(pattern, p) == 0 || at(pattern, p) == '/' ||
						(at(pattern, p) == '\\' && at(pattern, p+1) == '/')):
					// "**/" matches no directory or several.
					if at(pattern, p) == '/' && gitDowild(pattern, p+1, text, t, flags) == wmMatch {
						return wmMatch
					}
					matchSlash = true
				default:
					matchSlash = false
				}
			} else {
				matchSlash = flags&wmPathname == 0
			}
			if at(pattern, p) == 0 {
				// A trailing "**" matches everything. A trailing "*"
				// matches only without more slashes.
				if !matchSlash && strings.IndexByte(text[t:], '/') >= 0 {
					return wmNoMatch
				}
				return wmMatch
			} else if !matchSlash && at(pattern, p) == '/' {
				// One asterisk followed by a slash with WM_PATHNAME
				// matches the next directory.
				slash := strings.IndexByte(text[t:], '/')
				if slash < 0 {
					return wmNoMatch
				}
				t += slash
				// The slash is consumed by the loop.
				break
			}
			for tCh != 0 {
				// Advance faster when an asterisk is followed by a
				// literal: the text before the literal belongs to "*".
				if !gitIsGlobSpecial(at(pattern, p)) {
					pLit := at(pattern, p)
					if flags&wmCaseFold != 0 {
						pLit = gitToLower(pLit)
					}
					for {
						tCh = at(text, t)
						if tCh == 0 || (!matchSlash && tCh == '/') {
							break
						}
						if flags&wmCaseFold != 0 {
							tCh = gitToLower(tCh)
						}
						if tCh == pLit {
							break
						}
						t++
					}
					if tCh != pLit {
						if matchSlash {
							return wmAbortAll
						}
						return wmAbortToStarStar
					}
				}
				matched := gitDowild(pattern, p, text, t, flags)
				if matched != wmNoMatch {
					if !matchSlash || matched != wmAbortToStarStar {
						return matched
					}
				} else if !matchSlash && tCh == '/' {
					return wmAbortToStarStar
				}
				t++
				tCh = at(text, t)
				if flags&wmCaseFold != 0 {
					tCh = gitToLower(tCh)
				}
			}
			return wmAbortAll
		case '[':
			p++
			pCh = at(pattern, p)
			if pCh == '^' {
				pCh = '!'
			}
			negated := pCh == '!'
			if negated {
				p++
				pCh = at(pattern, p)
			}
			var prevCh byte
			matched := false
			for {
				if pCh == 0 {
					return wmAbortAll
				}
				switch {
				case pCh == '\\':
					p++
					pCh = at(pattern, p)
					if pCh == 0 {
						return wmAbortAll
					}
					if tCh == pCh {
						matched = true
					}
				case pCh == '-' && prevCh != 0 && at(pattern, p+1) != 0 && at(pattern, p+1) != ']':
					p++
					pCh = at(pattern, p)
					if pCh == '\\' {
						p++
						pCh = at(pattern, p)
						if pCh == 0 {
							return wmAbortAll
						}
					}
					if tCh <= pCh && tCh >= prevCh {
						matched = true
					} else if flags&wmCaseFold != 0 && gitIsLower(tCh) {
						upper := gitToUpper(tCh)
						if upper <= pCh && upper >= prevCh {
							matched = true
						}
					}
					pCh = 0 // This makes prevCh 0.
				case pCh == '[' && at(pattern, p+1) == ':':
					p += 2
					s := p
					for {
						pCh = at(pattern, p)
						if pCh == 0 || pCh == ']' {
							break
						}
						p++
					}
					if pCh == 0 {
						return wmAbortAll
					}
					i := p - s - 1
					if i < 0 || pattern[p-1] != ':' {
						// No ":]", so a plain '['.
						p = s - 2
						pCh = '['
						if tCh == pCh {
							matched = true
						}
						break
					}
					class := pattern[s : s+i]
					ok, known := gitCharClass(class, tCh, flags)
					if !known {
						return wmAbortAll
					}
					if ok {
						matched = true
					}
					pCh = 0 // This makes prevCh 0.
				default:
					if tCh == pCh {
						matched = true
					}
				}
				prevCh = pCh
				p++
				pCh = at(pattern, p)
				if pCh == ']' {
					break
				}
			}
			if matched == negated || (flags&wmPathname != 0 && tCh == '/') {
				return wmNoMatch
			}
			continue
		default:
			if tCh != pCh {
				return wmNoMatch
			}
			continue
		}
	}
	if t < len(text) {
		return wmNoMatch
	}
	return wmMatch
}

// gitCharClass matches a [:class:] of wildmatch. known is false for a
// class name that wildmatch does not know.
//
//nolint:gocyclo // One case per wildmatch character class.
func gitCharClass(class string, ch byte, flags int) (ok, known bool) {
	switch class {
	case "alnum":
		return gitIsAlpha(ch) || gitIsDigit(ch), true
	case "alpha":
		return gitIsAlpha(ch), true
	case "blank":
		return ch == ' ' || ch == '\t', true
	case "cntrl":
		return ch < 0x20 || ch == 0x7f, true
	case "digit":
		return gitIsDigit(ch), true
	case "graph":
		return ch > 0x20 && ch < 0x7f, true
	case "lower":
		return gitIsLower(ch), true
	case "print":
		return ch >= 0x20 && ch < 0x7f, true
	case "punct":
		return ch > 0x20 && ch < 0x7f && !gitIsAlpha(ch) && !gitIsDigit(ch), true
	case "space":
		return ch == ' ' || (ch >= '\t' && ch <= '\r'), true
	case "upper":
		return gitIsUpper(ch) || (flags&wmCaseFold != 0 && gitIsLower(ch)), true
	case "xdigit":
		return gitIsDigit(ch) || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F'), true
	default:
		return false, false
	}
}

func gitIsAlpha(ch byte) bool { return gitIsUpper(ch) || gitIsLower(ch) }
func gitIsDigit(ch byte) bool { return ch >= '0' && ch <= '9' }
