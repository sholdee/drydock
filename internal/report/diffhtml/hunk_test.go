package diffhtml

import "testing"

func TestParseUnifiedDiffHunksLineNumbers(t *testing.T) {
	hunks := parseUnifiedDiff("@@ -10,3 +10,4 @@\n context\n-removed\n+added one\n+added two\n")

	if len(hunks) != 1 {
		t.Fatalf("len(hunks) = %d, want 1", len(hunks))
	}
	if hunks[0].Header != "@@ -10,3 +10,4 @@" {
		t.Fatalf("Header = %q, want %q", hunks[0].Header, "@@ -10,3 +10,4 @@")
	}

	want := []diffRow{
		{Kind: "context", LeftNumber: 10, RightNumber: 10, LeftText: "context", RightText: "context"},
		{Kind: "removed", LeftNumber: 11, LeftText: "removed"},
		{Kind: "added", RightNumber: 11, RightText: "added one"},
		{Kind: "added", RightNumber: 12, RightText: "added two"},
	}
	assertDiffRows(t, hunks[0].Rows, want)
}

func TestParseUnifiedDiffMultipleHunks(t *testing.T) {
	hunks := parseUnifiedDiff("--- old\n+++ new\n@@ -1,1 +1,1 @@\n-old\n+new\n@@ -20,1 +20,1 @@\n-old twenty\n+new twenty\n")

	if len(hunks) != 2 {
		t.Fatalf("len(hunks) = %d, want 2", len(hunks))
	}
	if len(hunks[0].Rows) == 0 {
		t.Fatal("first hunk has no rows")
	}
	if got := hunks[0].Rows[0]; got.Kind != "removed" || got.LeftNumber != 1 {
		t.Fatalf("first hunk first row = %+v, want removed row at line 1", got)
	}
	if len(hunks[1].Rows) == 0 {
		t.Fatal("second hunk has no rows")
	}
	if got := hunks[1].Rows[0]; got.Kind != "removed" || got.LeftNumber != 20 {
		t.Fatalf("second hunk first row = %+v, want removed row at line 20", got)
	}
}

func TestParseUnifiedDiffPreservesRowsThatResembleFileHeaders(t *testing.T) {
	hunks := parseUnifiedDiff("--- old\n+++ new\n@@ -1,1 +1,1 @@\n---removed\n+++added\n")

	if len(hunks) != 1 {
		t.Fatalf("len(hunks) = %d, want 1", len(hunks))
	}
	want := []diffRow{
		{Kind: "removed", LeftNumber: 1, LeftText: "--removed"},
		{Kind: "added", RightNumber: 1, RightText: "++added"},
	}
	assertDiffRows(t, hunks[0].Rows, want)
}

func TestParseUnifiedDiffStopsAtDeclaredHunkRanges(t *testing.T) {
	hunks := parseUnifiedDiff("--- old\n+++ new\n@@ -1,1 +1,1 @@\n-old\n+new\n--- old2\n+++ new2\n@@ -20,1 +20,1 @@\n-old twenty\n+new twenty\n")

	if len(hunks) != 2 {
		t.Fatalf("len(hunks) = %d, want 2", len(hunks))
	}
	assertDiffRows(t, hunks[0].Rows, []diffRow{
		{Kind: "removed", LeftNumber: 1, LeftText: "old"},
		{Kind: "added", RightNumber: 1, RightText: "new"},
	})
	assertDiffRows(t, hunks[1].Rows, []diffRow{
		{Kind: "removed", LeftNumber: 20, LeftText: "old twenty"},
		{Kind: "added", RightNumber: 20, RightText: "new twenty"},
	})
}

func assertDiffRows(t *testing.T, got, want []diffRow) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("len(rows) = %d, want %d\nrows = %+v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("row %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}
