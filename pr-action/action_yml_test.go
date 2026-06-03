package praction

import (
	"os"
	"strings"
	"testing"
)

func TestActionUploadsHTMLDiffBeforeCommentAndAppendsURL(t *testing.T) {
	actionYAMLBytes, err := os.ReadFile("action.yml")
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}
	actionYAML := string(actionYAMLBytes)

	rawUpload := strings.Index(actionYAML, "- name: Upload rendered manifest diff\n")
	htmlUpload := strings.Index(actionYAML, "- name: Upload rendered manifest diff HTML\n")
	htmlLink := strings.Index(actionYAML, "- name: Add rendered manifest diff HTML link\n")
	comment := strings.Index(actionYAML, "- name: Comment rendered manifest diff\n")
	for name, index := range map[string]int{
		"raw diff upload":  rawUpload,
		"HTML diff upload": htmlUpload,
		"HTML link":        htmlLink,
		"diff comment":     comment,
	} {
		if index == -1 {
			t.Fatalf("action.yml missing %s step", name)
		}
	}
	if rawUpload >= htmlUpload || htmlUpload >= htmlLink || htmlLink >= comment {
		t.Fatalf("action.yml step order raw upload=%d html upload=%d html link=%d comment=%d", rawUpload, htmlUpload, htmlLink, comment)
	}

	htmlUploadBlock := actionYAML[htmlUpload:htmlLink]
	for _, want := range []string{
		"id: upload-diff-html",
		"path: ${{ steps.run.outputs.diff-html-path }}",
		"archive: false",
	} {
		if !strings.Contains(htmlUploadBlock, want) {
			t.Fatalf("HTML upload block missing %q:\n%s", want, htmlUploadBlock)
		}
	}

	linkBlock := actionYAML[htmlLink:comment]
	for _, want := range []string{
		"DRYDOCK_DIFF_COMMENT_PATH: ${{ steps.run.outputs.diff-comment-path }}",
		"DRYDOCK_DIFF_HTML_ARTIFACT_URL: ${{ steps.upload-diff-html.outputs.artifact-url }}",
		"[Full Diff Report](${DRYDOCK_DIFF_HTML_ARTIFACT_URL})",
		"artifact-url",
	} {
		if !strings.Contains(linkBlock, want) {
			t.Fatalf("HTML link block missing %q:\n%s", want, linkBlock)
		}
	}
	if strings.Contains(linkBlock, "[${DRYDOCK_DIFF_HTML_ARTIFACT_NAME}]") {
		t.Fatalf("HTML link block should not use artifact filename as link text:\n%s", linkBlock)
	}
	if strings.Contains(linkBlock, "[Full Diff Report](${DRYDOCK_DIFF_HTML_ARTIFACT_URL}).") {
		t.Fatalf("HTML link block should not append punctuation to link:\n%s", linkBlock)
	}
}
