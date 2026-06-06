package diffhtml

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/sholdee/drydock/internal/diff"
)

type lazyDiffPayload struct {
	Change  string                 `json:"change"`
	Metrics lazyDiffPayloadMetrics `json:"metrics"`
	Hunks   []lazyDiffPayloadHunk  `json:"hunks"`
}

type lazyDiffPayloadMetrics struct {
	RawBytes     int `json:"rawBytes"`
	RawKiB       int `json:"rawKiB"`
	AddedLines   int `json:"addedLines"`
	RemovedLines int `json:"removedLines"`
	ParsedRows   int `json:"parsedRows"`
}

type lazyDiffPayloadHunk struct {
	Header string               `json:"header"`
	Rows   []lazyDiffPayloadRow `json:"rows"`
}

type lazyDiffPayloadRow struct {
	Kind        string                       `json:"kind"`
	LeftNumber  int                          `json:"leftNumber"`
	RightNumber int                          `json:"rightNumber"`
	LeftText    string                       `json:"leftText"`
	RightText   string                       `json:"rightText"`
	LeftSyntax  []lazyDiffPayloadSyntaxRange `json:"leftSyntax,omitempty"`
	RightSyntax []lazyDiffPayloadSyntaxRange `json:"rightSyntax,omitempty"`
}

type lazyDiffPayloadSyntaxRange struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Class string `json:"class"`
}

func diffRawKiB(rawBytes int) int {
	if rawBytes == 0 {
		return 0
	}
	return (rawBytes + 1023) / 1024
}

func lazyDiffMetricText(metrics resourceDiffMetrics) string {
	return fmt.Sprintf("%s rows, +%s/-%s, %s KiB",
		formatCount(metrics.parsedRows),
		formatCount(metrics.addedLines),
		formatCount(metrics.removedLines),
		formatCount(diffRawKiB(metrics.rawBytes)),
	)
}

func formatCount(count int) string {
	text := strconv.Itoa(count)
	if len(text) <= 3 {
		return text
	}
	var builder strings.Builder
	firstGroup := len(text) % 3
	if firstGroup == 0 {
		firstGroup = 3
	}
	builder.WriteString(text[:firstGroup])
	for offset := firstGroup; offset < len(text); offset += 3 {
		builder.WriteByte(',')
		builder.WriteString(text[offset : offset+3])
	}
	return builder.String()
}

func lazyDiffPayloadJSON(result diff.Result, metrics resourceDiffMetrics, hunks []diffHunk) (string, error) {
	payload := lazyDiffPayload{
		Change: string(result.Change),
		Metrics: lazyDiffPayloadMetrics{
			RawBytes:     metrics.rawBytes,
			RawKiB:       diffRawKiB(metrics.rawBytes),
			AddedLines:   metrics.addedLines,
			RemovedLines: metrics.removedLines,
			ParsedRows:   metrics.parsedRows,
		},
		Hunks: lazyDiffPayloadHunks(hunks),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return scriptDataSafeJSON(data), nil
}

func lazyDiffPayloadHunks(hunks []diffHunk) []lazyDiffPayloadHunk {
	payloadHunks := make([]lazyDiffPayloadHunk, 0, len(hunks))
	for _, hunk := range hunks {
		payloadRows := make([]lazyDiffPayloadRow, 0, len(hunk.Rows))
		for _, row := range hunk.Rows {
			payloadRows = append(payloadRows, lazyDiffPayloadRowFromDiffRow(row))
		}
		payloadHunks = append(payloadHunks, lazyDiffPayloadHunk{
			Header: hunk.Header,
			Rows:   payloadRows,
		})
	}
	return payloadHunks
}

func lazyDiffPayloadRowFromDiffRow(row diffRow) lazyDiffPayloadRow {
	return lazyDiffPayloadRow{
		Kind:        row.Kind,
		LeftNumber:  row.LeftNumber,
		RightNumber: row.RightNumber,
		LeftText:    row.LeftText,
		RightText:   row.RightText,
		LeftSyntax:  lazyDiffPayloadSyntaxRanges(row.LeftText),
		RightSyntax: lazyDiffPayloadSyntaxRanges(row.RightText),
	}
}

func lazyDiffPayloadSyntaxRanges(text string) []lazyDiffPayloadSyntaxRange {
	runes := []rune(text)
	syntax := normalizedSyntaxRanges(lexYAMLLine(text), len(runes))
	if len(syntax) == 0 {
		return nil
	}
	payload := make([]lazyDiffPayloadSyntaxRange, 0, len(syntax))
	for _, token := range syntax {
		payload = append(payload, lazyDiffPayloadSyntaxRange{
			Start: token.start,
			End:   token.end,
			Class: token.class,
		})
	}
	return payload
}

func scriptDataSafeJSON(data []byte) string {
	replacer := strings.NewReplacer(
		"<", `\u003c`,
		">", `\u003e`,
		"&", `\u0026`,
		"'", `\u0027`,
		"`", `\u0060`,
		"=", `\u003d`,
		"\u2028", `\u2028`,
		"\u2029", `\u2029`,
	)
	return replacer.Replace(string(data))
}
