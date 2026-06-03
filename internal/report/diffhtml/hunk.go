package diffhtml

import (
	"strconv"
	"strings"
)

type diffHunk struct {
	Header string
	Rows   []diffRow
}

type diffRow struct {
	Kind        string
	LeftNumber  int
	RightNumber int
	LeftText    string
	RightText   string
}

func parseUnifiedDiff(text string) []diffHunk {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var hunks []diffHunk
	var current *diffHunk
	var leftNumber, rightNumber int
	var leftRemaining, rightRemaining int

	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			leftNumber, leftRemaining, rightNumber, rightRemaining = parseHunkRange(line)
			hunks = append(hunks, diffHunk{Header: line})
			current = &hunks[len(hunks)-1]
			if hunkConsumed(leftRemaining, rightRemaining) {
				current = nil
			}
			continue
		}
		if current == nil || line == "" {
			continue
		}

		prefix := line[0]
		content := line[1:]
		switch prefix {
		case ' ':
			current.Rows = append(current.Rows, diffRow{
				Kind:        "context",
				LeftNumber:  leftNumber,
				RightNumber: rightNumber,
				LeftText:    content,
				RightText:   content,
			})
			leftNumber++
			rightNumber++
			leftRemaining--
			rightRemaining--
		case '-':
			current.Rows = append(current.Rows, diffRow{
				Kind:       "removed",
				LeftNumber: leftNumber,
				LeftText:   content,
			})
			leftNumber++
			leftRemaining--
		case '+':
			current.Rows = append(current.Rows, diffRow{
				Kind:        "added",
				RightNumber: rightNumber,
				RightText:   content,
			})
			rightNumber++
			rightRemaining--
		default:
			continue
		}
		if hunkConsumed(leftRemaining, rightRemaining) {
			current = nil
		}
	}

	return hunks
}

func parseHunkRange(header string) (int, int, int, int) {
	fields := strings.Fields(header)
	if len(fields) < 3 {
		return 0, 0, 0, 0
	}
	leftStart, leftCount := parseRange(fields[1])
	rightStart, rightCount := parseRange(fields[2])
	return leftStart, leftCount, rightStart, rightCount
}

func parseRange(rangeText string) (int, int) {
	rangeText = strings.TrimPrefix(rangeText, "-")
	rangeText = strings.TrimPrefix(rangeText, "+")
	start, countText, hasCount := strings.Cut(rangeText, ",")
	number, err := strconv.Atoi(start)
	if err != nil {
		return 0, 0
	}
	if !hasCount {
		return number, 1
	}
	count, err := strconv.Atoi(countText)
	if err != nil {
		return number, 0
	}
	return number, count
}

func hunkConsumed(leftRemaining, rightRemaining int) bool {
	return leftRemaining <= 0 && rightRemaining <= 0
}
