---
title: Getting Started
---

Use drydock when you want to inspect Argo CD desired state from repository
contents before a controller sees it. The default commands do not call a live
Argo CD server or Kubernetes cluster, and use native renderers for the common
directory, Kustomize, Helm, and Jsonnet paths.

## Install

Install the latest Linux/macOS release with Homebrew:

```bash
brew install sholdee/tap/drydock
```

Homebrew installs shell completions automatically.

{{< details summary="Install Script" >}}

```bash
curl -fsSL https://raw.githubusercontent.com/sholdee/drydock/main/scripts/install-drydock.sh -o install-drydock.sh
bash install-drydock.sh --yes
```

The script verifies release checksums, verifies Sigstore bundles when
available, installs the `drydock` binary, and attempts shell completion
installation. Pin a release with `--version vX.Y.Z`.

Pipe form:

```bash
curl -fsSL https://raw.githubusercontent.com/sholdee/drydock/main/scripts/install-drydock.sh | bash -s -- --yes
```

Pinned pipe form:

```bash
curl -fsSL https://raw.githubusercontent.com/sholdee/drydock/main/scripts/install-drydock.sh | bash -s -- --version vX.Y.Z --yes
```

Use `--no-completions` when completions should be installed manually.

{{< /details >}}

For GitOps repository and CI pinning, use `mise` with the GitHub backend:

```toml
[tools]
"github:sholdee/drydock[exe=drydock]" = "vX.Y.Z"
```

{{< details summary="GitHub Actions" >}}

Use `setup-action` when your workflow owns the drydock commands:

```yaml
- uses: sholdee/drydock/setup-action@main
  with:
    version: vX.Y.Z
```

Use `pr-action` for the standard pull request render test, manifest diff,
image diff, artifacts, cache, and comment workflow. See
[GitHub Actions](/workflows/github-actions/) for inputs and permissions.

{{< /details >}}

{{< details summary="Download A Binary" >}}

Download Linux and macOS `amd64` or `arm64` archives from
[GitHub Releases](https://github.com/sholdee/drydock/releases/latest). Verify
the archive with `checksums.txt`, then place `drydock` on `PATH`.

{{< /details >}}

{{< details summary="Docker / GHCR" >}}

```bash
docker run --rm -v "$PWD:/workspace:ro" ghcr.io/sholdee/drydock:vX.Y.Z test apps --path /workspace
```

{{< /details >}}

{{< details summary="Go Install" >}}

```bash
go install github.com/sholdee/drydock/cmd/drydock@latest
```

{{< /details >}}

Manual binary installs can generate completions with:

```bash
drydock completion zsh
drydock completion bash
drydock completion fish
```

## First Commands

Run these from the GitOps repository you want to inspect:

```bash
drydock test apps --path .
drydock get apps --path .
drydock diag --path .
```

Start with `drydock test apps --path .`. It discovers Applications and renders
them without printing manifest bodies, which makes it the fastest first check
for render failures. `get apps` shows discovered Applications. `diag` uses the
same discovery and render validation path, then reports repository diagnostics.

If you are setting this up for CI, go to [GitHub Actions](/workflows/github-actions/)
next.

## Compare Changes

Compare the current tree against a baseline worktree:

```bash
git worktree add ../baseline main
drydock diff apps --path . --path-orig ../baseline
drydock diff images --path . --path-orig ../baseline -o markdown
```

Or compare local Git refs without creating a second checkout:

```bash
drydock diff apps --repo . --ref HEAD --ref-orig main
drydock diff images --repo . --ref HEAD --ref-orig main -o markdown
```

## Runtime Offline Does Not Mean Source Offline

drydock is runtime-offline: it does not depend on live Kubernetes or Argo CD.
Declared Git, HTTP Helm, OCI Helm, and remote Kustomize sources may still be
fetched into explicit caches during render commands.

Use `--offline` when a run must use only local files, repo maps, and existing
cache entries:

```bash
drydock test apps --path . --offline
drydock diff apps --path . --path-orig ../baseline --offline
```

For full command coverage, see the [CLI usage guide](/docs/usage/). For source
fetching, cache, and auth details, see [Source acquisition](/docs/source-acquisition/).
