package diffhtml

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/sholdee/drydock/internal/app"
)

const defaultTitle = "drydock diff"

type Options struct {
	Title           string
	DefaultResource DefaultResourceSelector
}

type DefaultResourceSelector struct {
	ParentNamespace string
	ParentName      string
	Group           string
	Kind            string
	Namespace       string
	Name            string
}

func Render(result app.DiffResult, options Options) ([]byte, error) {
	title := strings.TrimSpace(options.Title)
	if title == "" {
		title = defaultTitle
	}

	groups := groupedResults(result.Results)
	added, removed := totalLineChanges(groups)
	resourceCounts := countResourceChanges(result.Results)
	defaultResourceID := selectDefaultResourceID(groups, options.DefaultResource)

	var builder bytes.Buffer
	builder.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	builder.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&builder, "<title>%s</title>\n", escape(title))
	fmt.Fprintf(&builder, "<link rel=\"icon\" type=\"image/svg+xml\" href=\"%s\">\n", drydockFaviconHref)
	builder.WriteString("<style>")
	builder.WriteString(reviewStyles)
	builder.WriteString("</style>\n</head>\n")
	renderBodyOpen(&builder, defaultResourceID)
	builder.WriteString("<header class=\"report-header\">\n")
	builder.WriteString("<button class=\"nav-toggle\" type=\"button\" data-sidebar-toggle aria-controls=\"diff-tree\" aria-expanded=\"true\" aria-label=\"Toggle changed resources\"><span aria-hidden=\"true\">☰</span></button>\n")
	builder.WriteString("<div class=\"header-copy\">\n")
	fmt.Fprintf(&builder, "<h1>%s</h1>\n", escape(title))
	renderSummary(&builder, len(groups), len(result.Results), resourceCounts, added, removed)
	builder.WriteString("</div>\n")
	builder.WriteString("<div class=\"header-actions\">\n")
	builder.WriteString(drydockLogo)
	builder.WriteString("\n")
	builder.WriteString("</div>\n")
	builder.WriteString("</header>\n")
	builder.WriteString("<div class=\"review-layout\">\n")
	renderTree(&builder, groups)
	builder.WriteString("<div class=\"sidebar-resizer\" data-sidebar-resizer role=\"separator\" aria-orientation=\"vertical\" aria-label=\"Resize changed resources sidebar\" aria-valuemin=\"240\" aria-valuemax=\"480\" aria-valuenow=\"320\" tabindex=\"0\"><span class=\"sidebar-resizer-hint\" aria-hidden=\"true\">Release to close</span></div>\n")
	builder.WriteString("<div class=\"sidebar-backdrop\" data-sidebar-backdrop></div>\n")
	builder.WriteString("<main class=\"review-main\">\n")
	if len(groups) == 0 {
		builder.WriteString("<p class=\"no-diff\">No rendered manifest differences detected.</p>\n")
	} else {
		if err := renderGroups(&builder, groups); err != nil {
			return nil, err
		}
	}
	renderDiagnostics(&builder, result.Diagnostics)
	builder.WriteString("</main>\n</div>\n<script>")
	builder.WriteString(reviewScript)
	builder.WriteString("</script>\n</body>\n</html>\n")
	return builder.Bytes(), nil
}

func renderBodyOpen(builder *bytes.Buffer, defaultResourceID string) {
	builder.WriteString("<body data-view=\"side-by-side\" data-sidebar=\"auto\"")
	if defaultResourceID != "" {
		fmt.Fprintf(builder, " data-default-resource=\"%s\"", escape(defaultResourceID))
	}
	builder.WriteString(">\n")
}

func Write(w io.Writer, result app.DiffResult, options Options) error {
	data, err := Render(result, options)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
