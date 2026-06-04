---
title: Output Controls
---

drydock keeps stdout machine-parseable for structured output. Diagnostics and
failure summaries go to stderr unless status text is the primary output.

## Formats

Use `-o` or `--output` to select the format supported by each command:

| Command area | Output formats |
| --- | --- |
| `get apps`, `get images` | `table`, `name`, `json`, `yaml` |
| `test apps`, `test app` | `text`, `json`, `yaml` |
| `diff apps`, `diff app` | `unified`, `markdown`, `json`, `yaml` |
| `diff images` | `unified`, `markdown`, `json`, `yaml`, `name` |
| `diag` | `text`, `json`, `yaml` |

`-o name` is intended for list-style output. It is not supported for manifest
diffs, and removed-only image changes print no names while still counting as a
diff for exit-code behavior.

## Diff Controls

Unified manifest and image diffs support terminal color with
`--color=auto|always|never`. The default `auto` colors only when stdout is a
terminal. JSON and YAML outputs never include ANSI color.

Use markdown output when the result will be read in a pull request comment:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig origin/main -o markdown
drydock diff images --repo . --ref HEAD --ref-orig origin/main -o markdown
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

## Exit Codes

Diff commands use fixed exit codes:

| Code | Meaning |
| --- | --- |
| `0` | Success, no diff |
| `1` | Success, diff found |
| `2` | Runtime, config, or render error |

Use `--exit-code=false` for local inspection when a diff should not fail the
command.

For the full command reference, see the [CLI usage guide](/docs/usage/). For
action outputs and PR comment controls, see the [GitHub Actions guide](/docs/github-actions/).
