package diffhtml

import (
	"os"
	"strings"
	"testing"
)

func TestDocsSiteSearchKeyboardContract(t *testing.T) {
	script, err := os.ReadFile("../../../site/assets/js/site.js")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	text := string(script)
	for _, want := range []string{
		`const clearOrBlurSearch = () => {`,
		`if (input.value) {`,
		`input.blur();`,
		`event.key === "/" && !isEditable(event.target)`,
		`input.focus();`,
		`input.select();`,
		`event.key === "Escape" && (document.activeElement === input || input.value || !panel.hidden)`,
		`event.stopPropagation();`,
		`clearOrBlurSearch();`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("site search script missing keyboard contract %q", want)
		}
	}
}
