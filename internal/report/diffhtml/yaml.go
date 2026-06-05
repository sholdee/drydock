package diffhtml

import (
	"slices"
	"strings"
)

const (
	yamlKeyClass         = "yaml-key"
	yamlStringClass      = "yaml-string"
	yamlNumberClass      = "yaml-number"
	yamlBoolClass        = "yaml-bool"
	yamlNullClass        = "yaml-null"
	yamlCommentClass     = "yaml-comment"
	yamlDocClass         = "yaml-doc"
	yamlAnchorClass      = "yaml-anchor"
	yamlAliasClass       = "yaml-alias"
	yamlTagClass         = "yaml-tag"
	yamlPunctuationClass = "yaml-punctuation"
)

type syntaxRange struct {
	start int
	end   int
	class string
}

func lexYAMLLine(line string) []syntaxRange {
	runes := []rune(line)
	if len(runes) == 0 {
		return nil
	}
	if start, end, ok := yamlDocMarkerRange(runes); ok {
		return []syntaxRange{{start: start, end: end, class: yamlDocClass}}
	}

	var tokens []syntaxRange
	cursor := firstNonYAMLSpace(runes, 0)
	if cursor >= len(runes) {
		return nil
	}
	if isYAMLCommentStart(runes, cursor) {
		return []syntaxRange{{start: cursor, end: len(runes), class: yamlCommentClass}}
	}
	if isYAMLListMarker(runes, cursor) {
		tokens = append(tokens, syntaxRange{start: cursor, end: cursor + 1, class: yamlPunctuationClass})
		cursor = firstNonYAMLSpace(runes, cursor+1)
	}

	if keyStart, keyEnd, colon, ok := findYAMLMappingKey(runes, cursor); ok {
		tokens = append(tokens, syntaxRange{start: keyStart, end: keyEnd, class: yamlKeyClass})
		tokens = append(tokens, syntaxRange{start: colon, end: colon + 1, class: yamlPunctuationClass})
		scanYAMLValueTokens(runes, colon+1, &tokens)
		return tokens
	}

	scanYAMLValueTokens(runes, cursor, &tokens)
	return tokens
}

func yamlDocMarkerRange(runes []rune) (int, int, bool) {
	start := firstNonYAMLSpace(runes, 0)
	end := lastNonYAMLSpace(runes)
	if end-start != 3 {
		return 0, 0, false
	}
	if string(runes[start:end]) != "---" && string(runes[start:end]) != "..." {
		return 0, 0, false
	}
	return start, end, true
}

func findYAMLMappingKey(runes []rune, start int) (int, int, int, bool) {
	if start >= len(runes) {
		return 0, 0, 0, false
	}
	var quote rune
	for index := start; index < len(runes); index++ {
		current := runes[index]
		if quote != 0 {
			quote, index = scanYAMLQuotedRune(runes, index, quote)
			continue
		}
		switch {
		case current == '"' || current == '\'':
			quote = current
		case isYAMLCommentStart(runes, index):
			return 0, 0, 0, false
		case current == '{' || current == '[':
			return 0, 0, 0, false
		case current == ':':
			return finishYAMLBlockKeyCandidate(runes, start, index)
		}
	}
	return 0, 0, 0, false
}

func scanYAMLValueTokens(runes []rune, start int, tokens *[]syntaxRange) {
	flowDepth := 0
	for cursor := start; cursor < len(runes); {
		if next, ok := scanYAMLWhitespaceOrComment(runes, cursor, tokens); ok {
			if next < 0 {
				return
			}
			cursor = next
			continue
		}

		if next, nextFlowDepth, ok := scanYAMLFlowPunctuationToken(runes, cursor, flowDepth, tokens); ok {
			cursor = next
			flowDepth = nextFlowDepth
			continue
		}

		if flowDepth > 0 {
			if next, ok := scanYAMLFlowKeyOrColon(runes, cursor, tokens); ok {
				cursor = next
				continue
			}
		}

		if next, ok := scanYAMLSpecialValueToken(runes, cursor, tokens); ok {
			if next < 0 {
				return
			}
			cursor = next
			continue
		}

		end := yamlPlainTokenEnd(runes, cursor)
		if end <= cursor {
			cursor++
			continue
		}
		if class := classifyYAMLPlainToken(runes[cursor:end]); class != "" {
			*tokens = append(*tokens, syntaxRange{start: cursor, end: end, class: class})
		}
		cursor = end
	}
}

func scanYAMLWhitespaceOrComment(runes []rune, cursor int, tokens *[]syntaxRange) (int, bool) {
	if isYAMLSpace(runes[cursor]) {
		return cursor + 1, true
	}
	if isYAMLCommentStart(runes, cursor) {
		*tokens = append(*tokens, syntaxRange{start: cursor, end: len(runes), class: yamlCommentClass})
		return -1, true
	}
	return cursor, false
}

func scanYAMLFlowPunctuationToken(runes []rune, cursor, flowDepth int, tokens *[]syntaxRange) (int, int, bool) {
	current := runes[cursor]
	switch {
	case isYAMLFlowOpen(current):
		*tokens = append(*tokens, syntaxRange{start: cursor, end: cursor + 1, class: yamlPunctuationClass})
		return cursor + 1, flowDepth + 1, true
	case isYAMLFlowClose(current):
		*tokens = append(*tokens, syntaxRange{start: cursor, end: cursor + 1, class: yamlPunctuationClass})
		return cursor + 1, max(flowDepth-1, 0), true
	case current == ',':
		*tokens = append(*tokens, syntaxRange{start: cursor, end: cursor + 1, class: yamlPunctuationClass})
		return cursor + 1, flowDepth, true
	default:
		return cursor, flowDepth, false
	}
}

func scanYAMLFlowKeyOrColon(runes []rune, cursor int, tokens *[]syntaxRange) (int, bool) {
	if keyStart, keyEnd, colon, ok := findYAMLFlowKey(runes, cursor); ok {
		*tokens = append(*tokens, syntaxRange{start: keyStart, end: keyEnd, class: yamlKeyClass})
		*tokens = append(*tokens, syntaxRange{start: colon, end: colon + 1, class: yamlPunctuationClass})
		return colon + 1, true
	}
	if runes[cursor] == ':' {
		*tokens = append(*tokens, syntaxRange{start: cursor, end: cursor + 1, class: yamlPunctuationClass})
		return cursor + 1, true
	}
	return cursor, false
}

func scanYAMLSpecialValueToken(runes []rune, cursor int, tokens *[]syntaxRange) (int, bool) {
	switch runes[cursor] {
	case '"', '\'':
		if end, ok := scanYAMLQuotedString(runes, cursor); ok {
			*tokens = append(*tokens, syntaxRange{start: cursor, end: end, class: yamlStringClass})
			return end, true
		}
		return -1, true
	case '&':
		if end := scanYAMLNameToken(runes, cursor); end > cursor+1 {
			*tokens = append(*tokens, syntaxRange{start: cursor, end: end, class: yamlAnchorClass})
			return end, true
		}
	case '*':
		if end := scanYAMLNameToken(runes, cursor); end > cursor+1 {
			*tokens = append(*tokens, syntaxRange{start: cursor, end: end, class: yamlAliasClass})
			return end, true
		}
	case '!':
		end := scanYAMLNameToken(runes, cursor)
		*tokens = append(*tokens, syntaxRange{start: cursor, end: end, class: yamlTagClass})
		return end, true
	case '-':
		if isYAMLListMarker(runes, cursor) {
			*tokens = append(*tokens, syntaxRange{start: cursor, end: cursor + 1, class: yamlPunctuationClass})
			return cursor + 1, true
		}
	}
	return cursor, false
}

func scanYAMLQuotedRune(runes []rune, index int, quote rune) (rune, int) {
	current := runes[index]
	if quote == '"' && current == '\\' {
		return quote, index + 1
	}
	if current != quote {
		return quote, index
	}
	if quote == '\'' && index+1 < len(runes) && runes[index+1] == '\'' {
		return quote, index + 1
	}
	return 0, index
}

func finishYAMLKeyCandidate(runes []rune, start, colon int) (int, int, int, bool) {
	if colon+1 < len(runes) && runes[colon+1] == '/' {
		return 0, 0, 0, false
	}
	keyStart, keyEnd := trimYAMLRange(runes, start, colon)
	if keyStart == keyEnd || !isYAMLKeyCandidate(runes[keyStart:keyEnd]) {
		return 0, 0, 0, false
	}
	return keyStart, keyEnd, colon, true
}

func finishYAMLBlockKeyCandidate(runes []rune, start, colon int) (int, int, int, bool) {
	if colon+1 < len(runes) && !isYAMLSpace(runes[colon+1]) {
		return 0, 0, 0, false
	}
	return finishYAMLKeyCandidate(runes, start, colon)
}

func findYAMLFlowKey(runes []rune, start int) (int, int, int, bool) {
	if start >= len(runes) || !isYAMLPlainKeyStart(runes[start]) {
		return 0, 0, 0, false
	}
	for cursor := start; cursor < len(runes); cursor++ {
		current := runes[cursor]
		switch {
		case isYAMLCommentStart(runes, cursor):
			return 0, 0, 0, false
		case current == ':':
			return finishYAMLKeyCandidate(runes, start, cursor)
		case isYAMLSpace(current):
			return finishYAMLFlowKeyAfterSpace(runes, start, cursor)
		case isYAMLFlowPunctuation(current):
			return 0, 0, 0, false
		}
	}
	return 0, 0, 0, false
}

func finishYAMLFlowKeyAfterSpace(runes []rune, start, space int) (int, int, int, bool) {
	colon := firstNonYAMLSpace(runes, space)
	if colon >= len(runes) || runes[colon] != ':' {
		return 0, 0, 0, false
	}
	return finishYAMLKeyCandidate(runes, start, colon)
}

func scanYAMLQuotedString(runes []rune, start int) (int, bool) {
	quote := runes[start]
	for cursor := start + 1; cursor < len(runes); cursor++ {
		current := runes[cursor]
		if quote == '"' && current == '\\' {
			cursor++
			continue
		}
		if current == quote {
			if quote == '\'' && cursor+1 < len(runes) && runes[cursor+1] == '\'' {
				cursor++
				continue
			}
			return cursor + 1, true
		}
	}
	return 0, false
}

func scanYAMLNameToken(runes []rune, start int) int {
	cursor := start + 1
	for cursor < len(runes) {
		if isYAMLSpace(runes[cursor]) ||
			isYAMLCommentStart(runes, cursor) ||
			isYAMLFlowPunctuation(runes[cursor]) ||
			runes[cursor] == ':' {
			break
		}
		cursor++
	}
	if cursor == start+1 && runes[start] == '!' {
		return cursor
	}
	return cursor
}

func yamlPlainTokenEnd(runes []rune, start int) int {
	cursor := start
	for cursor < len(runes) {
		if isYAMLSpace(runes[cursor]) ||
			isYAMLCommentStart(runes, cursor) ||
			isYAMLFlowPunctuation(runes[cursor]) {
			break
		}
		cursor++
	}
	return cursor
}

func classifyYAMLPlainToken(token []rune) string {
	switch strings.ToLower(string(token)) {
	case "true", "false":
		return yamlBoolClass
	case "null", "~":
		return yamlNullClass
	}
	if isYAMLNumber(token) {
		return yamlNumberClass
	}
	return ""
}

func isYAMLNumber(token []rune) bool {
	cursor := 0
	if cursor < len(token) && (token[cursor] == '+' || token[cursor] == '-') {
		cursor++
	}

	digitsBeforeDot := consumeYAMLDigits(token, cursor)
	cursor += digitsBeforeDot
	digitsAfterDot := 0
	if cursor < len(token) && token[cursor] == '.' {
		cursor++
		digitsAfterDot = consumeYAMLDigits(token, cursor)
		cursor += digitsAfterDot
	}
	if digitsBeforeDot+digitsAfterDot == 0 {
		return false
	}

	if cursor < len(token) && (token[cursor] == 'e' || token[cursor] == 'E') {
		cursor++
		if cursor < len(token) && (token[cursor] == '+' || token[cursor] == '-') {
			cursor++
		}
		exponentDigits := consumeYAMLDigits(token, cursor)
		if exponentDigits == 0 {
			return false
		}
		cursor += exponentDigits
	}
	return cursor == len(token)
}

func consumeYAMLDigits(token []rune, start int) int {
	cursor := start
	for cursor < len(token) && token[cursor] >= '0' && token[cursor] <= '9' {
		cursor++
	}
	return cursor - start
}

func firstNonYAMLSpace(runes []rune, start int) int {
	for start < len(runes) && isYAMLSpace(runes[start]) {
		start++
	}
	return start
}

func lastNonYAMLSpace(runes []rune) int {
	end := len(runes)
	for end > 0 && isYAMLSpace(runes[end-1]) {
		end--
	}
	return end
}

func trimYAMLRange(runes []rune, start, end int) (int, int) {
	start = firstNonYAMLSpace(runes, start)
	for end > start && isYAMLSpace(runes[end-1]) {
		end--
	}
	return start, end
}

func isYAMLSpace(value rune) bool {
	return value == ' ' || value == '\t'
}

func isYAMLCommentStart(runes []rune, index int) bool {
	return index < len(runes) &&
		runes[index] == '#' &&
		(index == 0 || isYAMLSpace(runes[index-1]))
}

func isYAMLListMarker(runes []rune, index int) bool {
	return index < len(runes) &&
		runes[index] == '-' &&
		(index+1 == len(runes) || isYAMLSpace(runes[index+1]))
}

func isYAMLFlowOpen(value rune) bool {
	return value == '{' || value == '['
}

func isYAMLFlowClose(value rune) bool {
	return value == '}' || value == ']'
}

func isYAMLFlowPunctuation(value rune) bool {
	return isYAMLFlowOpen(value) || isYAMLFlowClose(value) || value == ','
}

func isYAMLPlainKeyStart(value rune) bool {
	return !isYAMLSpace(value) &&
		!isYAMLFlowPunctuation(value) &&
		value != ':' &&
		value != '"' &&
		value != '\'' &&
		value != '#' &&
		value != '&' &&
		value != '*' &&
		value != '!'
}

func isYAMLKeyCandidate(token []rune) bool {
	if len(token) == 0 {
		return false
	}
	if token[0] == '-' {
		return false
	}
	return !slices.Contains(token, '=')
}
