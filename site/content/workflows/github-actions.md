---
title: GitHub Actions
aliases:
  - /docs/github-actions/
---

drydock publishes two composite GitHub Actions:

- `sholdee/drydock/setup-action`: install a released `drydock` binary.
- `sholdee/drydock/pr-action`: install `drydock`, run PR validation, upload
  diff artifacts, and optionally maintain sticky PR comments.

Use `setup-action` when you want to own the CLI commands. Use `pr-action` when
you want the standard render test, markdown manifest diff, image diff, render
cache, artifacts, and pull request comment workflow.

The PR action is a convenience wrapper around the CLI. It does not run image
pull verification and does not pass GitHub tokens to the `drydock` subprocess.

## Full Rendered Diff View

The PR action turns rendered manifest changes into a compact pull request
comment and a standalone Full Rendered Diff View artifact:

{{< pr-comment-example >}}

When `upload-artifacts: "true"` and a manifest diff exists, the manifest diff
comment links to the Full Rendered Diff View. The raw unified diff artifact
remains available for scripts and archival workflows. Artifact availability
follows `artifact-retention-days`; GitHub controls whether the artifact link
opens in the current tab or a new tab.

For a permanent sample:

[Full Rendered Diff View](/examples/full-rendered-diff-view.html)

## Manual CLI Workflow

Use `setup-action` directly when you want a minimal workflow and prefer to own
the drydock commands yourself:

```yaml
name: drydock

on:
  pull_request:
    branches: [main]

permissions:
  contents: read

jobs:
  drydock:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
        with:
          fetch-depth: 0
      - uses: sholdee/drydock/setup-action@main
        with:
          version: vX.Y.Z
      - run: drydock test apps --path .
      - run: drydock diff apps --repo . --ref HEAD --ref-orig origin/${{ github.base_ref }}
      - run: >-
          drydock diff images --repo . --ref HEAD
          --ref-orig origin/${{ github.base_ref }} -o markdown
```

This lower-level example diffs against the base branch tip. When the pull
request branch is behind its base, that surfaces changes already merged into
the base as spurious differences. With full history checked out
(`fetch-depth: 0`), diff against the merge base instead — for example
`--ref-orig "$(git merge-base origin/${{ github.base_ref }} HEAD)"`. The PR
action resolves this merge base automatically.

The setup action accepts `latest`, `vX.Y.Z`, or bare `X.Y.Z`. It verifies the
release archive with `checksums.txt` unless `allow-unverified: "true"` is set.

By default, the setup action caches the verified release archive by resolved
version, runner OS/architecture, release repository, checksum, and key suffix.
`latest` is resolved to a concrete tag before cache lookup, so a new release
receives a new cache key. Binary caching is skipped when
`allow-unverified: "true"` is set. Disable it with `cache-binary: "false"` if a
workflow wants every run to download the archive.

## Pull Request Action

Use the PR action when you want the standard render test, manifest diff, image
diff, render cache, artifacts, and sticky comments:

```yaml
name: drydock

on:
  pull_request:
    branches: [main]

permissions:
  contents: read
  pull-requests: write

jobs:
  drydock:
    runs-on: ubuntu-latest
    steps:
      - uses: sholdee/drydock/pr-action@main
        with:
          version: vX.Y.Z
          comment-mode: both
          skip-secrets: "true"
          changed-only-include: |
            apps/**
          changed-only-ignore: |
            .github/**
```

The PR action checks out the pull request, fetches the base ref, runs
`drydock test apps`, renders the desired-state manifest diff, writes full diff
artifacts when differences are found, and comments in trusted same-repository
pull requests. Image diff comments are available as a companion signal. Fork
pull requests skip comments and render-cache restore/save by default.

By default the action:

- checks out the pull request head with credentials not persisted into Git;
- restores or saves a verified drydock binary archive when installation is
  enabled;
- fetches the pull request base branch and resolves its merge base with the
  head for ref-based diffs, deepening a shallow checkout as needed;
- runs `drydock test apps --path .`;
- runs `drydock diff apps --repo . --ref HEAD --ref-orig <merge-base>`, where
  `<merge-base>` is the common ancestor of the base branch and the pull request
  head, so changes already merged into the base branch are not surfaced as
  spurious differences;
- runs `drydock diff images` to report rendered image reference changes;
- records current-only image additions for the added image artifact;
- uses drydock render caches under the runner temp directory;
- uploads raw diff and browser-openable Full Rendered Diff View artifacts when
  manifest differences are found;
- writes sticky PR comments for trusted same-repository pull requests.

Image diff reporting does not run image pull verification. It compares rendered
image references rather than pulling images.

## Reporting And Gating

By default, `pr-action` fails render errors and reports manifest or image diffs
through comments and artifacts without failing the workflow. To make diffs a
gate, set `strict`, `strict-changed-only`, `fail-on-diff`, and optionally
`fail-on-image-diff`.

Use `run-test`, `run-diff`, and `run-image-diff` to disable individual default
steps. `fail-on-render-error` controls whether render test failures fail the
workflow when tests are enabled.

`changed-only-include` and `changed-only-ignore` are optional
newline-delimited globs passed to manifest and image diffs. They keep known
non-GitOps paths from forcing a full-fleet changed-only fallback. Keep them
narrow; ignored paths cannot trigger Application renders. They do not affect
`test apps`.

`discover-ignore` is different: its newline-delimited globs exclude matching
files from repository discovery before decoding, and it applies to `test apps`
as well as both diff steps. Use it when the repository commits non-deployable
YAML, such as unrendered chart templates, that fails strict discovery decoding.
With `strict-changed-only`, a discover-ignored file that changes still counts
as an unowned changed path unless it is also listed in `changed-only-ignore`;
repositories usually want the same globs in both inputs.

Use markdown output directly when building a custom workflow, or let
`pr-action` produce the comment:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig origin/main -o markdown
```

## Image Diff Companion Comment

Image comments can be enabled alongside the manifest diff. They are useful for
quickly scanning added and removed rendered image references:

{{< image-comment-example >}}

Run image diff markdown directly when building a custom workflow:

```bash
drydock diff images --repo . --ref HEAD --ref-orig origin/main -o markdown
```

## Authentication

The action accepts either a normal token or GitHub App credentials:

```yaml
- uses: sholdee/drydock/pr-action@main
  with:
    version: vX.Y.Z
    github-token: ${{ secrets.DRYDOCK_TOKEN }}
```

```yaml
- uses: sholdee/drydock/pr-action@main
  with:
    version: vX.Y.Z
    github-app-client-id: ${{ secrets.DRYDOCK_APP_CLIENT_ID }}
    github-app-private-key: ${{ secrets.DRYDOCK_APP_PRIVATE_KEY }}
```

`github-app-id` remains available as a legacy fallback for existing workflows,
but new workflows should use the GitHub App client ID.

The token is used for release downloads, checkout, baseline fetch, and PR
comments. Checkout uses `persist-credentials: false`, and the token is not
exported to `drydock test`, `drydock diff`, cache contents, or uploaded
artifacts.

## Caching

Binary caching is separate from drydock repository caches. Binary cache entries
contain only the released `drydock` archive and are keyed by release checksum.
The PR action cache root contains fetched Git, Helm, and remote Kustomize
sources, policy-managed plugin cache mounts, and persisted render outputs for
the repository under test.

Dirty-worktree render output reuse uses the existing render cache behavior. No
new PR action input is required.

Bump `cache-key-suffix` to start a fresh cache namespace (for example after a
renderer change you do not want served from older entries). Because render
outputs are content-addressed by Application input digests, serving a stale
entry is already safe — a suffix bump is a deliberate clean-slate switch rather
than a correctness control.

Fork pull requests do not restore or save drydock render caches by default,
because render caches can contain private repository, chart, remote source, or
plugin cache material. They also skip PR comments by default. Set
`cache-untrusted-restore: "true"` only if restoring cache contents into fork PR
runs is acceptable for that repository. Cache save remains disabled for fork
PRs.

When trusted container plugins are enabled, policy-managed plugin cache mounts
live under the PR action cache root as `${cache-path}/plugin`. Persisted render
outputs live under `${cache-path}/renders`. Both are restored or saved with the
same action cache entry. `drydock cache` lifecycle commands manage Git,
chart, remote-resource, and render output cache entry roots (use
`--render-cache-dir ${cache-path}/renders` to target the action's persisted
renders); they do not manage plugin cache mount roots.

### Cache Scope And Warming

GitHub Actions scopes every cache to the Git ref that created it. A run can
restore caches created in its own ref, the pull request base branch, and the
repository default branch. Caches created on the default branch are readable by
all branches and pull requests, but a cache saved during a pull request run is
scoped to that pull request and is reused only by later runs of the same pull
request — never by other pull requests.

A workflow that triggers only on `pull_request` therefore never populates a
shared cache. Every pull request misses on restore, renders cold, and saves a
cache that no other pull request can read. To make the render cache effective
across pull requests, warm it from the default branch: run the action on pushes
to the default branch with `save-cache: "true"`. The action rotates the cache
key per commit (it appends the commit SHA), and `actions/cache` keys are
immutable, so each push writes a fresh entry rather than freezing the first one.
Pull request runs restore the most recent matching entry through the generated
`cache-restore-keys` prefixes and re-render only the Applications whose inputs
changed.

```yaml
name: drydock cache warm

on:
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  warm:
    runs-on: ubuntu-latest
    steps:
      - uses: sholdee/drydock/pr-action@main
        with:
          version: vX.Y.Z
          run-diff: "false"
          run-image-diff: "false"
          comment-mode: none
          save-cache: "true"
```

The warm run renders the default branch's desired state with `drydock test
apps`; diffs are disabled because they need a base ref that a push event does
not provide. Leave `path` at its default (the repository root) so discovery
covers every Application your pull requests render. Keep `version`,
`cache-key-prefix`, and `cache-key-suffix` the same as the pull request workflow
so both runs share the same restore-key prefix; the action appends the commit
SHA to the primary key, so the keys differ per commit but resolve through that
prefix. Add a `paths:` filter to the `push` trigger if you only want to warm
when manifests change.

To keep pull request runs from consuming the cache budget and evicting the
shared default-branch entry, set `save-cache: "false"` on the pull request
action so those runs restore only. The render cache is content-addressed by
Application input digests, so a default-branch warm covers every unchanged
Application regardless of which commit a pull request branched from.

### Self-Hosted Runners And Local Caching

`actions/cache` is a remote, branch-scoped backend, so the scope limits above
apply on self-hosted runners too. On a self-hosted runner with durable storage,
persist the cache on the runner's own filesystem instead: it avoids the
upload/download round-trips and the cross-pull-request scope limits entirely —
every pull request and push on that runner shares one on-disk cache, which is
safe because the render cache is content-addressed by Application input digests.

Select the backend with `cache-mode`:

| `cache-mode` | Backend | Use for |
| --- | --- | --- |
| `auto` (default) | `actions/cache` (remote) | any runner; works everywhere |
| `github` | `actions/cache` (remote) | self-hosted that wants remote caching |
| `local` | runner filesystem at `cache-path` | self-hosted with durable storage |
| `off` | none | disable persistence |

```yaml
- uses: sholdee/drydock/pr-action@vX.Y.Z
  with:
    version: vX.Y.Z
    cache-mode: local
    cache-path: /var/lib/drydock-cache # a path that persists across jobs
```

`local` mode defaults `cache-path` to `${RUNNER_TOOL_CACHE}/drydock-cache`, which
persists across jobs on a long-lived self-hosted runner. It cannot detect
whether a runner is actually persistent: GitHub's `runner.environment` only
distinguishes GitHub-hosted from self-hosted, not durable from ephemeral.
Ephemeral self-hosted runners — actions-runner-controller pods and `--ephemeral`
runners — get fresh storage each job, so `local` helps them only if you mount a
durable volume at `cache-path`; otherwise keep `auto` so the remote backend
repopulates the cache each run. The action emits a notice on self-hosted runners
that suggests `local`, and warns when `local` is used on a GitHub-hosted runner
(whose filesystem is always ephemeral).

With `local`, drydock's render-cache size cap and `drydock cache prune` bound
the directory rather than GitHub's cache budget; outside the action's own
prune step, eviction is yours to manage. A shared on-disk cache is readable by
every job on the runner, so use it only for trusted workloads.

The action automatically prunes the on-runner cache after each run when
`cache-mode: local` is in effect: the `cache-prune-max-size` input (default
`4Gi`, any Kubernetes quantity) caps the cache, and the prune step evicts
least-recently-used entries from the source and render caches until the total
is at or below the cap.
Pruning is automatically disabled when `offline: true` because evicting source
cache entries would hard-fail later offline runs (the `"offline cache miss"`
error has no self-heal path). Pruning is housekeeping and never fails the job:
if the installed drydock version does not yet support `cache prune --max-size`,
the step emits a notice and exits cleanly; any other prune failure emits a
warning. The render sweep inside the prune command runs at its 512 Mi default,
matching run-time behavior. Set `cache-prune-max-size: ""` to opt out entirely.
Assume one job per `cache-path` at a time: concurrent jobs sharing a cache-path
can race the prune step against in-flight renders.

## Input Behavior

Newline-delimited inputs are passed as repeated drydock flags:

- `changed-only-include`
- `changed-only-ignore`
- `discover-kustomize`
- `discover-ignore`
- `repo-map`
- `cache-restore-keys`
- `extra-test-args`
- `extra-diff-args`
- `extra-image-diff-args`

`repo-map` entries use `URL=PATH` form. `offline: "true"` disables source
network access and expects local files, repo maps, or existing caches to satisfy
renders.

Only use `extra-*` inputs with trusted workflow configuration. The action
passes them as arguments without `eval`, but they still change drydock behavior.

## Setup Action Inputs

| Input | Default | Purpose |
| --- | --- | --- |
| `version` | `latest` | Released drydock version to install. Accepts `latest`, `vX.Y.Z`, or bare `X.Y.Z`. |
| `install-dir` | `/usr/local/bin` | Directory to install the `drydock` binary into. |
| `release-repository` | `sholdee/drydock` | Repository that publishes drydock release artifacts. |
| `github-token` | unset | Optional GitHub token for downloading release artifacts. |
| `allow-unverified` | `false` | Allow installation when the release does not publish `checksums.txt`. |
| `cache-binary` | `true` | Restore and save the verified drydock release archive. |
| `cache-binary-key-suffix` | `v1` | Suffix for generated drydock binary cache keys. |

## Setup Action Outputs

| Output | Meaning |
| --- | --- |
| `version` | Resolved drydock version that was installed. |
| `install-dir` | Directory where the drydock binary was installed. |
| `binary-cache-hit` | Whether the drydock release archive was restored from cache and verified. |

## PR Action Inputs

### Install And Authentication

| Input | Default | Purpose |
| --- | --- | --- |
| `version` | `latest` | Released drydock version to install when `install` is `true`. |
| `install` | `true` | Install drydock before running. |
| `drydock-bin` | `drydock` | Binary name or path to run when `install` is `false`. |
| `install-dir` | `/usr/local/bin` | Directory to install the drydock binary into. |
| `cache-binary` | `true` | Restore and save the verified release archive when `install` is `true`. |
| `cache-binary-key-suffix` | `v1` | Suffix for generated binary cache keys. |
| `github-token` | `github.token` fallback | Token for checkout, release downloads, baseline fetch, and comments. |
| `github-app-client-id` | unset | GitHub App client ID used with `github-app-private-key` to mint an installation token. |
| `github-app-id` | unset | Legacy GitHub App ID fallback. Prefer `github-app-client-id`. |
| `github-app-private-key` | unset | GitHub App private key. |
| `release-repository` | `sholdee/drydock` | Repository that publishes drydock release artifacts. |

### Checkout And Commands

| Input | Default | Purpose |
| --- | --- | --- |
| `checkout` | `true` | Check out the pull request head before running drydock. |
| `fetch-depth` | `1` | Fetch depth for checkout. |
| `path` | `.` | Repository path to inspect for render tests. Self-repository source resolution reads the checkout's git metadata, so pointing this at a subdirectory disables it — keep the checkout root and scope work with `changed-only-include`, or add `repo-map`. |
| `repo` | `.` | Local Git repository path used for ref-based diffs. |
| `base-ref` | PR base branch | Baseline branch name for diff commands. Required outside pull request events when diff steps run. |
| `head-ref` | `HEAD` after checkout | Current Git ref for diff commands. |
| `run-test` | `true` | Run `drydock test apps`. |
| `run-diff` | `true` | Run `drydock diff apps`. |
| `run-image-diff` | `true` | Run `drydock diff images` reporting. This does not run image pull verification. |

### Render And Diff Options

| Input | Default | Purpose |
| --- | --- | --- |
| `skip-secrets` | `true` | Omit Secret resources from output and diffs. |
| `offline` | `false` | Disable source network access and use local files, repo maps, or existing caches. |
| `strict` | `false` | Promote diagnostics to errors. |
| `strict-changed-only` | `false` | Fail when changed-only input ownership is ambiguous or incomplete. |
| `changed-only` | drydock default | Override changed-only behavior with `true` or `false`. |
| `changed-only-include` | unset | Newline-delimited repository-relative globs considered by changed-only selection. |
| `changed-only-ignore` | unset | Newline-delimited repository-relative globs ignored by changed-only selection. |
| `show-ignored-fields` | `false` | Show drydock default ignored diff fields. |
| `discover-kustomize` | unset | Newline-delimited local Kustomize paths to render during Application discovery. |
| `discover-ignore` | unset | Newline-delimited repository-relative glob patterns excluded from discovery before decoding. Applies to test, diff, and image diff steps. |
| `repo-map` | unset | Newline-delimited repository URL mappings in `URL=PATH` form. |
| `kube-version` | unset | Kubernetes version for rendering capabilities. Overrides per-app `kubeVersion`. |
| `api-versions` | unset | Newline-delimited additional Kubernetes API versions for capability-gated rendering, unioned with per-app `apiVersions`. Accepts `group/version` or `group/version/Kind` form. |
| `parallelism` | unset | Maximum number of Applications to render concurrently. |
| `max-discovery-depth` | unset | Maximum recursive rendered Application discovery depth. |
| `enable-avp-compat` | `false` | Force argocd-vault-plugin placeholder redaction for native-rendered sources. |
| `enable-ksops-compat` | `false` | Render KSOPS kustomize generators as deterministic placeholder manifests without decryption. |

### SOPS/KSOPS Repositories

Repositories that use [KSOPS](https://github.com/viaduct-ai/kustomize-sops) for
secret management can enable `enable-ksops-compat: true` to render KSOPS
generator entries as placeholder manifests without any decryption keys or
network access. Secret structure and key names are preserved; encrypted values
become deterministic placeholders (`drydock-ksops-redacted-<12hex>`) that are
grep-able and unmistakably synthetic. Pair with `skip-secrets: true` to exclude
placeholder Secrets from diff comments entirely.

Note: value-only sops rotations render identically under this mode because
placeholders derive from key identity, not ciphertext. A rotation produces no
diff in drydock output even though the live secret changes; use an out-of-band
rotation audit rather than relying on drydock diff for this class of change.

### Plugins And Extra Arguments

| Input | Default | Purpose |
| --- | --- | --- |
| `enable-plugins` | `false` | Enable trusted exec and container plugin policy entries. |
| `plugin-policy-path` | unset | Trusted plugin policy path relative to the selected policy root. |
| `plugin-policy-ref` | unset | Git ref to use as the trusted plugin policy source. |
| `plugin-policy-repo` | unset | Local Git repository path used to resolve `plugin-policy-ref`. |
| `disable-plugin-policy` | `false` | Disable trusted plugin policy loading. |
| `extra-test-args` | unset | Newline-delimited additional trusted arguments for `drydock test apps`. |
| `extra-diff-args` | unset | Newline-delimited additional trusted arguments for `drydock diff apps`. |
| `extra-image-diff-args` | unset | Newline-delimited additional trusted arguments for `drydock diff images`. |

### Cache, Comments, Artifacts, And Failures

| Input | Default | Purpose |
| --- | --- | --- |
| `cache` | `true` | Restore and save drydock render caches for trusted runs. |
| `cache-mode` | `auto` | Cache backend. `auto`/`github` use the remote `actions/cache` backend; `local` persists at `cache-path` on the runner and skips `actions/cache`; `off` disables persistence. `cache: "false"` forces `off`. |
| `cache-prune-max-size` | `4Gi` | Size cap for the on-runner drydock source and render caches when `cache-mode: local`. Least-recently-used entries are pruned after each run until the total is at or below this Kubernetes quantity. Empty string disables pruning. Automatically disabled when `offline: true` to protect load-bearing cache entries. The render sweep inside the prune command runs at its 512 Mi default. An invalid quantity surfaces as a per-run warning, never a job failure. |
| `save-cache` | `true` | Save drydock render caches after trusted runs. |
| `cache-untrusted-restore` | `false` | Restore drydock render caches for fork pull requests. Cache save remains disabled for forks. |
| `cache-path` | runner temp directory | Local drydock cache root. |
| `cache-key-prefix` | `drydock` | Prefix for generated render cache keys. |
| `cache-key-suffix` | `v1` | Suffix for generated render cache keys. |
| `cache-key` | unset | Full cache key override. |
| `cache-restore-keys` | unset | Newline-delimited cache restore key override. |
| `comment-mode` | `both` | Pull request comment mode: `none`, `diff`, `images`, or `both`. |
| `comment-empty` | `false` | Comment even when the corresponding diff is empty. |
| `comment-continue-on-error` | `true` | Do not fail the workflow when pull request commenting fails. |
| `diff-max-bytes` | `60000` | Maximum rendered diff comment bytes; larger values are clamped to GitHub's comment budget. |
| `upload-artifacts` | `true` | Upload full diff and image report artifacts when they are non-empty. |
| `artifact-retention-days` | `30` | Retention days for uploaded artifacts. |
| `diff-artifact-name` | generated | Artifact name override for rendered manifest diffs. |
| `image-artifact-name` | generated | Artifact name override for added image output. |
| `fail-on-render-error` | `true` | Fail the action when `drydock test apps` fails. |
| `fail-on-diff` | `false` | Fail the action when rendered manifest differences are detected. |
| `fail-on-image-diff` | `false` | Fail the action when rendered image differences are detected. |

## PR Action Outputs

| Output | Meaning |
| --- | --- |
| `has-diff` | Whether rendered manifest differences were detected. |
| `has-images` | Whether current-only image references were detected. |
| `has-image-diff` | Whether any image reference difference was detected. |
| `render-status` | Render test status: `passed`, `failed`, or `skipped`. |
| `diff-path` | Local path to the rendered manifest diff file. |
| `diff-html-path` | Local path to the Full Rendered Diff View file. |
| `images-path` | Local path to the current-only image references file. |
| `diff-artifact-name` | Rendered manifest diff artifact name. |
| `diff-html-artifact-name` | Rendered manifest diff HTML artifact name. |
| `image-artifact-name` | Added image artifact name. |
| `trusted-context` | Whether the action considered this event trusted for cache save and comments. |
