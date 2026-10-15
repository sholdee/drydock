---
title: Output Controls
---

drydock keeps stdout machine-parseable for structured output. Diagnostics and
failure summaries go to stderr unless status text is the primary output.

## Formats

Use `-o` or `--output` to select the format supported by each command:

| Command area | Output formats |
| --- | --- |
| `build apps`, `build app` | `yaml`, `json` |
| `get apps`, `get images` | `table`, `name`, `json`, `yaml` |
| `test apps`, `test app` | `text`, `json`, `yaml` |
| `diff apps`, `diff app` | `diff`, `dyff`, `markdown`, `json`, `yaml` |
| `diff images` | `diff`, `markdown`, `json`, `yaml`, `name` |
| `diag` | `text`, `json`, `yaml` |

`-o name` is intended for list-style output. It is not supported for manifest
diffs, and removed-only image changes print no names while still counting as a
diff for exit-code behavior.

Diagnostics remain on stderr for unified, JSON, YAML, and name output so stdout
stays parseable. Markdown diff output embeds successful diagnostics in the
generated report because the markdown document is the review surface: errors
are listed openly above the diffs, warnings collapse inside an expandable
block rendered below the diffs, and repeated diagnostic codes aggregate into
one entry with a count. Text diagnostics carry their stable code
(`error plugin.unsupported: …`) so the same identifier appears in text, JSON,
YAML, and markdown output.

`test apps` text output prints `PASS`, `FAIL`, or `SKIPPED` status lines. When
stdout and stderr are terminals, status lines stream as Applications complete
and stderr shows an in-place progress counter. Redirected text output stays
plain and buffered.

## Diff Controls

Unified and dyff manifest diffs and unified image diffs support terminal color
with `--color=auto|always|never`. The default `auto` colors only when stdout is
a terminal. JSON and YAML outputs never include ANSI color.

`-o dyff` renders each changed resource as a structural, path-based report
using the [dyff](https://github.com/homeport/dyff) library, which runs
in-process (no `dyff` binary is needed). It is available for `diff apps` and
`diff app`, not `diff images`. Secret values stay redacted, and `--raw-output-file`
and `--html-output-file` still write the unified form.

```bash
drydock diff apps --repo . --ref HEAD --ref-orig origin/main -o dyff
```

For pull request comments, `--markdown-diff-style dyff` keeps `-o markdown` but
writes each Application's changes as dyff's GitHub-style report (the
`dyff between -o github` format) inside the `diff` fence, so GitHub colors the
path, change type, and added and removed values. The default is `unified`.
Unlike the `dyff` CLI, drydock compares values exactly (no inline-JSON or
number normalization), does not inspect certificates, and falls back to the
unified diff for a resource whose values contain control characters.
The summary counts, size budget, `--raw-output-file`, and `--html-output-file`
still use the unified diff.

```bash
drydock diff apps --repo . --ref HEAD --ref-orig origin/main -o markdown --markdown-diff-style dyff
```

```diff
@@ spec.template.spec.containers.app.image @@
# apps/v1/Deployment/default/app
! ± value change
- registry.example/app:v1
+ registry.example/app:v2
```

Use markdown output when the result will be read in a pull request comment:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig origin/main -o markdown
drydock diff images --repo . --ref HEAD --ref-orig origin/main -o markdown
```

`--markdown-diagnostics` controls how much of the diagnostics list the
markdown report embeds. The default `all` lists errors openly above the diffs
and collapses warnings inside an expandable block below them; `errors` omits
warnings entirely; `none`
omits the whole diagnostics section. Summary warning and error counts stay in
the report in every mode, and the flag never changes stderr, JSON, or YAML
diagnostics:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig origin/main -o markdown --markdown-diagnostics errors
```

For local browser review, manifest diff commands can also write the standalone
Full Rendered Diff View used by the PR action artifact:

```bash
drydock diff apps \
  --repo . \
  --ref HEAD \
  --ref-orig origin/main \
  -o markdown \
  --html-output-file rendered-diff.html
```

Markdown remains the PR-comment stdout format. `--html-output-file` writes an
additional HTML file with the same standalone review view.

Manifest markdown includes a summary plus expandable per-Application rendered
manifest patches. A single Application is expanded. Several Applications stay
collapsed unless `--force-open-details` is set. Image markdown is a companion view for scanning rendered
image reference changes and omits unchanged images by default.

## Exit Codes

Diff commands use fixed exit codes:

| Code | Meaning |
| --- | --- |
| `0` | Success, no diff |
| `1` | Success, diff found |
| `2` | Runtime, config, or render error |

Use `--exit-code=false` for local inspection when a diff should not fail the
command.

For related operator guides, see the [reference hub](/reference/). For action
outputs and PR comment controls, see the [GitHub Actions guide](/workflows/github-actions/).
