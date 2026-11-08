#!/usr/bin/env bash
set -euo pipefail

# The OCI registry bridge needs an /etc/hosts entry for
# argocd-parity-registry.argocd-parity.svc.cluster.local. The script appends
# it with sudo (passwordless on CI; an interactive prompt locally) and removes
# its own marked line on exit. A pre-existing entry for that hostname —
# however it got there — skips sudo entirely, which is the local escape hatch
# for environments without sudo. On macOS the bridge uses local port 5443
# (AirPlay owns 5000) and certificate generation sticks to the
# LibreSSL-compatible config-file subjectAltName form.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

ARGOCD_MODULE="github.com/argoproj/argo-cd/v3"
FIXTURE_REPO_URL="git://argocd-parity-git.argocd-parity.svc.cluster.local/repo.git"
FIXTURE_REPO_PATH="${REPO_ROOT}/testdata/argocd-parity/repo"
IGNORE_FILE="${REPO_ROOT}/testdata/argocd-parity/compare-ignore.yaml"
SIDECAR_PATH="${REPO_ROOT}/testdata/argocd-parity/sidecar"
# Cluster Secrets for the cluster generator ApplicationSets. Argo CD reads them
# from the argocd namespace. drydock reads the same clusters from the provider
# fixture, which every drydock capture passes with --appset-provider-fixture.
CLUSTER_SECRETS_PATH="${REPO_ROOT}/testdata/argocd-parity/cluster-secrets"
APPSET_PROVIDER_FIXTURE="${REPO_ROOT}/testdata/argocd-parity/appset-provider-fixture.yaml"
CLUSTER_SECRET_NAMES=(
  parity-cluster-alpha
  parity-cluster-beta
)
PROJECT_POLICY_REPO_PATH="${REPO_ROOT}/testdata/argocd-project-policy/repo"
PROJECT_POLICY_EXPECTED="${REPO_ROOT}/testdata/argocd-project-policy/expected.yaml"
# Cluster Secrets that exist only in the live Argo CD. drydock never sees
# them, so the cases that use them test drydock's cannot-tell-offline result.
PROJECT_POLICY_LIVE_CLUSTER_SECRETS="${REPO_ROOT}/testdata/argocd-project-policy/live-cluster-secrets.yaml"
PROJECT_POLICY_LIVE_CLUSTER_SECRET_NAME="project-policy-live-scoped-cluster"
# Later project-policy phases. Each one changes cluster-wide Argo CD state
# that would break the cases before it, so each has its own fixture repo,
# expected file and drydock run, and runs after the phase before it.
PROJECT_POLICY_IN_CLUSTER_SECRET_DIR="${REPO_ROOT}/testdata/argocd-project-policy/in-cluster-secret"
PROJECT_POLICY_IN_CLUSTER_SECRET_NAME="project-policy-in-cluster-secret"
PROJECT_POLICY_IN_CLUSTER_DISABLED_DIR="${REPO_ROOT}/testdata/argocd-project-policy/in-cluster-disabled"
# Decode failures for directory sources and for Helm, Kustomize, and plugin
# output, and Helm file parameters that fail to resolve or to read. Each
# Application in this namespace must fail on both sides: Argo
# CD with a ComparisonError condition and drydock with a render failure,
# both naming the same file, or the object in it when the Argo CD message
# names no file. They live outside the argocd namespace
# so the main inventory and manifest comparisons never see them. The expected
# file lists the cases and the message each side must show. Files that both
# sides skip or render live in parity-directory-decode-skips and
# parity-directory-decode-jsonnet, which the main manifest comparison covers.
DIRECTORY_DECODE_NAMESPACE="parity-directory-decode"
DIRECTORY_DECODE_EXPECTED="${REPO_ROOT}/testdata/argocd-parity/directory-decode-expected.yaml"
# Set by run_directory_decode_smoke from DIRECTORY_DECODE_EXPECTED.
DIRECTORY_DECODE_APPLICATIONS=()
# The cases with source: plugin. Their drydock capture carries the same
# plugin flags as PLUGIN_APPLICATIONS.
DIRECTORY_DECODE_PLUGIN_APPLICATIONS=()
# A second Git repository for the out-of-bounds symlink case. The Argo CD
# repo-server fails every Application from a repository that holds such a
# symlink, so it cannot live in the main fixture repository. The smoke adds
# the symlink when it builds the repository, so drydock's own repository
# never holds it. Its Application lives in the directory decode namespace.
OOB_SYMLINK_REPO_URL="git://argocd-parity-git.argocd-parity.svc.cluster.local/oob-symlink.git"
OOB_SYMLINK_FIXTURE_PATH="${REPO_ROOT}/testdata/argocd-parity/oob-symlink-repo"
# The live diff smoke checks drydock's diff normalization against Argo CD
# sync status. Its Git repository has two tagged commits: live-diff/repo is
# the first, and live-diff/change holds the files that the second commit
# changes. Argo CD syncs each Application at the first tag, then compares
# the second tag with the live objects. drydock diffs the same two tags. The
# expected file lists the sync status of every resource. The settings file
# is an argocd-cm with only the ignore settings: drydock reads it from the
# repository, and the smoke applies its keys to the live argocd-cm.
LIVE_DIFF_REPO_URL="git://argocd-parity-git.argocd-parity.svc.cluster.local/live-diff.git"
LIVE_DIFF_FIXTURE_PATH="${REPO_ROOT}/testdata/argocd-parity/live-diff"
LIVE_DIFF_EXPECTED="${LIVE_DIFF_FIXTURE_PATH}/expected.yaml"
LIVE_DIFF_SETTINGS="settings/argocd-cm.yaml"
LIVE_DIFF_REVISION_A="live-diff-a"
LIVE_DIFF_REVISION_B="live-diff-b"
LIVE_DIFF_NAMESPACES=(parity-live-diff parity-live-diff-legacy)
# The field manager that owns the live argocd-cm keys from LIVE_DIFF_SETTINGS.
LIVE_DIFF_FIELD_MANAGER="drydock-live-diff-smoke"
# Set by run_live_diff_smoke from LIVE_DIFF_EXPECTED: the Applications that
# the smoke syncs and compares, and the Applications whose comparison must
# fail because Argo CD cannot build their ignore rules.
LIVE_DIFF_APPLICATIONS=()
LIVE_DIFF_ERROR_APPLICATIONS=()
# Set by prepare_live_diff_repo: the local git repo of LIVE_DIFF_REPO_URL,
# which drydock diffs.
LIVE_DIFF_WORK=""
# Set to "true" while the live diff Applications and argocd-cm keys exist.
# The cleanup trap reads it.
LIVE_DIFF_STATE_CHANGED="false"
# The source type gate phase turns Helm, Kustomize, and Jsonnet off in the
# live argocd-cm and compares the Applications of its fixture repo, which
# render as plain directories then. drydock reads the same keys from the
# argocd-cm.yaml in that repo. The smoke serves the repo from the main Git
# repository, and the phase runs last because the keys change how Argo CD
# renders every Application.
SOURCE_TYPE_GATES_REPO_PATH="${REPO_ROOT}/testdata/argocd-parity/source-type-gates/repo"
SOURCE_TYPE_GATES_MANIFESTS="${SOURCE_TYPE_GATES_REPO_PATH}/source-type-gates"
SOURCE_TYPE_GATES_SETTINGS='{"data":{"helm.enable":"false","kustomize.enable":"True","jsonnet.enable":"no"}}'
SOURCE_TYPE_GATES_RESET='{"data":{"helm.enable":null,"kustomize.enable":null,"jsonnet.enable":null}}'
SOURCE_TYPE_GATES_APPLICATIONS=(
  parity-gates-helm-discovered
  parity-gates-helm-explicit
  parity-gates-helm-chart-dir
  parity-gates-helm-chart-only
  parity-gates-kustomize-discovered
  parity-gates-kustomize-explicit
  parity-gates-jsonnet
)
# Set to "true" while the live argocd-cm turns the tools off. The cleanup
# trap reads it.
SOURCE_TYPE_GATES_STATE_CHANGED="false"
# Set by save_source_type_gate_keys: the merge patch that restores the live
# values of the keys.
SOURCE_TYPE_GATES_RESTORE=""
# The kustomize build options phases set kustomize.buildOptions on the live
# argocd-cm to the value in the argocd-cm.yaml of each phase repo, apply the
# Applications of that repo, and check them against the expected.psv of the
# phase: a render line compares the manifests, and a fail line compares the
# ComparisonError of Argo CD and the drydock error. drydock reads the same
# argocd-cm.yaml. The phases run after the source type gate phase, and the
# last one restores the value of the fixture install.
KUSTOMIZE_BUILD_OPTIONS_PATH="${REPO_ROOT}/testdata/argocd-parity/kustomize-build-options"
KUSTOMIZE_BUILD_OPTIONS_PHASES=(alpha no-helm unknown-flag output-file)
# The value that install_argocd sets.
KUSTOMIZE_BUILD_OPTIONS_DEFAULT="--enable-helm"
# Set to "true" while the live argocd-cm holds the value of a phase. The
# cleanup trap reads it.
KUSTOMIZE_BUILD_OPTIONS_STATE_CHANGED="false"
OCI_ARTIFACT_PATH="${REPO_ROOT}/testdata/argocd-parity/oci-artifact"
OCI_APPSET_ARTIFACT_PATH="${REPO_ROOT}/testdata/argocd-parity/oci-appset-artifact"
OCI_REGISTRY_HOST="argocd-parity-registry.argocd-parity.svc.cluster.local"
OCI_REGISTRY_PORT="5443"
# Multi-arch index digest for the Docker-official registry:2.8.3 (covers CI
# amd64 and local darwin arm64). Pulls try each mirror in order, retrying with
# backoff; every mirror is pinned to the same digest, so all serve identical
# content. The ECR mirror comes first (no Docker Hub 429 surface), but its
# anonymous quota is per egress IP and CI runners share IPs, so it can answer
# "toomanyrequests: Data limit exceeded" for a whole job.
OCI_REGISTRY_IMAGE_DIGEST="sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
OCI_REGISTRY_IMAGE_REPOSITORIES=(
  "public.ecr.aws/docker/library/registry"
  "mirror.gcr.io/library/registry"
  "docker.io/library/registry"
)
PULL_RETRY_ATTEMPTS=4
PULL_RETRY_INITIAL_DELAY_SECONDS=5
# Multi-arch index digest for the Docker-official alpine:3.23.6, identical on
# every mirror below. The container-plugin fixture runs it twice: as the
# repo-server CMP sidecar image (re-tagged and kind-loaded) and as drydock's
# container-engine image. The first mirror is the reference the committed
# policy (testdata/argocd-parity/repo/.drydock/plugins.yaml) names.
PARITY_ALPINE_IMAGE_DIGEST="sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0"
PARITY_ALPINE_IMAGE_REPOSITORIES=(
  "public.ecr.aws/docker/library/alpine"
  "mirror.gcr.io/library/alpine"
  "docker.io/library/alpine"
)
# Set by prepare_parity_alpine_image: the digest reference the pull actually
# succeeded with, which is the only name the local image store has for it.
PARITY_ALPINE_POLICY_IMAGE=""
CONTAINER_PLUGIN_APPLICATION="parity-plugin-container"
# argocd-vault-plugin release binary for the AVP sidecar, built into an image
# on top of the pinned alpine above. Release binaries are static (CGO off),
# so they run on alpine's musl. Pinned per architecture by sha256.
AVP_VERSION="v1.18.1"
AVP_SHA256_LINUX_AMD64="9e8e301c0d4e01f050b4df1e47a4137eb0ba459944ed2c32b53ef1571eb93c40"
AVP_SHA256_LINUX_ARM64="dd6e4d7db290c2f16aeb142941cd727e623b3faa25258c7743d9ddfac39db1c3"
AVP_APPLICATION="parity-avp"
# The Secrets-only AVP Application. `argocd app manifests` masks Secret data
# and stringData as ++++++++, so it is kept out of the masked comparison and
# compared through the kubectl-exec oracle alone.
AVP_SECRET_APPLICATION="parity-avp-secret"
# Applications the oracle renders with `argocd-vault-plugin generate` inside
# the sidecar and compares unmasked against drydock.
AVP_ORACLE_APPLICATIONS=("${AVP_APPLICATION}" "${AVP_SECRET_APPLICATION}")
# drydock-redacted- markers testdata/argocd-parity/repo/workloads/avp must
# render on each side: one per substituted placeholder occurrence (the two
# inside the nested list stay verbatim).
AVP_EXPECTED_MARKERS=16
# Markers testdata/argocd-parity/repo/workloads/avp-secret must render on
# each oracle side: four literal in stringData plus five inside decoded
# base64 data values.
AVP_SECRET_EXPECTED_MARKERS=9
OCI_ARTIFACT_REPOSITORY="parity/config"
OCI_ARTIFACT_TAG="v1.0.0"
OCI_APPLICATION="parity-oci-config"
# The ApplicationSet OCI generator fixture: a second artifact whose directory
# tree the parity-oci-* ApplicationSets list and read. It holds app
# directories, a hidden directory, nested directories, symlinked
# directories, and JSON and YAML param files, one of them a list of objects.
# Every Application those ApplicationSets generate renders from the same
# artifact.
OCI_APPSET_ARTIFACT_REPOSITORY="parity/appset"
OCI_APPSET_ARTIFACT_TAG="v1.0.0"
OCI_APPSETS=(
  parity-oci-directories
  parity-oci-directories-fasttemplate
  parity-oci-files
  parity-oci-files-fasttemplate
  parity-oci-matrix
)
# One generated Application to warm drydock's OCI cache with. Its build
# evaluates the OCI generator and renders the artifact, so the offline
# capture loop finds both in the cache.
OCI_APPSET_WARM_APPLICATION="parity-oci-dir-alpha"
# The OCI Helm chart fixture: a nested chart name under a scheme-less
# host:port repoURL, the shape Argo CD dispatches to `helm pull
# oci://<repo>/<chart>` and drydock now accepts too.
OCI_CHART_PATH="${REPO_ROOT}/testdata/argocd-parity/oci-chart/parity-nested-chart"
OCI_CHART_REPOSITORY="parity/nested"
OCI_CHART_APPLICATION="parity-oci-helm-nested"
# The same pushed chart addressed as an OCI artifact: repoURL
# oci://<host:port>/parity/nested/parity-nested-chart with chart: set.
# Argo CD's repo-server dispatches on IsOCI before IsHelm
# (reposerver/repository/repository.go:358-364), pulls the repoURL at the
# tag and never reads chart; drydock does the same and warns
# source.oci-chart-ignored. parity-oci-helm-chart-ignored is Argo-valid only
# when applied declaratively (argocd app create would reject it).
OCI_CHART_IGNORED_APPLICATION="parity-oci-helm-chart-ignored"
# Lists two valid components under ignoreMissingComponents. Argo CD v3.6.1
# leaves its component loop after the first valid one
# (util/kustomize/kustomize.go:346-372) and drops the second. drydock renders
# the same and warns kustomize.ignore-missing-components-dropped.
COMPONENTS_DROPPED_APPLICATION="parity-kustomize-generators"
# Sets spec.sourceHydrator. Argo CD compares only the sync source
# (ApplicationSpec.GetSources): syncSource.path at syncSource.targetBranch.
# The hydrator is not enabled in the smoke cluster, so
# prepare_fixture_git_image commits the hydrated content on
# SOURCE_HYDRATOR_BRANCH, where it differs from main at the same path. The
# drydock capture maps the fixture URL to FIXTURE_GIT_WORK, which has main
# checked out, so drydock must read the branch from Git. It also warns
# source.hydrator-sync-source.
SOURCE_HYDRATOR_APPLICATION="parity-source-hydrator"
SOURCE_HYDRATOR_BRANCH="parity-hydrated"
SOURCE_HYDRATOR_BRANCH_CONFIGMAP="parity-source-hydrator-hydrated-branch"
# Objects in namespaces that Argo CD does not reconcile
# (repo/namespace-gate). The Application lives in NAMESPACE_DISABLED, which
# application.namespaces does not list. The ApplicationSet lives in
# TENANT_NAMESPACE, which applicationsetcontroller.namespaces does not list,
# so its Application never exists. drydock skips both with
# discovery.namespace-not-enabled.
NAMESPACE_DISABLED="parity-namespace-disabled"
NAMESPACE_DISABLED_APPLICATION="parity-namespace-disabled"
NAMESPACE_DISABLED_APPLICATIONSET="parity-appset-namespace-disabled"
# An Application that the CRD schema rejects: its
# spec.sourceHydrator.syncSource.path is ".". It lives outside repo/, so the
# main inventory never sees it (assert_crd_rejected_application).
CRD_REJECTED_DIR="${REPO_ROOT}/testdata/argocd-parity/crd-rejected"
# A generated Application whose AppProject is missing, a recorded
# divergence that run_missing_project_smoke checks on its own directory.
MISSING_PROJECT_DIR="${REPO_ROOT}/testdata/argocd-parity/missing-project"
MISSING_PROJECT_APPLICATIONSET="parity-validation-missing-project"
MISSING_PROJECT_MESSAGE="application references project parity-missing-project which does not exist"
CRD_REJECTED_APPLICATION="parity-crd-sync-path-dot"
CRD_REJECTED_MESSAGE="Invalid value: \".\": spec.sourceHydrator.syncSource.path in body should match"
# The HTTP Helm repository fixture. A second container in the registry Pod
# serves it on its own port, so it shares the registry hostname and its
# /etc/hosts entry, and the registry port-forward also forwards this port.
# HELM_REPO_CHARTS lists one "name version" archive for each entry of
# helm-repo/index.yaml.
HELM_REPO_PORT="18879"
# The busybox-extras release of PARITY_ALPINE_IMAGE_DIGEST (alpine 3.23.6),
# whose httpd serves the HTTP Helm repository. It pins the upstream version
# only, because Alpine drops old package releases from its mirrors.
BUSYBOX_EXTRAS_VERSION="1.37"
HELM_REPO_URL="http://argocd-parity-registry.argocd-parity.svc.cluster.local:${HELM_REPO_PORT}"
HELM_REPO_PATH="${REPO_ROOT}/testdata/argocd-parity/helm-repo"
HELM_REPO_CHARTS=(
  "parity-range 1.0.0"
  "parity-range 1.2.0"
  "parity-range 1.2.3"
  "parity-range 1.3.0-rc.1"
  "parity-range 2.0.0"
  "parity-extra 1.0.0"
)
# Applications that read the HTTP Helm repository: chart sources with a
# version range, local charts whose dependencies helm dependency build
# fetches, and a file parameter URL. The last one is a version range on the
# OCI registry, whose tag list must be recorded too. warm_drydock_helm_repo_cache renders
# each once online, so the offline captures find the charts, the recorded
# version lists, and the remote files in the caches.
HELM_REPO_APPLICATIONS=(
  parity-helm-range-wildcard
  parity-helm-range-constraint
  parity-helm-range-short
  parity-helm-range-prerelease
  parity-helm-dependency-range
  parity-helm-dependency-partial
  parity-helm-file-parameter-url
  parity-oci-helm-range
)
# Applications whose source holds a file that prepare_fixture_git_image
# adds to FIXTURE_GIT_WORK: the vendored archive
# charts/helm-dependency-partial/charts/parity-range-1.2.0.tgz. Their drydock
# captures map the fixture URL to FIXTURE_GIT_WORK, as Argo CD reads the
# served copy of it.
HELM_REPO_GIT_WORK_APPLICATIONS=(
  parity-helm-dependency-partial
)
OCI_HOSTS_MARKER="drydock-argocd-parity-smoke"
HOSTS_ENTRY_ADDED="false"
OUT_DIR="${REPO_ROOT}/argocd-parity-smoke"
KEEP_CLUSTER="false"
CREATE_CLUSTER="true"
RUN_PROJECT_POLICY_SMOKE="true"
# Set to "true" once a project-policy phase starts to change state that every
# Application shares. The cleanup trap reads it.
PROJECT_POLICY_SHARED_STATE_CHANGED="false"
CLUSTER_NAME="drydock-argocd-parity-${GITHUB_RUN_ID:-$$}"
DRYDOCK_CMD=(go run ./cmd/drydock)
PORT_FORWARD_PID=""
REGISTRY_PORT_FORWARD_PID=""

APPLICATIONS=(
  parity-oci-config
  parity-oci-helm-nested
  parity-oci-helm-chart-ignored
  parity-directory
  parity-directory-edges
  parity-helm-release-namespace
  parity-helm-default-namespace
  parity-helm-capabilities
  parity-helm-file-parameters
  parity-helm-parameters
  parity-helm-render-options
  parity-helm-values
  parity-helm-valuefiles-glob
  parity-jsonnet
  parity-jsonnet-edges
  parity-kustomize
  parity-kustomize-helm
  parity-multi-source-ref-values
  parity-ref-only-source
  parity-repeated-resource
  parity-skip-file
  parity-sources-precedence
  parity-tracking
  parity-git-alpha
  parity-git-beta
  parity-git-file-alpha
  parity-git-file-beta
  parity-list-alpha
  parity-list-beta
  parity-merge-alpha
  parity-merge-beta
  parity-matrix-dev-api
  parity-matrix-dev-worker
  parity-matrix-prod-api
  parity-matrix-prod-worker
  parity-multi-source-last-wins
  parity-kustomize-options
  parity-selector-beta-prod
  parity-template-patch
  parity-helm-subchart
  parity-helm-crds-default
  parity-helm-values-precedence
  parity-helm-options-edge
  parity-source-overrides
  parity-ref-fileparam
  parity-helm-file-parameter-symlinks
  parity-helm-file-parameter-symlinks-override
  parity-helm-params-edge
  parity-directory-flat
  parity-directory-forced
  parity-directory-glob
  parity-directory-extension-filter
  parity-directory-decode-skips
  parity-directory-decode-jsonnet
  parity-directory-symlinks
  parity-kustomize-variants
  parity-kustomize-labels
  parity-kustomize-generators
  parity-kustomize-helm-capabilities
  parity-kustomize-helm-cross-namespace
  parity-kustomize-helm-defaults
  parity-kustomize-helm-shared-api-versions
  parity-ft-alpha
  parity-ft-beta
  parity-fn-gamma-one
  parity-helm-null-default
  parity-helm-block-scalars
  parity-helm-generated-decode
  parity-oci-dir-alpha
  parity-oci-dir-beta
  parity-oci-dir-delta
  parity-oci-dir-gamma
  parity-oci-ft-alpha
  parity-oci-ft-gamma
  parity-oci-ft-skipped
  parity-oci-file-east-east
  parity-oci-file-hidden-hidden
  parity-oci-file-north-params
  parity-oci-file-south-params
  parity-oci-file-west-mirror
  parity-oci-file-west-west
  parity-oci-ftfile-root-one
  parity-oci-ftfile-root-two
  parity-oci-mx-dev-delta
  parity-oci-mx-dev-gamma
  parity-oci-mx-prod-delta
  parity-oci-mx-prod-gamma
  parity-plugin-env
  parity-plugin-container
  parity-avp
  parity-avp-secret
  # spec.source.kustomize options as the kustomize edit commands of Argo CD
  # apply them: images, commonLabels and commonAnnotations, and components.
  parity-kustomize-source-images
  parity-kustomize-source-labels
  parity-kustomize-source-labels-without-selector
  parity-kustomize-source-components
  # ApplicationSet name validation: the empty-name and duplicated-name
  # Applications must be absent on both sides, so only these survive.
  parity-empty-name-alpha
  parity-empty-name-beta
  parity-duplicate-name-unique
  # parity-empty-project-dropped renders an empty spec.project, which the
  # ApplicationSet controller cannot find, so only this one exists.
  parity-empty-project-kept
  # A matrix child's own selector is not interpolated, so no
  # parity-matrix-selector-templated-* name exists. A selector inside a
  # merge child is, so it keeps only inner-dev.
  parity-matrix-selector-inner-dev
  # One spec.generators entry that sets both list and matrix generates
  # from both.
  parity-two-fields-list
  parity-two-fields-matrix
  # Cluster generator over cluster Secrets with reserved annotations.
  parity-cluster-alpha
  parity-cluster-beta
  parity-cluster-ft-alpha
  parity-cluster-ft-beta
  # parity-cluster-sprig-map-function calls hasKey on the map[string]string
  # cluster annotations, so its parity-cluster-haskey-* Applications must be
  # absent on both sides. APPLICATIONSET_ERRORS below proves that Argo CD
  # rejected it, not that it has not reconciled yet.
  # Generator selector on the []string path.segments param, which Argo CD
  # flattens to one "[a b c]" value. The parity-segments-index-* Applications
  # select on path.segments.3 and must be absent on both sides.
  parity-segments-alpha
  # RollingSync strategies. InvalidRolloutConfig never blocks Applications,
  # so every one of them exists. An empty steps list stops the whole
  # ApplicationSet, so no parity-rolling-sync-no-steps-* name is here.
  parity-rolling-sync-valid-dev
  parity-rolling-sync-valid-prod
  parity-rolling-sync-operator-dev
  parity-rolling-sync-duplicate-dev
  parity-rolling-sync-empty-step-dev
  parity-rolling-sync-max-update-dev
  # ARGOCD_APP_REVISION, _SHORT and _SHORT_8 (see APP_REVISION_GIT_APPLICATIONS).
  parity-app-revision-helm
  parity-app-revision-kustomize
  parity-app-revision-jsonnet
  parity-app-revision-plugin
  parity-app-revision-multi-source
  parity-app-revision-oci
  parity-app-revision-helm-repo
  parity-app-revision-annotated-tag
  parity-app-revision-annotated-tag-single
  # spec.project "". Argo CD reads it as the default project in
  # ARGOCD_APP_PROJECT_NAME and in the inventory comparison.
  parity-empty-project
  # kustomization.yaml and traefik-HelmChart.yaml in one directory: the
  # last file whose name ends in Chart.yaml or is a kustomization file
  # decides the type, so Argo CD renders it with Helm.
  parity-helm-chart-file-name
  # spec.sourceHydrator: Argo CD renders only the sync source.
  parity-source-hydrator
  # Helm value file paths: a repeated file, a symlink, a symlinked
  # directory, a path from the repository root, and a glob over a symlink.
  # Then a $ref glob, and a glob that matches nothing under
  # ignoreMissingValueFiles.
  parity-helm-value-file-paths
  parity-helm-value-file-ref-glob
  parity-helm-value-file-glob-missing
  # HELM_REPO_APPLICATIONS, and a vendored dependency of another version.
  parity-helm-file-parameter-url
  parity-helm-range-wildcard
  parity-helm-range-constraint
  parity-helm-range-short
  parity-helm-range-prerelease
  parity-helm-dependency-range
  parity-helm-dependency-partial
  parity-helm-dependency-vendored-version
  # A version range on the schemeless OCI chart of the registry.
  parity-oci-helm-range
)

# Applications whose Git sources read the ARGOCD_APP_REVISION variables. The
# repo-server reports the commit SHA of the fixture repository it serves.
# drydock reports the HEAD commit of the --repo-map target, so their drydock
# capture maps the fixture URL to FIXTURE_GIT_WORK, the Git repository that
# the served bare repository is cloned from. Both sides then see the same
# commit. The other captures keep FIXTURE_REPO_PATH, which is no Git
# repository top, so drydock keeps the spec targetRevision there.
APP_REVISION_GIT_APPLICATIONS=(
  parity-app-revision-helm
  parity-app-revision-kustomize
  parity-app-revision-jsonnet
  parity-app-revision-plugin
  parity-app-revision-multi-source
  parity-app-revision-annotated-tag
  parity-app-revision-annotated-tag-single
)
# The annotated tag that prepare_fixture_git_image puts on the fixture
# commit. A multi-source Application at this tag gets the git ls-remote
# result, a single-source Application the checked-out commit. Argo CD's
# listRemote peels annotated tags, so both get the commit SHA and never the
# SHA of the tag object (APP_REVISION_TAG_OBJECT_ABSENT).
APP_REVISION_ANNOTATED_TAG="parity-app-revision-annotated"
# The value each revision Application must render on the Argo CD side, as
# "name|text" pairs, so that both sides rendering an unresolved value cannot
# pass the comparison. @GIT_HEAD@ stands for the commit of FIXTURE_GIT_WORK,
# and "sha256:" for the start of an OCI manifest digest. The text is the part
# after the first "|".
APP_REVISION_EXPECTED=(
  "parity-app-revision-helm|@GIT_HEAD@"
  "parity-app-revision-kustomize|@GIT_HEAD@"
  "parity-app-revision-jsonnet|@GIT_HEAD@"
  "parity-app-revision-plugin|@GIT_HEAD@"
  "parity-app-revision-multi-source|@GIT_HEAD@"
  "parity-app-revision-multi-source|sha256:"
  "parity-app-revision-oci|sha256:"
  "parity-app-revision-helm-repo|1.0.0|1.0.0|1.0.0"
  "parity-app-revision-annotated-tag|@GIT_HEAD@"
  "parity-app-revision-annotated-tag-single|@GIT_HEAD@"
)
# Applications whose Argo CD capture must not hold the SHA of the
# APP_REVISION_ANNOTATED_TAG tag object.
APP_REVISION_TAG_OBJECT_ABSENT=(
  parity-app-revision-annotated-tag
  parity-app-revision-annotated-tag-single
)

# ApplicationSets that Argo CD must reject, as "name|text|code|path"
# entries. Before the inventory checks, the smoke waits for each one to have
# an ErrorOccurred condition with status True and a message that contains
# the text. Without this wait, a missing Application could also mean that
# the ApplicationSet has not reconciled yet. When an entry has a code,
# drydock must warn with that code for the ApplicationSet file at path, and
# the warning must contain the same text (compare_applicationset_errors).
# The template render failure has no code here because its drydock warning
# spans several lines.
#
# The parity-gen-error-* ApplicationSets each fail in one generator. Argo CD
# then generates nothing for the whole ApplicationSet, so none of their
# Applications are in APPLICATIONS. run_missing_project_smoke checks the
# missing AppProject case on its own.
#
# A matrix or merge param type clash cannot be listed here. Argo CD v3.6.1
# panics in that merge, and Reconcile recovers the panic and returns before
# it sets any condition
# (applicationset/controllers/applicationset_controller.go:123-133), so there
# is no condition to wait for.
APPLICATIONSET_ERRORS=(
  "parity-cluster-sprig-map-function|got map[string]string||"
  "parity-gen-error-elements-yaml|error unmarshling decoded ElementsYaml|appset.generator-failed|applicationsets/generator-error-elements-yaml.yaml"
  "parity-gen-error-empty-child|failed to get params for second generator in the matrix generator: child generator generated no parameters|appset.generator-failed|applicationsets/generator-error-empty-child.yaml"
  "parity-gen-error-nested-too-deep|failed to get params for second generator in the matrix generator: child generator returned an error on parameter generation: failed to get params for second generator in the matrix generator: child generator generated no parameters|appset.generator-failed|applicationsets/generator-error-nested-too-deep.yaml"
  "parity-gen-error-selector|error parsing label selector: \"Sometimes\" is not a valid pod selector operator|appset.generator-failed|applicationsets/generator-error-selector.yaml"
  "parity-gen-error-merge-keys|error getting param sets by merge key: the parameters from a generator were not unique by the given mergeKeys, Merge requires all param sets to be unique. Duplicate key was {\"suffix\":\"merged\"}|appset.generator-failed|applicationsets/generator-error-merge-keys.yaml"
  "parity-gen-error-matrix-count|found less than two generators, Matrix support only two|appset.generator-failed|applicationsets/generator-error-matrix-count.yaml"
  "parity-gen-error-cluster-selector|error getting cluster secrets: error converting label selector: values[0][parity.drydock.dev/role]: Invalid value: \"https://alpha.parity-clusters.invalid\"|appset.generator-failed|applicationsets/generator-error-cluster-selector.yaml"
)

# RollingSync ApplicationSets, as "name|type|status|reason|message|code"
# entries. Argo CD v3.6.1 must set the condition of that type with exactly
# that status, reason and message
# (applicationset/progressivesync/progressive_sync.go:953-971 and
# applicationset/controllers/applicationset_controller.go:263-277). drydock
# must warn with the code and carry the same message. An empty code means
# drydock must not warn. Argo CD shows only the highest-priority finding
# (validation_issues.go:114-128); drydock warns once for each finding class.
ROLLING_SYNC_CONDITIONS=(
  "parity-rolling-sync-valid|InvalidRolloutConfig|False|ApplicationSetValidRolloutConfig|Rolling Sync Configured correctly|"
  "parity-rolling-sync-operator|InvalidRolloutConfig|True|ApplicationSetInvalidRolloutConfig|Step 1 has invalid matchExpression operators: Exists. Supported Operators are 'In' and 'NotIn'|appset.invalid-rollout-config"
  "parity-rolling-sync-duplicate|InvalidRolloutConfig|True|ApplicationSetInvalidRolloutConfig|Application 'parity-rolling-sync-duplicate-dev' is selected by multiple steps: (1-2)|appset.invalid-rollout-config"
  "parity-rolling-sync-empty-step|InvalidRolloutConfig|True|ApplicationSetInvalidRolloutConfig|Steps [2] have no applications matching their criteria|appset.invalid-rollout-config"
  "parity-rolling-sync-max-update|InvalidRolloutConfig|True|ApplicationSetInvalidRolloutConfig|Step 1 has an invalid maxUpdate value: 1|appset.invalid-rollout-config"
  "parity-rolling-sync-no-steps|ErrorOccurred|True|ApplicationSetRolloutError|No steps defined for rollout|appset.rollout-no-steps"
)

TRACKING_APPLICATIONS=(
  parity-tracking
  parity-helm-crds-default
  parity-oci-config
  parity-tenant-overrides
  parity-plugin-env
)

# Applications outside the Argo CD controller namespace. They pin instance-name
# semantics: ARGOCD_APP_NAME is <namespace>_<name>, the per-Application source
# override file is .argocd-source-<namespace>_<name>.yaml (the bare-name file is
# ignored), and tracking metadata carries the same instance name.
TENANT_NAMESPACE="parity-tenant"
TENANT_APPLICATIONS=(
  parity-tenant-overrides
)

# Applications rendered through a config management plugin. Their drydock
# capture carries --enable-plugins plus trusted policy provenance; no other
# app may, because --plugin-policy-ref makes a missing policy fatal and
# materializes the whole policy-repo tree per invocation.
PLUGIN_APPLICATIONS=(
  parity-plugin-env
  parity-plugin-container
  parity-app-revision-plugin
)
# CMP sidecars patched onto argocd-repo-server. Each entry is both the
# container name and the ConfigManagementPlugin name, so its socket is
# /home/argocd/cmp-server/plugins/<entry>.sock.
CMP_SIDECARS=(
  parity-env
  parity-container
  argocd-vault-plugin
)
# Set by prepare_fixture_git_image: the local git repo the smoke builds from
# the working tree. It is the trusted policy repo for the plugin capture, so
# a local run with an uncommitted fixture is trusted exactly like CI's PR
# merge commit.
FIXTURE_GIT_WORK=""
# Set by prepare_fixture_git_image: the local git repo of OOB_SYMLINK_REPO_URL,
# which drydock reads through --repo-map.
OOB_SYMLINK_WORK=""

PROJECT_POLICY_CASES=(
  "argocd|project-policy-source-allowed|none"
  "argocd|project-policy-source-denied|source"
  "argocd|project-policy-destination-allowed|none"
  "argocd|project-policy-destination-denied|destination"
  "project-policy-tenant|project-policy-source-namespace-allowed|none"
  "project-policy-tenant|project-policy-source-namespace-denied|source-namespace"
  "argocd|project-policy-destination-deny-server-wins|destination"
  "argocd|project-policy-destination-deny-listed-first|destination"
  "argocd|project-policy-destination-deny-namespace-wins|destination"
  "argocd|project-policy-destination-deny-other-allowed|none"
  "argocd|project-policy-destination-name-in-cluster|none"
  "argocd|project-policy-destination-server-in-cluster-name-policy|none"
  "argocd|project-policy-destination-live-scoped-cluster|none"
  "argocd|project-policy-destination-live-scoped-cluster-denied|destination"
)
# A cluster Secret named in-cluster at another server. It would change the
# name-in-cluster case above, so it runs after that case is compared.
PROJECT_POLICY_IN_CLUSTER_SECRET_CASES=(
  "argocd|project-policy-in-cluster-secret-name|destination"
)
# argocd-cm cluster.inClusterEnabled "false" rejects every Application on
# https://kubernetes.default.svc, so this phase runs last.
PROJECT_POLICY_IN_CLUSTER_DISABLED_CASES=(
  "argocd|project-policy-in-cluster-disabled-server|destination"
)

usage() {
  cat <<'USAGE'
Usage: scripts/argocd-parity-smoke.sh [options]

Options:
  --binary <path>        drydock binary to run instead of go run ./cmd/drydock
  --out <dir>           output artifact directory (default: ./argocd-parity-smoke)
  --cluster-name <name> kind cluster name
  --existing-cluster    use an already-created kind cluster with --cluster-name
  --skip-project-policy-smoke
                        skip default project-policy smoke after parity checks
  --keep-cluster        leave the kind cluster running for debugging
  -h, --help            show this help
USAGE
}

fail() {
  echo "argocd render parity smoke: $*" >&2
  exit 2
}

log_step() {
  echo "==> $*" >&2
}

# retry runs a command until it succeeds, at most PULL_RETRY_ATTEMPTS times,
# sleeping PULL_RETRY_INITIAL_DELAY_SECONDS and then doubling between attempts.
# It is for downloads: image pulls, which fail transiently under anonymous
# rate limits, and the AVP release binary.
retry() {
  local description="$1" attempt=1 delay="${PULL_RETRY_INITIAL_DELAY_SECONDS}"
  shift
  until "$@"; do
    if ((attempt >= PULL_RETRY_ATTEMPTS)); then
      return 1
    fi
    echo "argocd render parity smoke: ${description} failed (attempt ${attempt}/${PULL_RETRY_ATTEMPTS}); retrying in ${delay}s" >&2
    sleep "${delay}"
    attempt=$((attempt + 1))
    delay=$((delay * 2))
  done
}

require_value() {
  local flag="$1"
  local value="${2:-}"
  [[ -n "${value}" ]] || fail "${flag} requires a value"
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --binary)
      require_value "$1" "${2:-}"
      DRYDOCK_CMD=("$2")
      shift 2
      ;;
    --out)
      require_value "$1" "${2:-}"
      OUT_DIR="$2"
      shift 2
      ;;
    --cluster-name)
      require_value "$1" "${2:-}"
      CLUSTER_NAME="$2"
      shift 2
      ;;
    --existing-cluster)
      CREATE_CLUSTER="false"
      shift
      ;;
    --skip-project-policy-smoke)
      RUN_PROJECT_POLICY_SMOKE="false"
      shift
      ;;
    --keep-cluster)
      KEEP_CLUSTER="true"
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

require_tool() {
  command -v "$1" >/dev/null 2>&1 || fail "required tool not found: $1"
}

for tool in base64 curl docker git go kind kubectl openssl oras; do
  require_tool "${tool}"
done

[[ -d "${FIXTURE_REPO_PATH}" ]] || fail "fixture repo not found: ${FIXTURE_REPO_PATH}"
[[ -f "${IGNORE_FILE}" ]] || fail "compare ignore file not found: ${IGNORE_FILE}"
[[ -d "${OCI_ARTIFACT_PATH}" ]] || fail "OCI artifact content directory not found: ${OCI_ARTIFACT_PATH}"
[[ -d "${OCI_APPSET_ARTIFACT_PATH}" ]] || fail "OCI ApplicationSet artifact content directory not found: ${OCI_APPSET_ARTIFACT_PATH}"
[[ -f "${OCI_CHART_PATH}/Chart.yaml" ]] || fail "OCI Helm chart fixture not found: ${OCI_CHART_PATH}/Chart.yaml"
[[ -d "${PROJECT_POLICY_REPO_PATH}" ]] || fail "project policy fixture repo not found: ${PROJECT_POLICY_REPO_PATH}"
[[ -f "${PROJECT_POLICY_EXPECTED}" ]] || fail "project policy expected file not found: ${PROJECT_POLICY_EXPECTED}"
[[ -f "${PROJECT_POLICY_LIVE_CLUSTER_SECRETS}" ]] || fail "project policy live cluster Secrets not found: ${PROJECT_POLICY_LIVE_CLUSTER_SECRETS}"
for project_policy_phase_dir in "${PROJECT_POLICY_IN_CLUSTER_SECRET_DIR}" "${PROJECT_POLICY_IN_CLUSTER_DISABLED_DIR}"; do
  [[ -d "${project_policy_phase_dir}/repo" ]] || fail "project policy phase repo not found: ${project_policy_phase_dir}/repo"
  [[ -f "${project_policy_phase_dir}/expected.yaml" ]] || fail "project policy phase expected file not found: ${project_policy_phase_dir}/expected.yaml"
done
[[ -f "${DIRECTORY_DECODE_EXPECTED}" ]] || fail "directory decode expected file not found: ${DIRECTORY_DECODE_EXPECTED}"
[[ -d "${OOB_SYMLINK_FIXTURE_PATH}" ]] || fail "out-of-bounds symlink fixture repo not found: ${OOB_SYMLINK_FIXTURE_PATH}"
[[ -f "${LIVE_DIFF_EXPECTED}" ]] || fail "live diff expected file not found: ${LIVE_DIFF_EXPECTED}"
[[ -d "${SIDECAR_PATH}" ]] || fail "CMP sidecar manifest directory not found: ${SIDECAR_PATH}"
[[ -f "${SOURCE_TYPE_GATES_MANIFESTS}/applications.yaml" ]] || fail "source type gate Applications not found: ${SOURCE_TYPE_GATES_MANIFESTS}/applications.yaml"
for kustomize_build_options_phase in "${KUSTOMIZE_BUILD_OPTIONS_PHASES[@]}"; do
  [[ -f "${KUSTOMIZE_BUILD_OPTIONS_PATH}/${kustomize_build_options_phase}/expected.psv" ]] \
    || fail "kustomize build options phase not found: ${KUSTOMIZE_BUILD_OPTIONS_PATH}/${kustomize_build_options_phase}/expected.psv"
done

OUT_DIR="$(mkdir -p "${OUT_DIR}" && cd "${OUT_DIR}" && pwd)"
# Each run has its own kubeconfig. kind create cluster and kind export
# kubeconfig set the current context of the kubeconfig they write, so a
# shared file would let two runs on one host switch each other's context.
# cleanup removes the file unless --keep-cluster keeps the cluster.
KUBECONFIG="${OUT_DIR}/kubeconfig"
export KUBECONFIG
rm -f "${KUBECONFIG}"
WORK_DIR="$(mktemp -d)"
ARGOCD_CONFIG_DIR="${WORK_DIR}/argocd"
ARGOCD_CONFIG="${ARGOCD_CONFIG_DIR}/config"
mkdir -p "${ARGOCD_CONFIG_DIR}"
chmod 0700 "${ARGOCD_CONFIG_DIR}"
export ARGOCD_CONFIG

# The CA file is re-read at every drydock OCI client construction, so it must
# outlive every drydock invocation: it lives in WORK_DIR (trap lifetime), and
# the warm run and the offline per-app loop share these exact variables.
OCI_TLS_DIR="${WORK_DIR}/registry-tls"
OCI_CA_FILE="${OCI_TLS_DIR}/tls.crt"
OCI_TLS_KEY_FILE="${OCI_TLS_DIR}/tls.key"
OCI_CACHE_DIR="${WORK_DIR}/oci-cache"
# OCI Helm charts land in the chart cache, not the artifact cache: the warm
# run fills it and the offline per-app loop reads it back.
CHART_CACHE_DIR="${WORK_DIR}/chart-cache"
# Fresh, non-overlapping render cache dirs: the persistent render cache key
# omits Offline, so sharing one dir would let the offline loop replay the
# warm (non-offline) render instead of exercising offline resolve+extract.
OCI_WARM_RENDER_CACHE_DIR="${WORK_DIR}/render-cache-warm"
# Remote value files and file parameter URLs. The warm run fills it and the
# offline captures read it back.
REMOTE_CACHE_DIR="${WORK_DIR}/remote-cache"
OCI_OFFLINE_RENDER_CACHE_DIR="${WORK_DIR}/render-cache-offline"

cleanup() {
  local status="$?"
  local pid
  for pid in "${PORT_FORWARD_PID}" "${REGISTRY_PORT_FORWARD_PID}"; do
    if [[ -n "${pid}" ]]; then
      kill "${pid}" >/dev/null 2>&1 || true
    fi
  done
  if [[ "${HOSTS_ENTRY_ADDED}" == "true" ]] && sudo -n true 2>/dev/null; then
    # Best-effort removal of only the marked line this script appended.
    sudo -n sed -i".${OCI_HOSTS_MARKER}.bak" "/# ${OCI_HOSTS_MARKER}\$/d" /etc/hosts >/dev/null 2>&1 || true
    sudo -n rm -f "/etc/hosts.${OCI_HOSTS_MARKER}.bak" >/dev/null 2>&1 || true
  fi
  if [[ "${status}" -ne 0 ]]; then
    collect_logs || true
  fi
  # A failed project-policy phase leaves behind the shared Argo CD state that
  # phases 2 and 3 change: the cluster Secret named in-cluster and the
  # argocd-cm cluster.inClusterEnabled key. On an existing cluster, undo only
  # that state, so that a manual rerun of one phase, or a look at the other
  # Applications, starts from the normal Argo CD settings. The failed
  # Application status is already in the artifacts. The reset does not make
  # a second full run pass on the same cluster. That run fails earlier, on
  # the namespaces and Applications that this run left. With --keep-cluster,
  # keep the state so that the user can inspect the failed phase.
  if [[ "${PROJECT_POLICY_SHARED_STATE_CHANGED}" == "true" ]]; then
    if [[ "${KEEP_CLUSTER}" == "true" ]]; then
      log_step "Keeping the project-policy shared Argo CD state for inspection (--keep-cluster)" || true
    elif [[ "${CREATE_CLUSTER}" != "true" ]]; then
      reset_project_policy_shared_state >/dev/null 2>&1 || true
    fi
  fi
  # On an existing cluster, a failed live diff phase must not leave its
  # Applications and argocd-cm keys behind. With --keep-cluster, keep them
  # for inspection.
  if [[ "${LIVE_DIFF_STATE_CHANGED}" == "true" && "${CREATE_CLUSTER}" != "true" && "${KEEP_CLUSTER}" != "true" ]]; then
    remove_live_diff_state >/dev/null 2>&1 || true
  fi
  # The same for the source type gate phase: on an existing cluster, turn
  # the tools on again unless --keep-cluster keeps the state.
  if [[ "${SOURCE_TYPE_GATES_STATE_CHANGED}" == "true" && "${CREATE_CLUSTER}" != "true" && "${KEEP_CLUSTER}" != "true" ]]; then
    remove_source_type_gates_state >/dev/null 2>&1 || true
  fi
  # The same for the kustomize build options phases.
  if [[ "${KUSTOMIZE_BUILD_OPTIONS_STATE_CHANGED}" == "true" && "${CREATE_CLUSTER}" != "true" && "${KEEP_CLUSTER}" != "true" ]]; then
    remove_kustomize_build_options_state >/dev/null 2>&1 || true
  fi
  rm -rf "${WORK_DIR}"
  if [[ "${CREATE_CLUSTER}" == "true" && "${KEEP_CLUSTER}" != "true" ]]; then
    kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
  fi
  if [[ "${KEEP_CLUSTER}" != "true" ]]; then
    rm -f "${KUBECONFIG}"
  fi
}
trap cleanup EXIT

artifact_dir() {
  mkdir -p "${OUT_DIR}/$1"
  printf '%s\n' "${OUT_DIR}/$1"
}

collect_logs() {
  local logs_dir sidecar
  logs_dir="$(artifact_dir logs)"
  # repo-server carries the parity CMP sidecars; name each container
  # explicitly so every log lands under its own filename regardless of the
  # default-container annotation.
  kubectl -n argocd logs deployment/argocd-repo-server -c argocd-repo-server --tail=300 > "${logs_dir}/argocd-repo-server.log" 2>&1 || true
  for sidecar in "${CMP_SIDECARS[@]}"; do
    kubectl -n argocd logs deployment/argocd-repo-server -c "${sidecar}" --tail=300 > "${logs_dir}/argocd-repo-server-${sidecar}.log" 2>&1 || true
  done
  kubectl -n argocd logs statefulset/argocd-application-controller --tail=300 > "${logs_dir}/argocd-application-controller.log" 2>&1 || true
  kubectl -n argocd logs deployment/argocd-applicationset-controller --tail=300 > "${logs_dir}/argocd-applicationset-controller.log" 2>&1 || true
  kubectl -n argocd-parity logs deployment/argocd-parity-registry --tail=300 > "${logs_dir}/argocd-parity-registry.log" 2>&1 || true
}

resolve_argocd_version() {
  local version
  version="$(cd "${REPO_ROOT}" && go list -m -f '{{.Version}}' "${ARGOCD_MODULE}")"
  [[ "${version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+ ]] || fail "could not resolve concrete Argo CD version from ${ARGOCD_MODULE}: ${version}"
  printf '%s\n' "${version}"
}

install_argocd_cli() {
  local version="$1"
  local os arch url target
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "${arch}" in
    x86_64 | amd64) arch="amd64" ;;
    arm64 | aarch64) arch="arm64" ;;
    *) fail "unsupported argocd CLI architecture: ${arch}" ;;
  esac
  target="${WORK_DIR}/bin/argocd"
  mkdir -p "${WORK_DIR}/bin"
  url="https://github.com/argoproj/argo-cd/releases/download/${version}/argocd-${os}-${arch}"
  curl -fsSL "${url}" -o "${target}"
  chmod 0755 "${target}"
  export PATH="${WORK_DIR}/bin:${PATH}"
}

# pin_container_policy_image points the container plugin policy in the given
# file at the reference the alpine pull succeeded with. drydock runs the image
# with `--pull never`, and a digest pull from a fallback mirror leaves the
# image under that mirror's name only. Only the git snapshot is rewritten:
# drydock reads trusted policy from --plugin-policy-repo at
# --plugin-policy-ref, never from --path. The check runs even when nothing was
# rewritten, so a digest bumped in the script but not in the committed policy
# (or the reverse) fails here instead of as an offline image miss.
pin_container_policy_image() {
  local policy="$1"
  local canonical content
  canonical="${PARITY_ALPINE_IMAGE_REPOSITORIES[0]}@${PARITY_ALPINE_IMAGE_DIGEST}"
  [[ -n "${PARITY_ALPINE_POLICY_IMAGE}" ]] \
    || fail "PARITY_ALPINE_POLICY_IMAGE is unset; prepare_parity_alpine_image must run before prepare_fixture_git_image"
  [[ -f "${policy}" ]] || fail "plugin policy not found in the fixture git snapshot: ${policy}"
  if [[ "${PARITY_ALPINE_POLICY_IMAGE}" != "${canonical}" ]]; then
    content="$(< "${policy}")"
    # The replacement stays unquoted: bash 3.2 would keep the quotes
    # literally, and an image reference carries no & or backslash that bash
    # 5.2's patsub_replacement would expand.
    printf '%s\n' "${content//"${canonical}"/${PARITY_ALPINE_POLICY_IMAGE}}" > "${policy}" \
      || fail "could not rewrite the container plugin image in ${policy}"
    echo "argocd render parity smoke: container plugin policy image rewritten to the pulled mirror reference ${PARITY_ALPINE_POLICY_IMAGE}" >&2
  fi
  grep -qF "image: ${PARITY_ALPINE_POLICY_IMAGE}" "${policy}" \
    || fail "container plugin policy ${policy} does not name the pulled image ${PARITY_ALPINE_POLICY_IMAGE}; keep its image digest in sync with PARITY_ALPINE_IMAGE_DIGEST"
}

# prepare_oob_symlink_repo builds the repository of OOB_SYMLINK_REPO_URL as
# WORK_DIR/oob-symlink.git. It adds other/escape.yaml, a symlink with an
# absolute target, outside the path of the Application that renders from
# the repository. Git stores the symlink as mode 120000, and the Argo CD
# checkout recreates it.
prepare_oob_symlink_repo() {
  OOB_SYMLINK_WORK="${WORK_DIR}/oob-symlink-src"
  mkdir -p "${OOB_SYMLINK_WORK}/other"
  cp -R "${OOB_SYMLINK_FIXTURE_PATH}/." "${OOB_SYMLINK_WORK}/"
  ln -s /etc/hosts "${OOB_SYMLINK_WORK}/other/escape.yaml"
  git -C "${OOB_SYMLINK_WORK}" init --initial-branch=main >/dev/null
  git -C "${OOB_SYMLINK_WORK}" config user.email "drydock@example.invalid"
  git -C "${OOB_SYMLINK_WORK}" config user.name "drydock render parity smoke"
  git -C "${OOB_SYMLINK_WORK}" add .
  git -C "${OOB_SYMLINK_WORK}" commit -m "seed out-of-bounds symlink fixture" >/dev/null
  [[ "$(git -C "${OOB_SYMLINK_WORK}" ls-files -s other/escape.yaml | cut -d' ' -f1)" == "120000" ]] \
    || fail "the out-of-bounds symlink fixture did not commit other/escape.yaml as a symlink"
  git clone --bare --no-hardlinks "${OOB_SYMLINK_WORK}" "${WORK_DIR}/oob-symlink.git" >/dev/null
  git --git-dir="${WORK_DIR}/oob-symlink.git" update-server-info
}

# prepare_live_diff_repo builds the repository of LIVE_DIFF_REPO_URL as
# WORK_DIR/live-diff.git: one commit tagged LIVE_DIFF_REVISION_A, and a second
# commit, with the live-diff/change files copied over it, tagged
# LIVE_DIFF_REVISION_B.
prepare_live_diff_repo() {
  LIVE_DIFF_WORK="${WORK_DIR}/live-diff-src"
  mkdir -p "${LIVE_DIFF_WORK}"
  cp -R "${LIVE_DIFF_FIXTURE_PATH}/repo/." "${LIVE_DIFF_WORK}/"
  git -C "${LIVE_DIFF_WORK}" init --initial-branch=main >/dev/null
  git -C "${LIVE_DIFF_WORK}" config user.email "drydock@example.invalid"
  git -C "${LIVE_DIFF_WORK}" config user.name "drydock render parity smoke"
  git -C "${LIVE_DIFF_WORK}" add .
  git -C "${LIVE_DIFF_WORK}" commit -m "seed live diff fixture" >/dev/null
  git -C "${LIVE_DIFF_WORK}" tag "${LIVE_DIFF_REVISION_A}"
  cp -R "${LIVE_DIFF_FIXTURE_PATH}/change/." "${LIVE_DIFF_WORK}/"
  git -C "${LIVE_DIFF_WORK}" add .
  git -C "${LIVE_DIFF_WORK}" commit -m "change live diff fixture" >/dev/null
  git -C "${LIVE_DIFF_WORK}" tag "${LIVE_DIFF_REVISION_B}"
  git clone --bare --no-hardlinks "${LIVE_DIFF_WORK}" "${WORK_DIR}/live-diff.git" >/dev/null
  git --git-dir="${WORK_DIR}/live-diff.git" update-server-info
}

prepare_fixture_git_image() {
  local bare image dockerfile phase
  # Script-scoped: this repo is also the trusted plugin policy repo for the
  # plugin Application's drydock capture.
  FIXTURE_GIT_WORK="${WORK_DIR}/fixture-src"
  bare="${WORK_DIR}/repo.git"
  image="drydock-argocd-parity-git:${CLUSTER_NAME}"
  mkdir -p "${FIXTURE_GIT_WORK}"
  cp -R "${FIXTURE_REPO_PATH}/." "${FIXTURE_GIT_WORK}/"
  cp -R "${PROJECT_POLICY_REPO_PATH}/." "${FIXTURE_GIT_WORK}/"
  cp -R "${PROJECT_POLICY_IN_CLUSTER_SECRET_DIR}/repo/." "${FIXTURE_GIT_WORK}/"
  cp -R "${PROJECT_POLICY_IN_CLUSTER_DISABLED_DIR}/repo/." "${FIXTURE_GIT_WORK}/"
  cp -R "${SOURCE_TYPE_GATES_REPO_PATH}/." "${FIXTURE_GIT_WORK}/"
  for phase in "${KUSTOMIZE_BUILD_OPTIONS_PHASES[@]}"; do
    cp -R "${KUSTOMIZE_BUILD_OPTIONS_PATH}/${phase}/repo/." "${FIXTURE_GIT_WORK}/"
  done
  pin_container_policy_image "${FIXTURE_GIT_WORK}/.drydock/plugins.yaml"
  package_helm_repo
  # Vendored archive of an older parity-range version that matches the
  # dependency range. helm dependency build fetches parity-range again, at
  # the resolved version, and removes this archive.
  mkdir -p "${FIXTURE_GIT_WORK}/charts/helm-dependency-partial/charts"
  cp "${WORK_DIR}/helm-repo/parity-range-1.2.0.tgz" "${FIXTURE_GIT_WORK}/charts/helm-dependency-partial/charts/"
  git -C "${FIXTURE_GIT_WORK}" init --initial-branch=main >/dev/null
  git -C "${FIXTURE_GIT_WORK}" config user.email "drydock@example.invalid"
  git -C "${FIXTURE_GIT_WORK}" config user.name "drydock render parity smoke"
  git -C "${FIXTURE_GIT_WORK}" add .
  git -C "${FIXTURE_GIT_WORK}" commit -m "seed argocd parity fixture" >/dev/null
  git -C "${FIXTURE_GIT_WORK}" tag -a "${APP_REVISION_ANNOTATED_TAG}" -m "annotated tag for the ARGOCD_APP_REVISION parity case"
  # The source hydrator sync branch. Its ConfigMap at the sync path has
  # another name than on main, so a side that renders main fails the
  # comparison.
  git -C "${FIXTURE_GIT_WORK}" checkout -q -b "${SOURCE_HYDRATOR_BRANCH}"
  printf 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\n' "${SOURCE_HYDRATOR_BRANCH_CONFIGMAP}" \
    > "${FIXTURE_GIT_WORK}/workloads/source-hydrator/hydrated/configmap.yaml"
  git -C "${FIXTURE_GIT_WORK}" commit -q -am "hydrated manifests for the source hydrator parity case"
  git -C "${FIXTURE_GIT_WORK}" checkout -q main
  git clone --bare --no-hardlinks "${FIXTURE_GIT_WORK}" "${bare}" >/dev/null
  git --git-dir="${bare}" update-server-info
  prepare_oob_symlink_repo
  prepare_live_diff_repo

  dockerfile="${WORK_DIR}/Dockerfile.git"
  [[ -n "${PARITY_ALPINE_POLICY_IMAGE}" ]] \
    || fail "PARITY_ALPINE_POLICY_IMAGE is unset; prepare_parity_alpine_image must run before prepare_fixture_git_image"
  # The base is the digest-pinned alpine that prepare_parity_alpine_image
  # pulled. busybox-extras serves the HTTP Helm repository.
  cat > "${dockerfile}" <<DOCKERFILE
FROM ${PARITY_ALPINE_POLICY_IMAGE}
RUN apk add --no-cache git-daemon busybox-extras~${BUSYBOX_EXTRAS_VERSION}
COPY --chown=65534:65534 helm-repo /srv/helm
COPY --chown=65534:65534 repo.git /srv/git/repo.git
COPY --chown=65534:65534 oob-symlink.git /srv/git/oob-symlink.git
COPY --chown=65534:65534 live-diff.git /srv/git/live-diff.git
RUN touch /srv/git/repo.git/git-daemon-export-ok /srv/git/oob-symlink.git/git-daemon-export-ok /srv/git/live-diff.git/git-daemon-export-ok && chmod -R a+rX /srv/git
EXPOSE 9418
USER 65534:65534
ENTRYPOINT ["git", "daemon", "--verbose", "--export-all", "--base-path=/srv/git", "--reuseaddr", "--informative-errors", "/srv/git"]
DOCKERFILE
  # The build fetches packages from the Alpine CDN, which is rate-limited
  # for anonymous clients.
  retry "docker build of ${image}" docker build -q -t "${image}" -f "${dockerfile}" "${WORK_DIR}" >/dev/null \
    || fail "docker build of the fixture Git server image ${image} failed"
  kind load docker-image "${image}" --name "${CLUSTER_NAME}"
}

# package_helm_repo writes the HTTP Helm repository into WORK_DIR/helm-repo:
# the committed index and files, and one chart archive for each
# HELM_REPO_CHARTS entry, built from helm-repo/chart-templates.
package_helm_repo() {
  local out="${WORK_DIR}/helm-repo"
  local entry name version chart_dir
  mkdir -p "${out}"
  cp "${HELM_REPO_PATH}/index.yaml" "${out}/index.yaml"
  cp -R "${HELM_REPO_PATH}/files" "${out}/files"
  for entry in "${HELM_REPO_CHARTS[@]}"; do
    name="${entry% *}"
    version="${entry#* }"
    chart_dir="${WORK_DIR}/helm-repo-src/${name}-${version}/${name}"
    mkdir -p "${chart_dir}/templates"
    printf 'apiVersion: v2\nname: %s\nversion: %s\n' "${name}" "${version}" > "${chart_dir}/Chart.yaml"
    cp "${HELM_REPO_PATH}/chart-templates/configmap.yaml" "${chart_dir}/templates/configmap.yaml"
    # COPYFILE_DISABLE keeps macOS tar from adding AppleDouble files.
    COPYFILE_DISABLE=1 tar -C "$(dirname "${chart_dir}")" -czf "${out}/${name}-${version}.tgz" "${name}" \
      || fail "could not package the Helm repository chart ${name} ${version}"
  done
}

install_fixture_git_server() {
  kubectl create namespace argocd-parity >/dev/null
  kubectl -n argocd-parity apply -f - <<YAML >/dev/null
apiVersion: apps/v1
kind: Deployment
metadata:
  name: argocd-parity-git
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: argocd-parity-git
  template:
    metadata:
      labels:
        app.kubernetes.io/name: argocd-parity-git
    spec:
      containers:
        - name: git
          image: drydock-argocd-parity-git:${CLUSTER_NAME}
          imagePullPolicy: Never
          ports:
            - containerPort: 9418
              name: git
---
apiVersion: v1
kind: Service
metadata:
  name: argocd-parity-git
spec:
  selector:
    app.kubernetes.io/name: argocd-parity-git
  ports:
    - name: git
      port: 9418
      targetPort: git
YAML
  kubectl -n argocd-parity rollout status deployment/argocd-parity-git --timeout=120s
}

ensure_registry_hosts_entry() {
  if grep -qF "${OCI_REGISTRY_HOST}" /etc/hosts; then
    return 0
  fi
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    sudo -n true 2>/dev/null \
      || fail "passwordless sudo is required on CI to append the ${OCI_REGISTRY_HOST} entry to /etc/hosts"
  else
    echo "sudo is required to append '127.0.0.1 ${OCI_REGISTRY_HOST}' to /etc/hosts (add that entry manually beforehand to skip sudo)" >&2
    sudo -v \
      || fail "sudo credentials are required to append the ${OCI_REGISTRY_HOST} entry to /etc/hosts; add '127.0.0.1 ${OCI_REGISTRY_HOST}' manually and rerun to skip sudo"
  fi
  printf '127.0.0.1 %s # %s\n' "${OCI_REGISTRY_HOST}" "${OCI_HOSTS_MARKER}" | sudo tee -a /etc/hosts >/dev/null \
    || fail "could not append the ${OCI_REGISTRY_HOST} entry to /etc/hosts"
  HOSTS_ENTRY_ADDED="true"
}

generate_registry_certificate() {
  local config="${OCI_TLS_DIR}/openssl.cnf"
  mkdir -p "${OCI_TLS_DIR}"
  # Config-file subjectAltName form: works on LibreSSL (macOS /usr/bin/openssl)
  # and OpenSSL 3.x alike; -addext does not. CA:TRUE + keyCertSign let Go
  # clients (Argo CD repo-server and drydock) use the self-signed leaf as its
  # own trust root.
  cat > "${config}" <<CONFIG
[req]
distinguished_name = dn
x509_extensions = v3_req
prompt = no

[dn]
CN = ${OCI_REGISTRY_HOST}

[v3_req]
subjectAltName = DNS:${OCI_REGISTRY_HOST}
basicConstraints = critical, CA:TRUE
keyUsage = critical, digitalSignature, keyEncipherment, keyCertSign
extendedKeyUsage = serverAuth
CONFIG
  openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 3 \
    -keyout "${OCI_TLS_KEY_FILE}" -out "${OCI_CA_FILE}" \
    -config "${config}" >/dev/null 2> "${OUT_DIR}/openssl-cert.stderr" \
    || fail "openssl self-signed certificate generation for ${OCI_REGISTRY_HOST} failed; see ${OUT_DIR}/openssl-cert.stderr"
  rm -f "${OUT_DIR}/openssl-cert.stderr"
  [[ -s "${OCI_CA_FILE}" && -s "${OCI_TLS_KEY_FILE}" ]] \
    || fail "generated OCI registry TLS certificate or key is missing or empty under ${OCI_TLS_DIR}"
}

# pull_pinned_image pulls <digest> from the first of the given repositories
# that serves it and prints the reference it pulled.
pull_pinned_image() {
  local digest="$1"
  shift
  local repository reference
  for repository in "$@"; do
    reference="${repository}@${digest}"
    if retry "docker pull of ${reference}" docker pull "${reference}" >/dev/null; then
      printf '%s\n' "${reference}"
      return 0
    fi
    echo "argocd render parity smoke: giving up on ${repository}; trying the next mirror" >&2
  done
  return 1
}

# daemon_linux_arch prints the architecture of the Docker daemon, which is the
# architecture of every image it builds and of the kind node it runs. The
# shell's own architecture can differ (a Rosetta shell, a remote DOCKER_HOST).
daemon_linux_arch() {
  local arch
  arch="$(docker version --format '{{.Server.Arch}}')" \
    || fail "docker version failed; is the Docker daemon reachable?"
  case "${arch}" in
    amd64 | arm64) printf '%s\n' "${arch}" ;;
    *) fail "unsupported Docker daemon architecture: ${arch}" ;;
  esac
}

# kind_load_image loads a local image into the kind cluster, falling back to a
# single-platform export.
kind_load_image() {
  local image="$1"
  local description="$2"
  local arch archive
  if kind load docker-image "${image}" --name "${CLUSTER_NAME}"; then
    return 0
  fi
  # Under Docker's containerd image store a digest pull keeps the multi-arch
  # index but only the host platform's blobs; kind's `ctr images import
  # --all-platforms` of the docker-save stream then fails on the missing
  # foreign-platform manifests ("content digest ...: not found"). Exporting
  # just the host platform sidesteps the index entirely.
  arch="$(daemon_linux_arch)"
  archive="${WORK_DIR}/kind-load-${description// /-}.tar"
  docker save --platform "linux/${arch}" "${image}" -o "${archive}" \
    || fail "single-platform docker save of the ${description} image ${image} for linux/${arch} failed"
  kind load image-archive "${archive}" --name "${CLUSTER_NAME}" \
    || fail "kind load of the ${description} image ${image} into cluster ${CLUSTER_NAME} failed"
  rm -f "${archive}"
}

prepare_registry_image() {
  local image="drydock-argocd-parity-registry:${CLUSTER_NAME}"
  local pulled
  pulled="$(pull_pinned_image "${OCI_REGISTRY_IMAGE_DIGEST}" "${OCI_REGISTRY_IMAGE_REPOSITORIES[@]}")" \
    || fail "docker pull of the pinned OCI registry image ${OCI_REGISTRY_IMAGE_DIGEST} failed from every mirror: ${OCI_REGISTRY_IMAGE_REPOSITORIES[*]}"
  # A digest pull has no tag; the Deployment matches on the image field with
  # imagePullPolicy Never, so re-tag to the exact name:tag it references.
  docker tag "${pulled}" "${image}" \
    || fail "docker tag of the pinned OCI registry image ${pulled} to ${image} failed"
  kind_load_image "${image}" "OCI registry"
}

# prepare_parity_alpine_image pulls the pinned alpine for the container
# plugin fixture. The digest reference stays in the local image store for
# drydock's offline `docker run --pull never`; the re-tagged name is what the
# repo-server sidecar runs with imagePullPolicy Never.
prepare_parity_alpine_image() {
  local image="drydock-argocd-parity-alpine:${CLUSTER_NAME}"
  PARITY_ALPINE_POLICY_IMAGE="$(pull_pinned_image "${PARITY_ALPINE_IMAGE_DIGEST}" "${PARITY_ALPINE_IMAGE_REPOSITORIES[@]}")" \
    || fail "docker pull of the pinned alpine image ${PARITY_ALPINE_IMAGE_DIGEST} failed from every mirror: ${PARITY_ALPINE_IMAGE_REPOSITORIES[*]}"
  docker tag "${PARITY_ALPINE_POLICY_IMAGE}" "${image}" \
    || fail "docker tag of the pinned alpine image ${PARITY_ALPINE_POLICY_IMAGE} to ${image} failed"
  kind_load_image "${image}" "plugin alpine"
}

# prepare_avp_image builds the argocd-vault-plugin sidecar image with no
# Dockerfile build, so nothing is fetched beyond the sha256-pinned release
# binary: the binary is copied into a container created from the
# already-pulled alpine, and the container is committed.
prepare_avp_image() {
  local image="drydock-argocd-parity-avp:${CLUSTER_NAME}"
  local arch expected actual binary url container version
  arch="$(daemon_linux_arch)"
  case "${arch}" in
    amd64) expected="${AVP_SHA256_LINUX_AMD64}" ;;
    arm64) expected="${AVP_SHA256_LINUX_ARM64}" ;;
    *) fail "no pinned argocd-vault-plugin sha256 for linux/${arch}" ;;
  esac
  [[ -n "${PARITY_ALPINE_POLICY_IMAGE}" ]] \
    || fail "PARITY_ALPINE_POLICY_IMAGE is unset; prepare_parity_alpine_image must run before prepare_avp_image"
  binary="${WORK_DIR}/argocd-vault-plugin"
  url="https://github.com/argoproj-labs/argocd-vault-plugin/releases/download/${AVP_VERSION}/argocd-vault-plugin_${AVP_VERSION#v}_linux_${arch}"
  retry "download of ${url}" curl -fsSL "${url}" -o "${binary}" \
    || fail "download of the argocd-vault-plugin ${AVP_VERSION} linux/${arch} release binary from ${url} failed"
  actual="$(openssl dgst -sha256 -r "${binary}" | cut -d ' ' -f 1)"
  [[ "${actual}" == "${expected}" ]] \
    || fail "argocd-vault-plugin ${AVP_VERSION} linux/${arch} sha256 mismatch: got ${actual}, want ${expected} (${url})"
  chmod 0755 "${binary}"
  container="$(docker create --pull never "${PARITY_ALPINE_POLICY_IMAGE}")" \
    || fail "docker create from the pinned alpine image ${PARITY_ALPINE_POLICY_IMAGE} failed"
  if ! docker cp "${binary}" "${container}:/usr/local/bin/argocd-vault-plugin" >/dev/null \
    || ! docker commit "${container}" "${image}" >/dev/null; then
    docker rm -f "${container}" >/dev/null 2>&1 || true
    fail "could not build ${image} from ${PARITY_ALPINE_POLICY_IMAGE} and the argocd-vault-plugin binary"
  fi
  docker rm -f "${container}" >/dev/null 2>&1 || true
  # Proves the binary runs in the image before the sidecar depends on it.
  version="$(docker run --rm --network none --pull never "${image}" argocd-vault-plugin version)" \
    || fail "argocd-vault-plugin version failed inside ${image}"
  [[ "${version}" == *" ${AVP_VERSION} "* ]] \
    || fail "argocd-vault-plugin in ${image} reports '${version}', want ${AVP_VERSION}"
  echo "argocd render parity smoke: ${version}" >&2
  kind_load_image "${image}" "argocd-vault-plugin"
}

install_fixture_registry() {
  kubectl -n argocd-parity create secret tls argocd-parity-registry-tls \
    --cert="${OCI_CA_FILE}" --key="${OCI_TLS_KEY_FILE}" >/dev/null \
    || fail "could not create the argocd-parity-registry-tls secret in namespace argocd-parity"
  kubectl -n argocd-parity apply -f - <<YAML >/dev/null || fail "could not apply the OCI registry Deployment and Service manifests"
apiVersion: apps/v1
kind: Deployment
metadata:
  name: argocd-parity-registry
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: argocd-parity-registry
  template:
    metadata:
      labels:
        app.kubernetes.io/name: argocd-parity-registry
    spec:
      containers:
        - name: registry
          image: drydock-argocd-parity-registry:${CLUSTER_NAME}
          imagePullPolicy: Never
          env:
            - name: REGISTRY_HTTP_ADDR
              value: ":${OCI_REGISTRY_PORT}"
            - name: REGISTRY_HTTP_TLS_CERTIFICATE
              value: /certs/tls.crt
            - name: REGISTRY_HTTP_TLS_KEY
              value: /certs/tls.key
          ports:
            - containerPort: ${OCI_REGISTRY_PORT}
              name: registry
          volumeMounts:
            - name: registry-tls
              mountPath: /certs
              readOnly: true
        - name: helm-repo
          image: drydock-argocd-parity-git:${CLUSTER_NAME}
          imagePullPolicy: Never
          command: ["busybox-extras", "httpd", "-f", "-p", "${HELM_REPO_PORT}", "-h", "/srv/helm"]
          ports:
            - containerPort: ${HELM_REPO_PORT}
              name: helm-repo
          readinessProbe:
            httpGet:
              path: /index.yaml
              port: helm-repo
            periodSeconds: 2
      volumes:
        - name: registry-tls
          secret:
            secretName: argocd-parity-registry-tls
---
apiVersion: v1
kind: Service
metadata:
  name: argocd-parity-registry
spec:
  selector:
    app.kubernetes.io/name: argocd-parity-registry
  ports:
    - name: registry
      port: ${OCI_REGISTRY_PORT}
      targetPort: registry
    - name: helm-repo
      port: ${HELM_REPO_PORT}
      targetPort: helm-repo
YAML
  kubectl -n argocd-parity rollout status deployment/argocd-parity-registry --timeout=120s \
    || fail "OCI registry deployment argocd-parity-registry did not become ready within 120s"
}

start_registry_port_forward() {
  kubectl -n argocd-parity port-forward svc/argocd-parity-registry "${OCI_REGISTRY_PORT}:${OCI_REGISTRY_PORT}" "${HELM_REPO_PORT}:${HELM_REPO_PORT}" >/dev/null 2>&1 &
  REGISTRY_PORT_FORWARD_PID="$!"
  # One probe validates the hosts entry, the forward, the TLS SAN, and
  # registry liveness together. The second one checks the Helm repository.
  for _ in {1..60}; do
    if curl -fsS --cacert "${OCI_CA_FILE}" "https://${OCI_REGISTRY_HOST}:${OCI_REGISTRY_PORT}/v2/" >/dev/null 2>&1 \
      && curl -fsS "${HELM_REPO_URL}/index.yaml" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "OCI registry or Helm repository was not reachable at https://${OCI_REGISTRY_HOST}:${OCI_REGISTRY_PORT}/v2/ and ${HELM_REPO_URL}/index.yaml through the port-forward (check the /etc/hosts entry, the TLS SAN, and the registry pod)"
}

push_oci_artifact() {
  local content_dir="$1" repository="$2" tag="$3" label="$4"
  local ref stderr_file
  ref="${OCI_REGISTRY_HOST}:${OCI_REGISTRY_PORT}/${repository}:${tag}"
  stderr_file="${OUT_DIR}/oras-push-${label}.stderr"
  # Push the content directory from inside it so the tar entries sit at the
  # extraction root (path: . in the fixture Application) as exactly one
  # tar+gzip content layer. helm push or per-file oras push would change the
  # media-type semantics. oras keeps hidden directories and stores symlinks
  # as symlinks, which the ApplicationSet artifact relies on.
  (cd "${content_dir}" && oras push --ca-file "${OCI_CA_FILE}" "${ref}" . >/dev/null 2> "${stderr_file}") \
    || fail "oras push of the OCI parity ${label} artifact to ${ref} failed; see ${stderr_file}"
  rm -f "${stderr_file}"
}

push_oci_chart() {
  local ref stderr_file
  ref="${OCI_REGISTRY_HOST}:${OCI_REGISTRY_PORT}/${OCI_CHART_REPOSITORY}"
  stderr_file="${OUT_DIR}/helm-chart-push.stderr"
  # helm's own registry client, not oras: only it writes the helm config and
  # content media types Argo CD's repo-server and drydock both require, and
  # the smoke must not depend on a helm binary being installed.
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-parity-chart-push \
    --chart-dir "${OCI_CHART_PATH}" \
    --ref "${ref}" \
    --ca-file "${OCI_CA_FILE}" \
    > /dev/null 2> "${stderr_file}") \
    || fail "helm chart push of the nested OCI parity chart to ${ref} failed; see ${stderr_file}"
  rm -f "${stderr_file}"
}

verify_oci_chart_manifest() {
  local ref manifest stderr_file config_count layer_count media_type_count
  ref="${OCI_REGISTRY_HOST}:${OCI_REGISTRY_PORT}/${OCI_CHART_REPOSITORY}/parity-nested-chart:1.0.0"
  stderr_file="${OUT_DIR}/oras-chart-manifest-fetch.stderr"
  manifest="$(oras manifest fetch --ca-file "${OCI_CA_FILE}" "${ref}" 2> "${stderr_file}")" \
    || fail "oras manifest fetch for the pushed nested OCI parity chart ${ref} failed; see ${stderr_file}"
  rm -f "${stderr_file}"
  config_count="$(grep -o 'application/vnd\.cncf\.helm\.config\.v1+json' <<< "${manifest}" | wc -l | tr -d ' ' || true)"
  layer_count="$(grep -o 'application/vnd\.cncf\.helm\.chart\.content\.v1\.tar+gzip' <<< "${manifest}" | wc -l | tr -d ' ' || true)"
  media_type_count="$(grep -o '"mediaType"' <<< "${manifest}" | wc -l | tr -d ' ' || true)"
  # Expect exactly two mediaType entries — the helm config and one helm chart
  # content layer, with no provenance layer and no extra content layer. Helm's
  # registry client writes no top-level manifest mediaType, unlike the oras
  # push in verify_oci_artifact_manifest, so the total is two and not three.
  if [[ "${config_count}" != "1" || "${layer_count}" != "1" || "${media_type_count}" != "2" ]]; then
    printf '%s\n' "${manifest}" > "${OUT_DIR}/oci-chart-manifest.json"
    fail "pushed nested OCI parity chart ${ref} has the wrong manifest shape: want one application/vnd.cncf.helm.config.v1+json config and exactly one application/vnd.cncf.helm.chart.content.v1.tar+gzip layer, got ${config_count} helm configs and ${layer_count} helm content layers across ${media_type_count} mediaType entries; see ${OUT_DIR}/oci-chart-manifest.json"
  fi
}

verify_oci_artifact_manifest() {
  local repository="$1" tag="$2" label="$3"
  local ref manifest stderr_file layer_count media_type_count
  ref="${OCI_REGISTRY_HOST}:${OCI_REGISTRY_PORT}/${repository}:${tag}"
  stderr_file="${OUT_DIR}/oras-manifest-fetch-${label}.stderr"
  manifest="$(oras manifest fetch --ca-file "${OCI_CA_FILE}" "${ref}" 2> "${stderr_file}")" \
    || fail "oras manifest fetch for the pushed OCI parity ${label} artifact ${ref} failed; see ${stderr_file}"
  rm -f "${stderr_file}"
  layer_count="$(grep -o 'application/vnd\.oci\.image\.layer\.v1\.tar+gzip' <<< "${manifest}" | wc -l | tr -d ' ' || true)"
  media_type_count="$(grep -o '"mediaType"' <<< "${manifest}" | wc -l | tr -d ' ' || true)"
  # Expect exactly three mediaType entries (manifest, empty config, one
  # layer) with the single layer being the tar+gzip content type.
  if [[ "${layer_count}" != "1" || "${media_type_count}" != "3" ]]; then
    printf '%s\n' "${manifest}" > "${OUT_DIR}/oci-${label}-artifact-manifest.json"
    fail "pushed OCI parity ${label} artifact ${ref} has the wrong manifest shape: want exactly one application/vnd.oci.image.layer.v1.tar+gzip content layer, got ${layer_count} tar+gzip layers across ${media_type_count} mediaType entries; see ${OUT_DIR}/oci-${label}-artifact-manifest.json"
  fi
}

# wait_for_oci_applicationsets gates on every OCI generator ApplicationSet
# reporting ParametersGenerated=True. The ApplicationSet controller sets it
# only after the generator pulled the artifact through the repo-server and
# produced params, so a registry, CA, or generator failure names the
# ApplicationSet here instead of surfacing later as a missing Application.
wait_for_oci_applicationsets() {
  local appset status dump
  for appset in "${OCI_APPSETS[@]}"; do
    status=""
    for _ in {1..120}; do
      status="$(kubectl -n argocd get applicationset "${appset}" \
        -o jsonpath='{.status.conditions[?(@.type=="ParametersGenerated")].status}' 2>/dev/null || true)"
      if [[ "${status}" == "True" ]]; then
        break
      fi
      sleep 2
    done
    if [[ "${status}" != "True" ]]; then
      dump="$(artifact_dir logs)/applicationset-${appset}.yaml"
      kubectl -n argocd get applicationset "${appset}" -o yaml > "${dump}" 2>&1 || true
      fail "OCI generator ApplicationSet ${appset} did not report ParametersGenerated=True (the ApplicationSet controller could not list or read ${OCI_APPSET_ARTIFACT_REPOSITORY}:${OCI_APPSET_ARTIFACT_TAG} through the repo-server, or the generator failed); see ${dump}"
    fi
  done
}

wait_for_oci_application() {
  local app sync_status
  # The registry-backed Applications: the artifact source, the nested OCI
  # Helm chart source, parity-oci-helm-chart-ignored (the chart pulled as an
  # oci:// artifact with chart: set), and one Application the OCI generator
  # made from the ApplicationSet artifact. Naming the app in the failure
  # tells a TLS problem (argocd-tls-certs-cm) apart from a media-type or
  # chart-name problem.
  for app in "${OCI_APPLICATION}" "${OCI_CHART_APPLICATION}" "${OCI_CHART_IGNORED_APPLICATION}" "${OCI_APPSET_WARM_APPLICATION}"; do
    sync_status=""
    for _ in {1..120}; do
      sync_status="$(kubectl -n argocd get application "${app}" -o jsonpath='{.status.sync.status}' 2>/dev/null || true)"
      if [[ "${sync_status}" == "Synced" || "${sync_status}" == "OutOfSync" ]]; then
        break
      fi
      sleep 2
    done
    if [[ "${sync_status}" != "Synced" && "${sync_status}" != "OutOfSync" ]]; then
      kubectl -n argocd get application "${app}" -o yaml > "${OUT_DIR}/${app}.yaml" 2>&1 || true
      fail "OCI Application ${app} did not reach a comparable sync state (Argo CD could not fetch it from ${OCI_REGISTRY_HOST}; check the argocd-tls-certs-cm CA, the registry service, and the pushed manifest media types); see ${OUT_DIR}/${app}.yaml"
    fi
  done
}

warm_drydock_oci_cache() {
  local app stderr_file
  # The artifact app and parity-oci-helm-chart-ignored fill --oci-cache-dir
  # (an oci:// repoURL takes the artifact flow whether or not chart: is set),
  # the schemeless chart app fills --chart-cache-dir; the offline capture
  # loop reads both back. The OCI generator Application fills
  # --oci-cache-dir with the ApplicationSet artifact. Every later offline
  # drydock run evaluates the OCI generator ApplicationSets during
  # discovery, so none of them works without this warm run.
  for app in "${OCI_APPLICATION}" "${OCI_CHART_APPLICATION}" "${OCI_CHART_IGNORED_APPLICATION}" "${OCI_APPSET_WARM_APPLICATION}"; do
    stderr_file="${OUT_DIR}/drydock-oci-warm-${app}.stderr"
    (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" build app "argocd/${app}" \
      --path "${FIXTURE_REPO_PATH}" \
      --repo-map "${FIXTURE_REPO_URL}=${FIXTURE_REPO_PATH}" \
      --oci-cache-dir "${OCI_CACHE_DIR}" \
      --chart-cache-dir "${CHART_CACHE_DIR}" \
      --oci-ca-file "${OCI_CA_FILE}" \
      --render-cache-dir "${OCI_WARM_RENDER_CACHE_DIR}" \
      > /dev/null 2> "${stderr_file}") \
      || fail "drydock OCI cache warm (non-offline build of ${app} with --oci-cache-dir/--chart-cache-dir/--oci-ca-file) failed; see ${stderr_file}"
    rm -f "${stderr_file}"
  done
}

# warm_drydock_helm_repo_cache renders each HELM_REPO_APPLICATIONS entry
# once without --offline, so the offline captures find the charts, the
# version lists that resolve the ranges, and the remote files.
warm_drydock_helm_repo_cache() {
  local app stderr_file repo_map_path git_work_app
  for app in "${HELM_REPO_APPLICATIONS[@]}"; do
    repo_map_path="${FIXTURE_REPO_PATH}"
    for git_work_app in "${HELM_REPO_GIT_WORK_APPLICATIONS[@]}"; do
      if [[ "${app}" == "${git_work_app}" ]]; then
        repo_map_path="${FIXTURE_GIT_WORK}"
      fi
    done
    stderr_file="${OUT_DIR}/drydock-helm-repo-warm-${app}.stderr"
    (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" build app "argocd/${app}" \
      --path "${FIXTURE_REPO_PATH}" \
      --repo-map "${FIXTURE_REPO_URL}=${repo_map_path}" \
      --oci-cache-dir "${OCI_CACHE_DIR}" \
      --chart-cache-dir "${CHART_CACHE_DIR}" \
      --remote-cache-dir "${REMOTE_CACHE_DIR}" \
      --oci-ca-file "${OCI_CA_FILE}" \
      --render-cache-dir "${OCI_WARM_RENDER_CACHE_DIR}" \
      --appset-provider-fixture "${APPSET_PROVIDER_FIXTURE}" \
      > /dev/null 2> "${stderr_file}") \
      || fail "drydock Helm repository cache warm (non-offline build of ${app}) failed; see ${stderr_file}"
    rm -f "${stderr_file}"
  done
}

install_argocd() {
  local version="$1"
  kubectl create namespace argocd >/dev/null
  kubectl -n argocd apply --server-side --force-conflicts -f "https://raw.githubusercontent.com/argoproj/argo-cd/${version}/manifests/install.yaml" >/dev/null
  kubectl wait --for=condition=Established crd/applications.argoproj.io --timeout=120s
  kubectl wait --for=condition=Established crd/applicationsets.argoproj.io --timeout=120s
  kubectl -n argocd patch configmap argocd-cm --type merge -p '{"data":{"kustomize.buildOptions":"--enable-helm"}}' >/dev/null
  # Argo CD checks RollingSync steps only with progressive syncs on, and they
  # are off by default. The ApplicationSet controller restart below picks it
  # up. drydock reads the same key from repo/settings/argocd-cmd-params-cm.yaml.
  #
  # The controller reconciles one ApplicationSet at a time. In v3.6.1 all
  # reconciles share one progressivesync.Manager, and PerformProgressiveSyncs
  # stores the RollingSync findings in its validationIssues field
  # (applicationset/progressivesync/progressive_sync.go:82 and 119-125). With
  # the default of 10 concurrent reconciles, one ApplicationSet can get the
  # InvalidRolloutConfig message of another. drydock does not model that race,
  # and the condition comparison needs a stable message.
  kubectl -n argocd patch configmap argocd-cmd-params-cm --type merge \
    -p '{"data":{"applicationsetcontroller.enable.progressive.syncs":"true","applicationsetcontroller.concurrent.reconciliations.max":"1"}}' >/dev/null
  # Argo CD derives the OCI registry CA from argocd-tls-certs-cm keyed by the
  # bare hostname without port. The patch must land before the rollout
  # restart below so repo-server pods mount the CA from the start (ConfigMap
  # volume propagation to a running pod races the kubelet sync).
  [[ -s "${OCI_CA_FILE}" ]] \
    || fail "OCI registry CA certificate not found at ${OCI_CA_FILE} before Argo CD install; certificate generation must run first"
  kubectl -n argocd create configmap argocd-tls-certs-cm \
    --from-file="${OCI_REGISTRY_HOST}=${OCI_CA_FILE}" \
    --dry-run=client -o json > "${WORK_DIR}/argocd-tls-certs-cm.patch.json" \
    || fail "could not build the argocd-tls-certs-cm patch payload from ${OCI_CA_FILE}"
  kubectl -n argocd patch configmap argocd-tls-certs-cm --type merge \
    --patch-file "${WORK_DIR}/argocd-tls-certs-cm.patch.json" >/dev/null \
    || fail "could not patch argocd-tls-certs-cm with the ${OCI_REGISTRY_HOST} CA bundle"
  kubectl -n argocd rollout restart deployment/argocd-repo-server >/dev/null
  kubectl -n argocd rollout restart deployment/argocd-applicationset-controller >/dev/null
  kubectl -n argocd rollout status deployment/argocd-repo-server --timeout=300s
  kubectl -n argocd rollout status deployment/argocd-server --timeout=300s
  kubectl -n argocd rollout status deployment/argocd-applicationset-controller --timeout=300s
  kubectl -n argocd rollout status statefulset/argocd-application-controller --timeout=300s
}

# wait_for_cmp_socket gates on a sidecar's plugin socket. rollout status only
# proves the container is Running; this makes a bad plugin descriptor fail
# here with a clear message instead of minutes later as "did not generate
# manifests".
wait_for_cmp_socket() {
  local sidecar="$1"
  local socket="/home/argocd/cmp-server/plugins/${sidecar}.sock"
  local sidecar_log
  for _ in {1..60}; do
    if kubectl -n argocd exec deployment/argocd-repo-server -c "${sidecar}" -- \
      sh -c "test -S ${socket}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  # Same filename collect_logs writes from the EXIT trap, so the later, longer
  # tail supersedes this dump instead of leaving two copies.
  sidecar_log="$(artifact_dir logs)/argocd-repo-server-${sidecar}.log"
  kubectl -n argocd logs deployment/argocd-repo-server -c "${sidecar}" --tail=200 > "${sidecar_log}" 2>&1 || true
  fail "argocd-cmp-server in sidecar ${sidecar} never bound ${socket}; see ${sidecar_log}"
}

# install_avp_backend applies the Secret the AVP sidecar's kubernetessecret
# backend reads (drydock's public redaction markers, no secret value) and the
# RBAC that lets the repo-server ServiceAccount get it, then confirms the
# grant, so a broken binding fails here and not as a generate error.
install_avp_backend() {
  kubectl apply -f "${SIDECAR_PATH}/avp-backend.yaml" -f "${SIDECAR_PATH}/avp-rbac.yaml" >/dev/null \
    || fail "could not apply the argocd-vault-plugin backend Secret and RBAC from ${SIDECAR_PATH}"
  for _ in {1..30}; do
    if kubectl auth can-i get secrets/parity-avp-backend -n argocd \
      --as=system:serviceaccount:argocd:argocd-repo-server >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "ServiceAccount argocd/argocd-repo-server still cannot get secret parity-avp-backend after applying ${SIDECAR_PATH}/avp-rbac.yaml"
}

install_cmp_sidecar() {
  local version="$1"
  local patch_file sidecar
  patch_file="${WORK_DIR}/repo-server-cmp-patch.yaml"
  # The ConfigMaps must exist before the patch: each sidecar mounts its own as
  # a non-optional configMap volume, so a patch that landed first would leave
  # the new ReplicaSet in ContainerCreating and the rollout below would burn
  # its whole timeout on a confusing message.
  kubectl -n argocd create configmap parity-env-cmp \
    --from-file="plugin.yaml=${SIDECAR_PATH}/plugin.yaml" >/dev/null \
    || fail "could not create the parity-env-cmp ConfigMap from ${SIDECAR_PATH}/plugin.yaml"
  kubectl -n argocd create configmap parity-container-cmp \
    --from-file="plugin.yaml=${SIDECAR_PATH}/plugin-container.yaml" >/dev/null \
    || fail "could not create the parity-container-cmp ConfigMap from ${SIDECAR_PATH}/plugin-container.yaml"
  kubectl -n argocd create configmap parity-avp-cmp \
    --from-file="plugin.yaml=${SIDECAR_PATH}/plugin-avp.yaml" >/dev/null \
    || fail "could not create the parity-avp-cmp ConfigMap from ${SIDECAR_PATH}/plugin-avp.yaml"
  install_avp_backend
  sed -e "s|__ARGOCD_IMAGE__|quay.io/argoproj/argocd:${version}|" \
    -e "s|__PARITY_ALPINE_IMAGE__|drydock-argocd-parity-alpine:${CLUSTER_NAME}|" \
    -e "s|__PARITY_AVP_IMAGE__|drydock-argocd-parity-avp:${CLUSTER_NAME}|" \
    "${SIDECAR_PATH}/repo-server-patch.yaml" > "${patch_file}" \
    || fail "could not render the repo-server CMP sidecar patch from ${SIDECAR_PATH}/repo-server-patch.yaml"
  if grep -q '__[A-Z_]*__' "${patch_file}"; then
    fail "the rendered repo-server CMP sidecar patch ${patch_file} still has an unsubstituted __PLACEHOLDER__"
  fi
  # install.yaml is applied server-side once and never re-applied, so a
  # client-side strategic patch afterwards is safe, and the patch itself
  # rolls the Deployment - no restart call needed.
  kubectl -n argocd patch deployment argocd-repo-server --type strategic \
    --patch-file "${patch_file}" >/dev/null \
    || fail "could not patch argocd-repo-server with the parity CMP sidecars"
  kubectl -n argocd rollout status deployment/argocd-repo-server --timeout=300s
  for sidecar in "${CMP_SIDECARS[@]}"; do
    wait_for_cmp_socket "${sidecar}"
  done
}

login_argocd() {
  local password
  password="$(kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d)"
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo "::add-mask::${password}"
  fi
  kubectl -n argocd port-forward svc/argocd-server 18080:443 >/dev/null 2>&1 &
  PORT_FORWARD_PID="$!"
  for _ in {1..60}; do
    if curl -kfsS https://localhost:18080 >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  argocd login localhost:18080 --username admin --password "${password}" --insecure >/dev/null
}

# apply_cluster_secrets applies the cluster Secrets before any
# ApplicationSet, so the cluster generator sees them on its first reconcile.
# It uses client-side apply on purpose: that writes
# kubectl.kubernetes.io/last-applied-configuration, one of the reserved
# annotations the cluster generator must hide. The check below fails the run
# if that annotation is missing, because the fixture would then stop
# exercising it.
apply_cluster_secrets() {
  local secret
  kubectl -n argocd apply -f "${CLUSTER_SECRETS_PATH}" >/dev/null \
    || fail "could not apply the cluster generator Secrets from ${CLUSTER_SECRETS_PATH}"
  for secret in "${CLUSTER_SECRET_NAMES[@]}"; do
    [[ -n "$(kubectl -n argocd get secret "${secret}" \
      -o jsonpath='{.metadata.annotations.kubectl\.kubernetes\.io/last-applied-configuration}')" ]] \
      || fail "cluster Secret argocd/${secret} has no kubectl.kubernetes.io/last-applied-configuration annotation; the cluster generator fixture needs it"
  done
}

apply_fixture_apps() {
  ensure_namespace "${TENANT_NAMESPACE}"
  ensure_namespace "${DIRECTORY_DECODE_NAMESPACE}"
  ensure_namespace "${NAMESPACE_DISABLED}"
  kubectl -n argocd apply -f "${FIXTURE_REPO_PATH}/projects" >/dev/null
  kubectl -n argocd apply -f "${FIXTURE_REPO_PATH}/applications" >/dev/null
  apply_cluster_secrets
  kubectl -n argocd apply -f "${FIXTURE_REPO_PATH}/applicationsets" >/dev/null
  kubectl -n "${TENANT_NAMESPACE}" apply -f "${FIXTURE_REPO_PATH}/tenant-applications" >/dev/null
  kubectl -n "${DIRECTORY_DECODE_NAMESPACE}" apply -f "${FIXTURE_REPO_PATH}/directory-decode-applications" >/dev/null
  # Each file names its own namespace.
  kubectl apply -f "${FIXTURE_REPO_PATH}/namespace-gate" >/dev/null
}

wait_for_application() {
  local namespace="$1"
  local app="$2"
  local reconciled_at
  for _ in {1..120}; do
    if kubectl -n "${namespace}" get application "${app}" >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  kubectl -n "${namespace}" get application "${app}" >/dev/null
  # `argocd app manifests` serves the controller's cached comparison, so an
  # Application the controller never reconciled returns exit 0 with empty
  # output. Gate on reconciledAt so that failure surfaces here instead of as a
  # misleading "did not generate manifests" after the capture retry loop.
  for _ in {1..120}; do
    reconciled_at="$(kubectl -n "${namespace}" get application "${app}" -o jsonpath='{.status.reconciledAt}' 2>/dev/null || true)"
    if [[ -n "${reconciled_at}" ]]; then
      return 0
    fi
    sleep 2
  done
  local dump
  dump="$(artifact_dir logs)/unreconciled-${namespace}-${app}.yaml"
  kubectl -n "${namespace}" get application "${app}" -o yaml > "${dump}" 2>&1 || true
  fail "Argo CD did not reconcile Application ${namespace}/${app}; see ${dump}"
}

wait_for_applications() {
  local app
  for app in "${APPLICATIONS[@]}"; do
    wait_for_application argocd "${app}"
  done
  for app in "${TENANT_APPLICATIONS[@]}"; do
    wait_for_application "${TENANT_NAMESPACE}" "${app}"
  done
}

# wait_for_applicationset_error waits until ApplicationSet argocd/<name> has
# an ErrorOccurred condition with status True and a message that contains the
# expected text. It keeps polling while the condition is missing or different,
# because the first reconcile can still be running.
wait_for_applicationset_error() {
  local appset="$1"
  local expected="$2"
  local status="" message="" dump
  for _ in {1..120}; do
    status="$(kubectl -n argocd get applicationset "${appset}" \
      -o jsonpath='{.status.conditions[?(@.type=="ErrorOccurred")].status}' 2>/dev/null || true)"
    message="$(kubectl -n argocd get applicationset "${appset}" \
      -o jsonpath='{.status.conditions[?(@.type=="ErrorOccurred")].message}' 2>/dev/null || true)"
    if [[ "${status}" == "True" && "${message}" == *"${expected}"* ]]; then
      return 0
    fi
    sleep 2
  done
  dump="$(artifact_dir logs)/applicationset-${appset}.yaml"
  kubectl -n argocd get applicationset "${appset}" -o yaml > "${dump}" 2>&1 || true
  fail "ApplicationSet argocd/${appset} has no ErrorOccurred condition with status True and a message that contains \"${expected}\" (last status: ${status:-none}; last message: ${message:-none}); see ${dump}"
}

wait_for_applicationset_errors() {
  local entry appset expected _code _path
  for entry in "${APPLICATIONSET_ERRORS[@]}"; do
    IFS='|' read -r appset expected _code _path <<< "${entry}"
    wait_for_applicationset_error "${appset}" "${expected}"
  done
}

# compare_applicationset_errors checks the drydock side of
# APPLICATIONSET_ERRORS: for each entry with a code, the get apps stderr
# that capture_drydock_applications keeps must hold a warning with that
# code for the ApplicationSet file, with the Argo CD text in it.
# run_missing_project_smoke checks a recorded divergence. It applies
# MISSING_PROJECT_DIR after the main comparisons, so its Applications stay
# out of the main inventory. Argo CD must report the missing AppProject in an
# ErrorOccurred condition, create parity-missing-project-kept, and not
# create parity-missing-project-dropped. drydock does not know the AppProjects
# of the cluster offline, so it must keep both Applications and warn
# project.missing for parity-missing-project-dropped, with the note that the
# ApplicationSet controller does not create it.
run_missing_project_smoke() {
  local output_dir list_file list_stderr diag_file diag_stderr names
  log_step "Checking a generated Application whose AppProject is missing"
  kubectl apply -f "${MISSING_PROJECT_DIR}" >/dev/null
  wait_for_applicationset_error "${MISSING_PROJECT_APPLICATIONSET}" "${MISSING_PROJECT_MESSAGE}"
  wait_for_application argocd parity-missing-project-kept
  if kubectl -n argocd get application parity-missing-project-dropped >/dev/null 2>&1; then
    fail "Argo CD created parity-missing-project-dropped, whose AppProject does not exist"
  fi
  output_dir="$(artifact_dir missing-project)"
  list_file="${output_dir}/drydock-apps.txt"
  list_stderr="${output_dir}/drydock-apps.stderr"
  diag_file="${output_dir}/drydock-diag.txt"
  diag_stderr="${output_dir}/drydock-diag.stderr"
  if ! (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" get apps \
    --path "${MISSING_PROJECT_DIR}" \
    --repo-map "${FIXTURE_REPO_URL}=${FIXTURE_REPO_PATH}" \
    --offline > "${list_file}" 2> "${list_stderr}"); then
    cat "${list_stderr}" >&2 || true
    fail "drydock get apps on ${MISSING_PROJECT_DIR} failed; see ${list_stderr}"
  fi
  names="$(awk 'NR > 1 { print $2 }' "${list_file}" | sort | tr '\n' ' ')"
  [[ "${names}" == "parity-missing-project-dropped parity-missing-project-kept " ]] \
    || fail "drydock listed '${names}' for ${MISSING_PROJECT_DIR}; want both Applications (recorded divergence); see ${list_file}"
  if ! (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" diag \
    --path "${MISSING_PROJECT_DIR}" \
    --repo-map "${FIXTURE_REPO_URL}=${FIXTURE_REPO_PATH}" \
    --offline > "${diag_file}" 2> "${diag_stderr}"); then
    cat "${diag_stderr}" >&2 || true
    fail "drydock diag on ${MISSING_PROJECT_DIR} failed; see ${diag_stderr}"
  fi
  grep -F "warning project.missing: Application argocd/parity-missing-project-dropped references missing AppProject \"parity-missing-project\"; ApplicationSet argocd/${MISSING_PROJECT_APPLICATIONSET} generates it, and the Argo CD ApplicationSet controller does not create or update it unless that AppProject exists in the cluster" "${diag_file}" >/dev/null \
    || fail "drydock did not warn project.missing for parity-missing-project-dropped with the ApplicationSet controller note; see ${diag_file}"
  if grep -qF "parity-missing-project-kept" "${diag_file}"; then
    fail "drydock warned for parity-missing-project-kept, whose AppProject exists; see ${diag_file}"
  fi
  kubectl delete -f "${MISSING_PROJECT_DIR}" --wait=false >/dev/null || true
  echo "Missing AppProject: Argo CD drops the Application, drydock keeps it and warns project.missing (recorded divergence)" >&2
}

compare_applicationset_errors() {
  local stderr_file="${OUT_DIR}/applications/drydock.stderr"
  local entry appset expected code path checked=0
  log_step "Comparing Argo CD ApplicationSet errors with drydock warnings"
  for entry in "${APPLICATIONSET_ERRORS[@]}"; do
    IFS='|' read -r appset expected code path <<< "${entry}"
    [[ -n "${code}" ]] || continue
    if ! grep -F "warning ${code}: " "${stderr_file}" | grep -F "(path: ${path}," | grep -qF "${expected}"; then
      grep -E '^warning appset\.' "${stderr_file}" >&2 || echo "(no appset warnings)" >&2
      fail "drydock did not warn ${code} for ApplicationSet argocd/${appset} (${path}) with the Argo CD text \"${expected}\"; see ${stderr_file}"
    fi
    checked=$((checked + 1))
  done
  echo "ApplicationSet errors: ${checked} drydock warnings match" >&2
}

# applicationset_condition prints "<status>|<reason>|<message>" for the
# condition of the given type on ApplicationSet argocd/<name>, and nothing
# when it has none.
applicationset_condition() {
  local appset="$1"
  local condition_type="$2"
  kubectl -n argocd get applicationset "${appset}" \
    -o jsonpath="{range .status.conditions[?(@.type==\"${condition_type}\")]}{.status}|{.reason}|{.message}{end}" 2>/dev/null || true
}

# wait_for_rolling_sync_conditions waits until each ApplicationSet in
# ROLLING_SYNC_CONDITIONS has its expected condition. The maxUpdate finding
# needs the Applications to exist first, so it appears on a later reconcile.
wait_for_rolling_sync_conditions() {
  local entry appset condition_type status reason message code expected actual dump
  for entry in "${ROLLING_SYNC_CONDITIONS[@]}"; do
    IFS='|' read -r appset condition_type status reason message code <<< "${entry}"
    expected="${status}|${reason}|${message}"
    for _ in {1..120}; do
      actual="$(applicationset_condition "${appset}" "${condition_type}")"
      [[ "${actual}" == "${expected}" ]] && continue 2
      sleep 2
    done
    dump="$(artifact_dir logs)/applicationset-${appset}.yaml"
    kubectl -n argocd get applicationset "${appset}" -o yaml > "${dump}" 2>&1 || true
    fail "ApplicationSet argocd/${appset} ${condition_type} condition is \"${actual:-none}\", want \"${expected}\"; see ${dump}"
  done
}

# print_rolling_sync_warnings prints drydock's RollingSync warnings to the
# job log, where a failed comparison is diagnosed. CI also uploads the file.
print_rolling_sync_warnings() {
  local stderr_file="$1"
  echo "argocd render parity smoke: drydock RollingSync warnings from ${stderr_file}:" >&2
  grep -E '^warning appset\.(invalid-rollout-config|rollout-no-steps|rollout-strategy-ignored): ' "${stderr_file}" >&2 || echo "(none)" >&2
}

# compare_rolling_sync_conditions compares the RollingSync findings of both
# sides. Argo CD and drydock must flag the same ApplicationSets as
# ROLLING_SYNC_CONDITIONS, and for each one drydock must warn with the
# expected code and the Argo CD condition message. It reads the drydock
# warnings from the get apps stderr that capture_drydock_applications keeps.
# assert_crd_rejected_application checks an Application that the API
# server rejects with the CRD schema, so Argo CD never holds it. drydock
# must fail it with the same message.
assert_crd_rejected_application() {
  local output exit_code=0
  log_step "Checking an Application that the CRD schema rejects"
  if output="$(kubectl apply --dry-run=server -f "${CRD_REJECTED_DIR}/apps/${CRD_REJECTED_APPLICATION}.yaml" 2>&1)"; then
    fail "the API server accepted ${CRD_REJECTED_APPLICATION}; want the CRD schema to reject it"
  fi
  [[ "${output}" == *"${CRD_REJECTED_MESSAGE}"* ]] \
    || fail "the API server rejected ${CRD_REJECTED_APPLICATION} without '${CRD_REJECTED_MESSAGE}': ${output}"
  output="$(cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" build app "argocd/${CRD_REJECTED_APPLICATION}" \
    --path "${CRD_REJECTED_DIR}" --offline 2>&1)" || exit_code="$?"
  [[ "${exit_code}" != "0" ]] \
    || fail "drydock rendered ${CRD_REJECTED_APPLICATION}; want the CRD schema failure"
  [[ "${output}" == *"${CRD_REJECTED_MESSAGE}"* ]] \
    || fail "drydock failed ${CRD_REJECTED_APPLICATION} without '${CRD_REJECTED_MESSAGE}': ${output}"
}

compare_rolling_sync_conditions() {
  local stderr_file="${OUT_DIR}/applications/drydock.stderr"
  local entry appset condition_type status reason message code
  local expected="" argocd drydock
  log_step "Comparing Argo CD RollingSync conditions with drydock warnings"
  for entry in "${ROLLING_SYNC_CONDITIONS[@]}"; do
    IFS='|' read -r appset condition_type status reason message code <<< "${entry}"
    [[ -n "${code}" ]] || continue
    expected+="${appset}"$'\n'
    if ! grep -F "warning ${code}: ApplicationSet argocd/${appset} " "${stderr_file}" | grep -qF ": ${message};"; then
      print_rolling_sync_warnings "${stderr_file}"
      fail "drydock did not warn ${code} for ApplicationSet argocd/${appset} with the Argo CD message \"${message}\"; see ${stderr_file}"
    fi
  done
  expected="$(printf '%s' "${expected}" | sort)"
  argocd="$(kubectl -n argocd get applicationsets.argoproj.io -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.conditions[?(@.type=="InvalidRolloutConfig")].status}{"\t"}{.status.conditions[?(@.reason=="ApplicationSetRolloutError")].status}{"\n"}{end}' \
    | awk -F'\t' '$2 == "True" || $3 == "True" { print $1 }' | sort)"
  drydock="$(grep -oE '^warning appset\.(invalid-rollout-config|rollout-no-steps): ApplicationSet argocd/[^ ]+' "${stderr_file}" \
    | sed 's|.*/||' | sort -u || true)"
  if [[ "${argocd}" != "${expected}" || "${drydock}" != "${expected}" ]]; then
    {
      printf 'expected flagged ApplicationSets:\n%s\n\n' "${expected}"
      printf 'Argo CD flagged ApplicationSets:\n%s\n\n' "${argocd}"
      printf 'drydock flagged ApplicationSets:\n%s\n' "${drydock}"
    } > "${OUT_DIR}/rolling-sync-conditions.diff"
    cat "${OUT_DIR}/rolling-sync-conditions.diff" >&2
    print_rolling_sync_warnings "${stderr_file}"
    fail "the ApplicationSets that Argo CD and drydock flag for RollingSync do not match; see ${OUT_DIR}/rolling-sync-conditions.diff"
  fi
  echo "RollingSync conditions: ${#ROLLING_SYNC_CONDITIONS[@]} cases match" >&2
}

assert_namespace_inventory() {
  local namespace="$1"
  local diff_file="$2"
  shift 2
  local expected actual
  expected="$(printf '%s\n' "$@" | sort)"
  actual="$(kubectl -n "${namespace}" get applications.argoproj.io -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)"
  if [[ "${actual}" != "${expected}" ]]; then
    {
      echo "expected Applications:"
      printf '%s\n' "${expected}"
      echo
      echo "actual Applications:"
      printf '%s\n' "${actual}"
    } > "${OUT_DIR}/${diff_file}"
    fail "Argo CD Application inventory in ${namespace} did not match expected list; see ${OUT_DIR}/${diff_file}"
  fi
}

assert_application_inventory() {
  assert_namespace_inventory argocd application-inventory.diff "${APPLICATIONS[@]}"
  assert_namespace_inventory "${TENANT_NAMESPACE}" tenant-application-inventory.diff "${TENANT_APPLICATIONS[@]}"
}

capture_argocd_manifest() {
  local app_ref="$1"
  local stem="$2"
  local output_dir="$3"
  for _ in {1..120}; do
    if argocd app manifests "${app_ref}" > "${output_dir}/${stem}.yaml" 2> "${OUT_DIR}/argocd-${stem}.stderr"; then
      rm -f "${OUT_DIR}/argocd-${stem}.stderr"
      break
    fi
    sleep 2
  done
  if [[ ! -s "${output_dir}/${stem}.yaml" ]]; then
    fail "Argo CD did not generate manifests for ${app_ref}; see ${OUT_DIR}/argocd-${stem}.stderr"
  fi
}

capture_argocd_manifests() {
  local app output_dir masked_dir
  output_dir="$(artifact_dir argocd-manifests)"
  # The Secrets-only AVP Application renders masked (++++++++) here, so it
  # stays out of the exact comparison and is recorded only as proof that the
  # sidecar generated it under Argo CD; the oracle compares it unmasked.
  masked_dir="$(artifact_dir avp-oracle/argocd-masked-manifests)"
  for app in "${APPLICATIONS[@]}"; do
    if [[ "${app}" == "${AVP_SECRET_APPLICATION}" ]]; then
      capture_argocd_manifest "${app}" "${app}" "${masked_dir}"
      continue
    fi
    capture_argocd_manifest "${app}" "${app}" "${output_dir}"
  done
  # Without this check, both sides rendering main would pass.
  grep -qF "name: ${SOURCE_HYDRATOR_BRANCH_CONFIGMAP}" "${output_dir}/${SOURCE_HYDRATOR_APPLICATION}.yaml" \
    || fail "Argo CD did not render ${SOURCE_HYDRATOR_BRANCH_CONFIGMAP} from branch ${SOURCE_HYDRATOR_BRANCH} for ${SOURCE_HYDRATOR_APPLICATION}"
  for app in "${TENANT_APPLICATIONS[@]}"; do
    capture_argocd_manifest "${TENANT_NAMESPACE}/${app}" "${app}" "${output_dir}"
  done
}

# drydock_build_args sets DRYDOCK_BUILD_ARGS to the offline `drydock build
# app` command line every capture shares. The optional second argument is
# the --repo-map target of the fixture URL (default FIXTURE_REPO_PATH).
drydock_build_args() {
  local app_ref="$1"
  local repo_map_path="${2:-${FIXTURE_REPO_PATH}}"
  DRYDOCK_BUILD_ARGS=(build app "${app_ref}"
    --path "${FIXTURE_REPO_PATH}"
    --repo-map "${FIXTURE_REPO_URL}=${repo_map_path}"
    --oci-cache-dir "${OCI_CACHE_DIR}"
    --chart-cache-dir "${CHART_CACHE_DIR}"
    --oci-ca-file "${OCI_CA_FILE}"
    --render-cache-dir "${OCI_OFFLINE_RENDER_CACHE_DIR}"
    --remote-cache-dir "${REMOTE_CACHE_DIR}"
    --appset-provider-fixture "${APPSET_PROVIDER_FIXTURE}"
    --offline)
}

capture_drydock_manifest() {
  local app_ref="$1"
  local stem="$2"
  local output_dir="$3"
  local plugin_app revision_app stderr_file repo_map_path
  local build_args
  repo_map_path="${FIXTURE_REPO_PATH}"
  for revision_app in "${APP_REVISION_GIT_APPLICATIONS[@]}" "${SOURCE_HYDRATOR_APPLICATION}" "${HELM_REPO_GIT_WORK_APPLICATIONS[@]}"; do
    if [[ "${stem}" == "${revision_app}" ]]; then
      [[ -n "${FIXTURE_GIT_WORK}" ]] \
        || fail "FIXTURE_GIT_WORK is unset; prepare_fixture_git_image must run before capturing ${stem}"
      repo_map_path="${FIXTURE_GIT_WORK}"
      break
    fi
  done
  drydock_build_args "${app_ref}" "${repo_map_path}"
  build_args=("${DRYDOCK_BUILD_ARGS[@]}")
  for plugin_app in "${PLUGIN_APPLICATIONS[@]}"; do
    if [[ "${stem}" == "${plugin_app}" ]]; then
      # Scoped to the plugin app on purpose: --plugin-policy-ref makes a
      # missing policy fatal for every app it is passed to, and each
      # invocation materializes the whole policy-repo tree into a temp dir.
      # exec engines also require a non-empty --plugin-policy-ref for trust,
      # so these flags are exactly what makes this render work at all.
      [[ -n "${FIXTURE_GIT_WORK}" ]] \
        || fail "FIXTURE_GIT_WORK is unset; prepare_fixture_git_image must run before capturing ${stem}"
      build_args+=(--enable-plugins
        --plugin-policy-repo "${FIXTURE_GIT_WORK}"
        --plugin-policy-ref HEAD)
      break
    fi
  done
  stderr_file="${OUT_DIR}/drydock-${stem}.stderr"
  if ! (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" "${build_args[@]}" \
    > "${output_dir}/${stem}.yaml" 2> "${stderr_file}"); then
    # The stderr file sits outside the uploaded artifact directories, so
    # print it into the job log where it is the only diagnosis.
    echo "argocd render parity smoke: drydock capture of ${app_ref} failed; its stderr follows" >&2
    cat "${stderr_file}" >&2 || true
    fail "drydock capture of ${app_ref} failed; see ${stderr_file}"
  fi
  if [[ "${stem}" == "${OCI_CHART_IGNORED_APPLICATION}" ]]; then
    # parity-oci-helm-chart-ignored must render AND warn: the capture runs
    # without --strict, so the advisory source.oci-chart-ignored line is the
    # only evidence that drydock ignored chart: and took the artifact path
    # rather than rendering the same manifests through the chart flow.
    require_drydock_capture_warning "${app_ref}" "${stderr_file}" \
      source.oci-chart-ignored \
      "an oci:// repoURL with chart: set must warn once and render the artifact"
  fi
  if [[ "${stem}" == "${SOURCE_HYDRATOR_APPLICATION}" ]]; then
    grep -qF "name: ${SOURCE_HYDRATOR_BRANCH_CONFIGMAP}" "${output_dir}/${stem}.yaml" \
      || fail "drydock did not render ${SOURCE_HYDRATOR_BRANCH_CONFIGMAP} from branch ${SOURCE_HYDRATOR_BRANCH} for ${app_ref}"
    # The manifest comparison proves that drydock renders the sync source.
    # The warning tells users that drydock does not run the hydrator.
    require_drydock_capture_warning "${app_ref}" "${stderr_file}" \
      source.hydrator-sync-source \
      "an Application with spec.sourceHydrator must warn that drydock renders the sync source"
  fi
  if [[ "${stem}" == "${COMPONENTS_DROPPED_APPLICATION}" ]]; then
    # The manifest comparison proves that drydock drops the later valid
    # component as Argo CD does. The warning is the only evidence that users
    # see the dropped resources.
    require_drydock_capture_warning "${app_ref}" "${stderr_file}" \
      kustomize.ignore-missing-components-dropped \
      "a valid component after the first valid one under ignoreMissingComponents must warn"
  fi
  rm -f "${stderr_file}"
}

# require_drydock_capture_warning fails the smoke when a drydock capture's
# stderr lacks the named warning code.
require_drydock_capture_warning() {
  local app_ref="$1"
  local stderr_file="$2"
  local code="$3"
  local reason="$4"
  if ! grep -qF "${code}" "${stderr_file}"; then
    echo "argocd render parity smoke: drydock capture of ${app_ref} did not warn ${code}; its stderr follows" >&2
    cat "${stderr_file}" >&2 || true
    fail "drydock capture of ${app_ref} rendered without the ${code} warning (${reason}); see ${stderr_file}"
  fi
}

# preflight_container_plugin_capture checks what drydock's container engine
# needs before the capture that uses it, so a host gap fails with a named
# cause rather than a generic plugin error. It mirrors drydock's own lookups:
# docker only on the controlled PATH, and (offline) an isolated empty Docker
# client config, so the default context's endpoint - DOCKER_HOST or
# /var/run/docker.sock - must hold the image, not the shell's current context.
preflight_container_plugin_capture() {
  local dir docker_path="" client_config
  for dir in /usr/local/bin /usr/bin /bin; do
    if [[ -f "${dir}/docker" && -x "${dir}/docker" ]]; then
      docker_path="${dir}/docker"
      break
    fi
  done
  [[ -n "${docker_path}" ]] \
    || fail "drydock's container engine looks up docker only on /usr/local/bin:/usr/bin:/bin and none of them has it (docker on PATH: $(command -v docker || echo none)); ${CONTAINER_PLUGIN_APPLICATION} cannot render"
  client_config="${WORK_DIR}/docker-preflight-config"
  mkdir -p "${client_config}"
  DOCKER_CONFIG="${client_config}" "${docker_path}" image inspect "${PARITY_ALPINE_POLICY_IMAGE}" >/dev/null 2>&1 \
    || fail "${docker_path} with an empty client config (as drydock runs it offline) cannot see ${PARITY_ALPINE_POLICY_IMAGE}; drydock runs it with --pull never, so ${CONTAINER_PLUGIN_APPLICATION} cannot render. Locally: unset DOCKER_CONTEXT, and if /var/run/docker.sock is absent set DOCKER_HOST to the endpoint \`docker context inspect\` reports"
}

capture_drydock_manifests() {
  local app output_dir
  output_dir="$(artifact_dir drydock-manifests)"
  preflight_container_plugin_capture
  for app in "${APPLICATIONS[@]}"; do
    # Rendered by capture_avp_oracle_manifests into the oracle directory, so
    # the masked comparison sees the same Application set on both sides.
    [[ "${app}" == "${AVP_SECRET_APPLICATION}" ]] && continue
    capture_drydock_manifest "argocd/${app}" "${app}" "${output_dir}"
  done
  for app in "${TENANT_APPLICATIONS[@]}"; do
    capture_drydock_manifest "${TENANT_NAMESPACE}/${app}" "${app}" "${output_dir}"
  done
}

# capture_avp_oracle_manifests is the second AVP oracle. `argocd app
# manifests` masks Secret data as ++++++++, so it cannot see a base64
# Secret.data substitution. Each oracle Application's source directory is
# copied into the argocd-vault-plugin sidecar's private /tmp and rendered
# there with `argocd-vault-plugin generate`: the same binary, AVP_TYPE,
# backend Secret and projected ServiceAccount token the Argo CD side used.
# That unmasked output and drydock's render of the same Application land in
# avp-oracle/ for compare_avp_oracle_manifests.
capture_avp_oracle_manifests() {
  local pod app source_path remote avp_dir drydock_dir stderr_file
  avp_dir="$(artifact_dir avp-oracle/argocd-vault-plugin-manifests)"
  drydock_dir="$(artifact_dir avp-oracle/drydock-manifests)"
  # One pod for both execs: a copy and a generate that landed on different
  # repo-server pods would render an empty directory.
  pod="$(kubectl -n argocd get pods -l app.kubernetes.io/name=argocd-repo-server \
    --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')" \
    || fail "could not list argocd-repo-server pods for the argocd-vault-plugin oracle"
  [[ -n "${pod}" ]] || fail "no running argocd-repo-server pod for the argocd-vault-plugin oracle"
  for app in "${AVP_ORACLE_APPLICATIONS[@]}"; do
    source_path="$(kubectl -n argocd get application "${app}" -o jsonpath='{.spec.source.path}')" \
      || fail "could not read spec.source.path of Application argocd/${app}"
    [[ -n "${source_path}" ]] || fail "Application argocd/${app} has no spec.source.path for the argocd-vault-plugin oracle"
    [[ -d "${FIXTURE_REPO_PATH}/${source_path}" ]] \
      || fail "Application argocd/${app} source path ${source_path} is not a directory under ${FIXTURE_REPO_PATH}"
    remote="/tmp/drydock-avp-oracle/${app}"
    # COPYFILE_DISABLE keeps macOS tar from adding ._* AppleDouble entries,
    # which AVP would read as YAML files.
    COPYFILE_DISABLE=1 tar -C "${FIXTURE_REPO_PATH}/${source_path}" -cf - . \
      | kubectl -n argocd exec -i "${pod}" -c argocd-vault-plugin -- \
        sh -c "rm -rf '${remote}' && mkdir -p '${remote}' && tar -xf - -C '${remote}'" \
      || fail "could not copy ${source_path} into the argocd-vault-plugin sidecar of ${pod} at ${remote}"
    stderr_file="${OUT_DIR}/avp-oracle/argocd-vault-plugin-${app}.stderr"
    if ! kubectl -n argocd exec "${pod}" -c argocd-vault-plugin -- \
      argocd-vault-plugin generate "${remote}" > "${avp_dir}/${app}.yaml" 2> "${stderr_file}"; then
      echo "argocd render parity smoke: argocd-vault-plugin generate of ${source_path} failed in the sidecar; its stderr follows" >&2
      cat "${stderr_file}" >&2 || true
      fail "argocd-vault-plugin generate of ${source_path} failed in the sidecar; see ${stderr_file}"
    fi
    rm -f "${stderr_file}"
    [[ -s "${avp_dir}/${app}.yaml" ]] \
      || fail "argocd-vault-plugin generate of ${source_path} in the sidecar produced no output"
    capture_drydock_manifest "argocd/${app}" "${app}" "${drydock_dir}"
  done
}

# count_avp_markers prints how many drydock-redacted- markers a manifest file
# carries: literally, plus inside every standard base64 token it contains. A
# substituted Secret.data value is base64 on both sides, so a literal count
# alone would let "neither side decoded anything" pass there.
count_avp_markers() {
  local file="$1" token
  {
    grep -o 'drydock-redacted-[0-9a-f]\{12\}' "${file}" || true
    while IFS= read -r token; do
      printf '%s' "${token}" | base64 -d 2>/dev/null \
        | grep -ao 'drydock-redacted-[0-9a-f]\{12\}' || true
    done < <(grep -oE '[A-Za-z0-9+/]{16,}={0,2}' "${file}" || true)
  } | wc -l | tr -d ' '
}

# check_avp_marker_count reports one file's marker count against the expected
# one and returns 1 on a mismatch, so assert_avp_markers can print every side
# before failing.
check_avp_marker_count() {
  local file="$1" want="$2" label="$3" count
  count="$(count_avp_markers "${file}")"
  echo "argocd render parity smoke: ${label} markers: ${count} (want ${want})" >&2
  [[ "${count}" == "${want}" ]]
}

# assert_avp_markers keeps "both sides left the placeholders alone" from
# passing the exact comparisons: the masked sides must render exactly
# AVP_EXPECTED_MARKERS markers for the AVP fixture, and the oracle sides
# AVP_EXPECTED_MARKERS and AVP_SECRET_EXPECTED_MARKERS for their two
# Applications.
assert_avp_markers() {
  local side mismatch="false"
  log_step "Counting redaction markers on every AVP side"
  for side in argocd drydock; do
    check_avp_marker_count "${OUT_DIR}/${side}-manifests/${AVP_APPLICATION}.yaml" \
      "${AVP_EXPECTED_MARKERS}" "${AVP_APPLICATION} ${side}" || mismatch="true"
  done
  for side in argocd-vault-plugin drydock; do
    check_avp_marker_count "${OUT_DIR}/avp-oracle/${side}-manifests/${AVP_APPLICATION}.yaml" \
      "${AVP_EXPECTED_MARKERS}" "${AVP_APPLICATION} oracle ${side}" || mismatch="true"
    check_avp_marker_count "${OUT_DIR}/avp-oracle/${side}-manifests/${AVP_SECRET_APPLICATION}.yaml" \
      "${AVP_SECRET_EXPECTED_MARKERS}" "${AVP_SECRET_APPLICATION} oracle ${side}" || mismatch="true"
  done
  [[ "${mismatch}" == "false" ]] \
    || fail "every AVP side must render exactly its expected drydock-redacted- marker count; see ${OUT_DIR}/argocd-manifests/${AVP_APPLICATION}.yaml, ${OUT_DIR}/drydock-manifests/${AVP_APPLICATION}.yaml and ${OUT_DIR}/avp-oracle/"
}

# assert_app_revision_captures fails the smoke unless every revision
# Application rendered its expected resolved value under Argo CD. The exact
# comparison then proves that drydock rendered the same.
assert_app_revision_captures() {
  local entry app text git_head git_tag_object file mismatch="false"
  log_step "Checking the resolved ARGOCD_APP_REVISION values on the Argo CD side"
  git_head="$(git -C "${FIXTURE_GIT_WORK}" rev-parse HEAD)" \
    || fail "could not read the HEAD commit of ${FIXTURE_GIT_WORK}"
  git_tag_object="$(git -C "${FIXTURE_GIT_WORK}" rev-parse "refs/tags/${APP_REVISION_ANNOTATED_TAG}")" \
    || fail "could not read the ${APP_REVISION_ANNOTATED_TAG} tag object of ${FIXTURE_GIT_WORK}"
  [[ "${git_tag_object}" != "${git_head}" ]] \
    || fail "${APP_REVISION_ANNOTATED_TAG} must be an annotated tag, whose object SHA differs from the commit SHA"
  for entry in "${APP_REVISION_EXPECTED[@]}"; do
    app="${entry%%|*}"
    text="${entry#*|}"
    text="${text//@GIT_HEAD@/${git_head}}"
    file="${OUT_DIR}/argocd-manifests/${app}.yaml"
    if ! grep -qF -- "${text}" "${file}"; then
      echo "argocd render parity smoke: ${file} does not contain '${text}'" >&2
      mismatch="true"
    fi
  done
  for app in "${APP_REVISION_TAG_OBJECT_ABSENT[@]}"; do
    file="${OUT_DIR}/argocd-manifests/${app}.yaml"
    if grep -qF -- "${git_tag_object}" "${file}"; then
      echo "argocd render parity smoke: ${file} contains the tag object SHA ${git_tag_object}" >&2
      mismatch="true"
    fi
  done
  [[ "${mismatch}" == "false" ]] \
    || fail "the Argo CD side must render the resolved revision for every ARGOCD_APP_REVISION Application; see ${OUT_DIR}/argocd-manifests/"
}

# compare_avp_oracle_manifests runs the exact per-resource comparison on the
# oracle output, with no Secret masking: the "argocd" side of the comparer's
# output is the sidecar's `argocd-vault-plugin generate` output here.
compare_avp_oracle_manifests() {
  log_step "Comparing argocd-vault-plugin sidecar output and drydock rendered manifests"
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-parity-compare \
    --argocd-dir "${OUT_DIR}/avp-oracle/argocd-vault-plugin-manifests" \
    --drydock-dir "${OUT_DIR}/avp-oracle/drydock-manifests" \
    --out-dir "${OUT_DIR}/compare-avp-oracle" \
    --ignore-file "${IGNORE_FILE}")
}

compare_manifests() {
  log_step "Comparing Argo CD and drydock rendered manifests"
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-parity-compare \
    --argocd-dir "${OUT_DIR}/argocd-manifests" \
    --drydock-dir "${OUT_DIR}/drydock-manifests" \
    --out-dir "${OUT_DIR}/compare" \
    --ignore-file "${IGNORE_FILE}")
}

# capture_argocd_applications records every Application the live Argo CD
# instance holds, both the committed ones and the ones the ApplicationSet
# controller generated. It runs before the project-policy smoke applies its
# own Applications.
# The Application in NAMESPACE_DISABLED exists as an object, but Argo CD
# never reconciles it, so it is not part of the inventory that drydock must
# match. assert_namespace_gate checks it.
capture_argocd_applications() {
  local output_dir
  output_dir="$(artifact_dir applications)"
  kubectl get applications.argoproj.io --all-namespaces \
    --field-selector "metadata.namespace!=${NAMESPACE_DISABLED}" -o json > "${output_dir}/argocd.json" \
    || fail "could not list the Argo CD Applications for the Application inventory comparison"
}

# assert_namespace_gate checks the objects in namespaces that Argo CD does
# not reconcile. Argo CD: the Application has no reconciledAt, and the API
# server refuses it to the admin user. It fails the namespace check and
# answers "permission denied", so it does not reveal whether the object
# exists (getAppEnforceRBAC and getApplicationEnforceRBACClient,
# server/application/application.go:177-286 at v3.6.1). The ApplicationSet
# has no status. assert_application_inventory
# already proved that its Application does not exist. drydock: get apps
# warns discovery.namespace-not-enabled for both, and compare_applications
# proves that it lists neither the Application nor anything the
# ApplicationSet would generate. It runs after the other Applications were
# reconciled, so the controllers had the time to act on these objects.
assert_namespace_gate() {
  local reconciled_at appset_status get_output stderr_file
  log_step "Checking objects in namespaces that Argo CD does not reconcile"
  reconciled_at="$(kubectl -n "${NAMESPACE_DISABLED}" get application "${NAMESPACE_DISABLED_APPLICATION}" -o jsonpath='{.status.reconciledAt}')"
  [[ -z "${reconciled_at}" ]] \
    || fail "Argo CD reconciled ${NAMESPACE_DISABLED}/${NAMESPACE_DISABLED_APPLICATION} at ${reconciled_at}; application.namespaces must not enable ${NAMESPACE_DISABLED}"
  if get_output="$(argocd app get "${NAMESPACE_DISABLED}/${NAMESPACE_DISABLED_APPLICATION}" 2>&1)"; then
    fail "the Argo CD API server returned ${NAMESPACE_DISABLED}/${NAMESPACE_DISABLED_APPLICATION}; want it refused"
  fi
  [[ "${get_output}" == *"permission denied"* ]] \
    || fail "argocd app get ${NAMESPACE_DISABLED}/${NAMESPACE_DISABLED_APPLICATION} failed without permission denied: ${get_output}"
  appset_status="$(kubectl -n "${TENANT_NAMESPACE}" get applicationset "${NAMESPACE_DISABLED_APPLICATIONSET}" -o jsonpath='{.status}')"
  [[ -z "${appset_status}" ]] \
    || fail "the ApplicationSet controller reconciled ${TENANT_NAMESPACE}/${NAMESPACE_DISABLED_APPLICATIONSET}: ${appset_status}"
  stderr_file="${OUT_DIR}/applications/drydock.stderr"
  grep -qF "discovery.namespace-not-enabled: Application ${NAMESPACE_DISABLED}/${NAMESPACE_DISABLED_APPLICATION} is skipped" "${stderr_file}" \
    || fail "drydock get apps did not warn that it skipped ${NAMESPACE_DISABLED}/${NAMESPACE_DISABLED_APPLICATION}; see ${stderr_file}"
  grep -qF "discovery.namespace-not-enabled: ApplicationSet ${TENANT_NAMESPACE}/${NAMESPACE_DISABLED_APPLICATIONSET} is skipped" "${stderr_file}" \
    || fail "drydock get apps did not warn that it skipped ${TENANT_NAMESPACE}/${NAMESPACE_DISABLED_APPLICATIONSET}; see ${stderr_file}"
}

# capture_drydock_applications lists every Application drydock discovers and
# generates from the fixture repository, with the same offline flags and
# caches as the manifest capture.
capture_drydock_applications() {
  local output_dir stderr_file
  output_dir="$(artifact_dir applications)"
  # Kept: compare_rolling_sync_conditions reads the warnings in it.
  stderr_file="${output_dir}/drydock.stderr"
  if ! (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" get apps \
    --path "${FIXTURE_REPO_PATH}" \
    --repo-map "${FIXTURE_REPO_URL}=${FIXTURE_REPO_PATH}" \
    --oci-cache-dir "${OCI_CACHE_DIR}" \
    --chart-cache-dir "${CHART_CACHE_DIR}" \
    --oci-ca-file "${OCI_CA_FILE}" \
    --appset-provider-fixture "${APPSET_PROVIDER_FIXTURE}" \
    --render-cache-dir "${OCI_OFFLINE_RENDER_CACHE_DIR}" \
    --offline \
    -o json > "${output_dir}/drydock.json" 2> "${stderr_file}"); then
    echo "argocd render parity smoke: drydock get apps failed; its stderr follows" >&2
    cat "${stderr_file}" >&2 || true
    fail "drydock get apps for the Application inventory comparison failed; see ${stderr_file}"
  fi
}

# compare_applications checks names and specs, not just counts: drydock must
# generate exactly the Applications Argo CD holds, with the same project,
# destination, and sources. The manifest capture loops over the fixed
# APPLICATIONS list, so an extra Application that only drydock generates
# (for example from a directory the repo-server walk skips) shows up only
# here.
compare_applications() {
  log_step "Comparing Argo CD and drydock Application inventories and specs"
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-parity-compare \
    --argocd-applications "${OUT_DIR}/applications/argocd.json" \
    --drydock-applications "${OUT_DIR}/applications/drydock.json" \
    --out-dir "${OUT_DIR}/compare-applications")
}

compare_tracking_manifests() {
  local app argocd_dir drydock_dir
  log_step "Comparing tracking metadata without ignore rules"
  argocd_dir="$(artifact_dir argocd-tracking-manifests)"
  drydock_dir="$(artifact_dir drydock-tracking-manifests)"
  for app in "${TRACKING_APPLICATIONS[@]}"; do
    cp "${OUT_DIR}/argocd-manifests/${app}.yaml" "${argocd_dir}/${app}.yaml"
    cp "${OUT_DIR}/drydock-manifests/${app}.yaml" "${drydock_dir}/${app}.yaml"
  done
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-parity-compare \
    --argocd-dir "${argocd_dir}" \
    --drydock-dir "${drydock_dir}" \
    --out-dir "${OUT_DIR}/compare-tracking")
}

ensure_namespace() {
  local namespace="$1"
  kubectl get namespace "${namespace}" >/dev/null 2>&1 || kubectl create namespace "${namespace}" >/dev/null
}

# The value must equal application.namespaces in
# repo/settings/argocd-cmd-params-cm.yaml, which drydock reads.
configure_application_namespaces() {
  kubectl -n argocd patch configmap argocd-cmd-params-cm --type merge \
    -p "{\"data\":{\"application.namespaces\":\"${TENANT_NAMESPACE},${DIRECTORY_DECODE_NAMESPACE},project-policy-tenant\"}}" >/dev/null
  kubectl -n argocd rollout restart deployment/argocd-server >/dev/null
  kubectl -n argocd rollout restart statefulset/argocd-application-controller >/dev/null
  kubectl -n argocd rollout status deployment/argocd-server --timeout=300s
  kubectl -n argocd rollout status statefulset/argocd-application-controller --timeout=300s
}

# The project-policy phase the functions below act on. use_project_policy_phase
# sets it: the artifact directory name, the drydock fixture repo, the
# directory in that repo that holds the phase's manifests, the expected file,
# and the namespace|application|category cases.
PROJECT_POLICY_PHASE=""
PROJECT_POLICY_PHASE_REPO=""
PROJECT_POLICY_PHASE_MANIFESTS=""
PROJECT_POLICY_PHASE_EXPECTED=""
PROJECT_POLICY_PHASE_CASES=()

# reset_project_policy_shared_state undoes the shared state that the later
# project-policy phases change: the cluster Secret named in-cluster and the
# argocd-cm cluster.inClusterEnabled key. It is safe to run more than once.
# A merge patch with a null value removes the key, and does nothing when the
# key is absent.
reset_project_policy_shared_state() {
  kubectl --request-timeout=30s -n argocd delete secret "${PROJECT_POLICY_IN_CLUSTER_SECRET_NAME}" --ignore-not-found >/dev/null
  kubectl --request-timeout=30s -n argocd patch configmap argocd-cm --type merge \
    -p '{"data":{"cluster.inClusterEnabled":null}}' >/dev/null
}

use_project_policy_phase() {
  PROJECT_POLICY_PHASE="$1"
  PROJECT_POLICY_PHASE_REPO="$2"
  PROJECT_POLICY_PHASE_MANIFESTS="$2/$3"
  PROJECT_POLICY_PHASE_EXPECTED="$4"
  shift 4
  PROJECT_POLICY_PHASE_CASES=("$@")
}

prepare_project_policy_namespaces() {
  ensure_namespace project-policy-tenant
  ensure_namespace project-policy-workloads
}

# A phase's cluster-secrets.yaml is in its drydock repo, so both Argo CD and
# drydock see those cluster Secrets.
apply_project_policy_apps() {
  if [[ -f "${PROJECT_POLICY_PHASE_MANIFESTS}/cluster-secrets.yaml" ]]; then
    kubectl apply -f "${PROJECT_POLICY_PHASE_MANIFESTS}/cluster-secrets.yaml" >/dev/null
  fi
  kubectl apply -f "${PROJECT_POLICY_PHASE_MANIFESTS}/projects.yaml" >/dev/null
  kubectl apply -f "${PROJECT_POLICY_PHASE_MANIFESTS}/applications.yaml" >/dev/null
}

# apply_project_policy_live_cluster_secrets applies the cluster Secrets that
# only Argo CD sees. The Secret points at the kind API server through a
# second address, so it needs credentials: a short-lived token for the
# controller's own ServiceAccount. The token goes to kubectl on stdin, so it
# never lands on disk or in a process argument. TLS verification is off
# because this is a throwaway kind cluster.
apply_project_policy_live_cluster_secrets() {
  local token
  kubectl apply -f "${PROJECT_POLICY_LIVE_CLUSTER_SECRETS}" >/dev/null
  token="$(kubectl -n argocd create token argocd-application-controller)"
  [[ -n "${token}" ]] || fail "could not create a token for the project-policy live cluster Secret"
  printf '{"stringData":{"config":"{\\"bearerToken\\":\\"%s\\",\\"tlsClientConfig\\":{\\"insecure\\":true}}"}}' "${token}" \
    | kubectl -n argocd patch secret "${PROJECT_POLICY_LIVE_CLUSTER_SECRET_NAME}" --type merge --patch-file /dev/stdin >/dev/null
}

# application_conditions prints one "<type>\t<message>" line per status
# condition of an Application, and nothing when it has none or is missing.
application_conditions() {
  local namespace="$1"
  local app="$2"
  kubectl -n "${namespace}" get application "${app}" -o jsonpath='{range .status.conditions[*]}{.type}{"\t"}{.message}{"\n"}{end}' 2>/dev/null || true
}

# application_has_condition_type succeeds when the Application has at least
# one status condition of the given type.
application_has_condition_type() {
  local namespace="$1"
  local app="$2"
  local expected_type="$3"
  local condition_type message
  while IFS=$'\t' read -r condition_type message; do
    [[ "${condition_type}" == "${expected_type}" ]] && return 0
  done < <(application_conditions "${namespace}" "${app}")
  return 1
}

project_policy_condition_categories() {
  local namespace="$1"
  local app="$2"
  local conditions condition_type message normalized category
  conditions="$(application_conditions "${namespace}" "${app}")"
  while IFS=$'\t' read -r condition_type message; do
    [[ -n "${message}" ]] || continue
    normalized="$(printf '%s' "${message}" | tr '[:upper:]' '[:lower:]')"
    category="unknown"
    if [[ "${condition_type}" == "UnknownError" && "${normalized}" == *" in namespace "* && "${normalized}" == *" is not permitted to use project "* ]]; then
      category="source-namespace"
    elif [[ "${condition_type}" == "InvalidSpecError" ]]; then
      if [[ "${normalized}" == *"source namespace"* ]]; then
        category="source-namespace"
      elif [[ "${normalized}" == *"error getting cluster"* || "${normalized}" == *"clusters with this name"* || "${normalized}" == *"clusters with the same name"* ]]; then
        # A destination Argo CD cannot resolve to a cluster. These messages
        # do not say "destination" (util/argo/argo.go:1152-1193, v3.6.1).
        category="destination"
      elif [[ "${normalized}" == *destination* ]]; then
        category="destination"
      elif [[ "${normalized}" == *source* || "${normalized}" == *repo* ]]; then
        category="source"
      fi
    else
      continue
    fi
    printf '%s\n' "${category}"
  done <<< "${conditions}"
}

project_policy_has_condition_category() {
  local namespace="$1"
  local app="$2"
  local expected="$3"
  local category
  while IFS= read -r category; do
    [[ "${category}" == "${expected}" ]] && return 0
  done < <(project_policy_condition_categories "${namespace}" "${app}")
  return 1
}

project_policy_has_condition() {
  local namespace="$1"
  local app="$2"
  [[ -n "$(project_policy_condition_categories "${namespace}" "${app}")" ]]
}

wait_for_project_policy_application() {
  local namespace="$1"
  local app="$2"
  local expected_category="$3"
  local reconciled_at
  for _ in {1..180}; do
    if ! kubectl -n "${namespace}" get application "${app}" >/dev/null 2>&1; then
      sleep 2
      continue
    fi
    if [[ "${expected_category}" == "none" ]]; then
      reconciled_at="$(kubectl -n "${namespace}" get application "${app}" -o jsonpath='{.status.reconciledAt}' 2>/dev/null || true)"
      if [[ -n "${reconciled_at}" ]] && ! project_policy_has_condition "${namespace}" "${app}"; then
        return 0
      fi
    elif project_policy_has_condition_category "${namespace}" "${app}" "${expected_category}"; then
      return 0
    fi
    sleep 2
  done
  local failure_dir
  failure_dir="$(artifact_dir "${PROJECT_POLICY_PHASE}")"
  kubectl -n "${namespace}" get application "${app}" -o yaml > "${failure_dir}/${app}.yaml" 2>&1 || true
  fail "project-policy Application ${namespace}/${app} did not reach expected policy condition category ${expected_category}; see ${failure_dir}/${app}.yaml"
}

wait_for_project_policy_applications() {
  local case_entry namespace app expected_category
  for case_entry in "${PROJECT_POLICY_PHASE_CASES[@]}"; do
    IFS='|' read -r namespace app expected_category <<< "${case_entry}"
    wait_for_project_policy_application "${namespace}" "${app}" "${expected_category}"
  done
}

capture_project_policy_argocd_applications() {
  local case_entry namespace app expected_category output_dir
  output_dir="$(artifact_dir "${PROJECT_POLICY_PHASE}/argocd-applications")"
  for case_entry in "${PROJECT_POLICY_PHASE_CASES[@]}"; do
    IFS='|' read -r namespace app expected_category <<< "${case_entry}"
    kubectl -n "${namespace}" get application "${app}" -o json > "${output_dir}/${app}.json"
  done
}

capture_project_policy_drydock_diagnostics() {
  local output_dir stderr_file
  output_dir="$(artifact_dir "${PROJECT_POLICY_PHASE}")"
  stderr_file="${output_dir}/drydock-diagnostics.stderr"
  (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" diag \
    --path "${PROJECT_POLICY_PHASE_REPO}" \
    --repo-map "${FIXTURE_REPO_URL}=${PROJECT_POLICY_PHASE_REPO}" \
    --offline \
    --render \
    --project-diagnostics all \
    -o json > "${output_dir}/drydock-diagnostics.json" 2> "${stderr_file}")
  rm -f "${stderr_file}"
}

compare_project_policy_smoke() {
  local output_dir stderr_file
  output_dir="$(artifact_dir "${PROJECT_POLICY_PHASE}")"
  stderr_file="${output_dir}/summary.stderr"
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-project-policy-smoke \
    --argocd-app-dir "${output_dir}/argocd-applications" \
    --drydock-diagnostics "${output_dir}/drydock-diagnostics.json" \
    --expected "${PROJECT_POLICY_PHASE_EXPECTED}" \
    --out "${output_dir}/summary.txt" 2> "${stderr_file}")
  rm -f "${stderr_file}"
}

run_project_policy_phase() {
  log_step "Preparing ${PROJECT_POLICY_PHASE} namespaces"
  prepare_project_policy_namespaces
  log_step "Applying ${PROJECT_POLICY_PHASE} AppProjects and Applications"
  apply_project_policy_apps
  log_step "Waiting for ${PROJECT_POLICY_PHASE} Application policy outcomes"
  wait_for_project_policy_applications
  log_step "Capturing ${PROJECT_POLICY_PHASE} Argo CD Application status"
  capture_project_policy_argocd_applications
  log_step "Capturing ${PROJECT_POLICY_PHASE} drydock diagnostics"
  capture_project_policy_drydock_diagnostics
  log_step "Comparing ${PROJECT_POLICY_PHASE} outcomes"
  compare_project_policy_smoke
}

run_project_policy_smoke() {
  use_project_policy_phase project-policy "${PROJECT_POLICY_REPO_PATH}" project-policy \
    "${PROJECT_POLICY_EXPECTED}" "${PROJECT_POLICY_CASES[@]}"
  log_step "Applying project-policy cluster Secrets that only Argo CD sees"
  apply_project_policy_live_cluster_secrets
  run_project_policy_phase

  use_project_policy_phase project-policy-in-cluster-secret "${PROJECT_POLICY_IN_CLUSTER_SECRET_DIR}/repo" \
    project-policy-in-cluster-secret "${PROJECT_POLICY_IN_CLUSTER_SECRET_DIR}/expected.yaml" \
    "${PROJECT_POLICY_IN_CLUSTER_SECRET_CASES[@]}"
  PROJECT_POLICY_SHARED_STATE_CHANGED="true"
  run_project_policy_phase
  log_step "Removing the cluster Secret named in-cluster"
  kubectl -n argocd delete secret "${PROJECT_POLICY_IN_CLUSTER_SECRET_NAME}" --ignore-not-found >/dev/null

  # drydock reads the same setting from the phase repo's argocd-cm.yaml.
  use_project_policy_phase project-policy-in-cluster-disabled "${PROJECT_POLICY_IN_CLUSTER_DISABLED_DIR}/repo" \
    project-policy-in-cluster-disabled "${PROJECT_POLICY_IN_CLUSTER_DISABLED_DIR}/expected.yaml" \
    "${PROJECT_POLICY_IN_CLUSTER_DISABLED_CASES[@]}"
  log_step "Disabling the in-cluster cluster in argocd-cm"
  kubectl -n argocd patch configmap argocd-cm --type merge \
    -p '{"data":{"cluster.inClusterEnabled":"false"}}' >/dev/null
  run_project_policy_phase
  log_step "Enabling the in-cluster cluster in argocd-cm again"
  reset_project_policy_shared_state
  PROJECT_POLICY_SHARED_STATE_CHANGED="false"
}

load_directory_decode_applications() {
  local app
  DIRECTORY_DECODE_APPLICATIONS=()
  while IFS= read -r app; do
    [[ -n "${app}" ]] && DIRECTORY_DECODE_APPLICATIONS+=("${app}")
  done < <(cd "${REPO_ROOT}" && go run ./scripts/argocd-directory-decode-smoke --list --expected "${DIRECTORY_DECODE_EXPECTED}")
  [[ "${#DIRECTORY_DECODE_APPLICATIONS[@]}" -gt 0 ]] \
    || fail "no directory decode cases listed in ${DIRECTORY_DECODE_EXPECTED}"
  DIRECTORY_DECODE_PLUGIN_APPLICATIONS=()
  while IFS= read -r app; do
    [[ -n "${app}" ]] && DIRECTORY_DECODE_PLUGIN_APPLICATIONS+=("${app}")
  done < <(cd "${REPO_ROOT}" && go run ./scripts/argocd-directory-decode-smoke --list --source plugin --expected "${DIRECTORY_DECODE_EXPECTED}")
}

# wait_for_directory_decode_application waits for the controller to record
# a ComparisonError on a decode failure Application, or an InvalidSpecError
# on a spec case. The comparison checks which type each case needs. A new Application's
# first comparison resolves its revision without the revision cache, so the
# controller records the error at once rather than after the repo error
# grace period (CompareAppState, controller/state.go at v3.6.1).
wait_for_directory_decode_application() {
  local app="$1"
  local failure_dir
  for _ in {1..180}; do
    if application_has_condition_type "${DIRECTORY_DECODE_NAMESPACE}" "${app}" ComparisonError \
      || application_has_condition_type "${DIRECTORY_DECODE_NAMESPACE}" "${app}" InvalidSpecError; then
      return 0
    fi
    sleep 2
  done
  failure_dir="$(artifact_dir directory-decode)"
  kubectl -n "${DIRECTORY_DECODE_NAMESPACE}" get application "${app}" -o yaml > "${failure_dir}/${app}.yaml" 2>&1 || true
  fail "directory decode Application ${DIRECTORY_DECODE_NAMESPACE}/${app} did not report a ComparisonError or an InvalidSpecError; see ${failure_dir}/${app}.yaml"
}

capture_directory_decode_argocd_applications() {
  local app output_dir
  output_dir="$(artifact_dir directory-decode/argocd-applications)"
  for app in "${DIRECTORY_DECODE_APPLICATIONS[@]}"; do
    kubectl -n "${DIRECTORY_DECODE_NAMESPACE}" get application "${app}" -o json > "${output_dir}/${app}.json"
  done
}

# capture_directory_decode_drydock_results runs `drydock build app` for each
# decode failure Application and records its exit code and stderr. A failed
# render is the expected outcome here, so it does not stop the smoke. A
# plugin case gets the plugin flags that capture_drydock_manifest gives the
# plugin Applications.
capture_directory_decode_drydock_results() {
  local app plugin_app output_dir exit_code
  local build_args
  output_dir="$(artifact_dir directory-decode/drydock-results)"
  for app in "${DIRECTORY_DECODE_APPLICATIONS[@]}"; do
    drydock_build_args "${DIRECTORY_DECODE_NAMESPACE}/${app}"
    build_args=("${DRYDOCK_BUILD_ARGS[@]}")
    [[ -n "${OOB_SYMLINK_WORK}" ]] \
      || fail "OOB_SYMLINK_WORK is unset; prepare_fixture_git_image must run before capturing ${app}"
    build_args+=(--repo-map "${OOB_SYMLINK_REPO_URL}=${OOB_SYMLINK_WORK}")
    for plugin_app in "${DIRECTORY_DECODE_PLUGIN_APPLICATIONS[@]}"; do
      if [[ "${app}" == "${plugin_app}" ]]; then
        [[ -n "${FIXTURE_GIT_WORK}" ]] \
          || fail "FIXTURE_GIT_WORK is unset; prepare_fixture_git_image must run before capturing ${app}"
        build_args+=(--enable-plugins
          --plugin-policy-repo "${FIXTURE_GIT_WORK}"
          --plugin-policy-ref HEAD)
        break
      fi
    done
    exit_code=0
    (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" "${build_args[@]}" \
      > "${output_dir}/${app}.yaml" 2> "${output_dir}/${app}.stderr") || exit_code="$?"
    printf '%s\n' "${exit_code}" > "${output_dir}/${app}.exit"
  done
}

compare_directory_decode_smoke() {
  local output_dir
  output_dir="$(artifact_dir directory-decode)"
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-directory-decode-smoke \
    --expected "${DIRECTORY_DECODE_EXPECTED}" \
    --namespace "${DIRECTORY_DECODE_NAMESPACE}" \
    --argocd-app-dir "${output_dir}/argocd-applications" \
    --drydock-dir "${output_dir}/drydock-results" \
    --out "${output_dir}/summary.txt")
}

run_directory_decode_smoke() {
  local app
  load_directory_decode_applications
  assert_namespace_inventory "${DIRECTORY_DECODE_NAMESPACE}" directory-decode-application-inventory.diff "${DIRECTORY_DECODE_APPLICATIONS[@]}"
  log_step "Waiting for directory decode failures in Argo CD"
  for app in "${DIRECTORY_DECODE_APPLICATIONS[@]}"; do
    wait_for_directory_decode_application "${app}"
  done
  log_step "Capturing directory decode Argo CD Application status"
  capture_directory_decode_argocd_applications
  log_step "Capturing directory decode drydock results"
  capture_directory_decode_drydock_results
  log_step "Comparing directory decode failures"
  compare_directory_decode_smoke
}

load_live_diff_applications() {
  local app
  LIVE_DIFF_APPLICATIONS=()
  while IFS= read -r app; do
    [[ -n "${app}" ]] && LIVE_DIFF_APPLICATIONS+=("${app}")
  done < <(cd "${REPO_ROOT}" && go run ./scripts/argocd-live-diff-smoke --list --expected "${LIVE_DIFF_EXPECTED}")
  [[ "${#LIVE_DIFF_APPLICATIONS[@]}" -gt 0 ]] \
    || fail "no live diff Applications listed in ${LIVE_DIFF_EXPECTED}"
  LIVE_DIFF_ERROR_APPLICATIONS=()
  while IFS= read -r app; do
    [[ -n "${app}" ]] && LIVE_DIFF_ERROR_APPLICATIONS+=("${app}")
  done < <(cd "${REPO_ROOT}" && go run ./scripts/argocd-live-diff-smoke --list-comparison-errors --expected "${LIVE_DIFF_EXPECTED}")
}

# apply_live_diff_settings adds the argocd-cm keys of LIVE_DIFF_SETTINGS to
# the live argocd-cm with server-side apply, and with no keys removes them
# again: server-side apply deletes the fields that its field manager set
# before and no longer lists. Other argocd-cm keys keep their values.
apply_live_diff_settings() {
  kubectl apply --server-side --field-manager="${LIVE_DIFF_FIELD_MANAGER}" \
    -f "${LIVE_DIFF_WORK}/${LIVE_DIFF_SETTINGS}" >/dev/null
}

remove_live_diff_settings() {
  kubectl --request-timeout=30s apply --server-side --field-manager="${LIVE_DIFF_FIELD_MANAGER}" -f - >/dev/null <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
YAML
}

# remove_live_diff_state deletes the live diff Applications and removes the
# live diff argocd-cm keys. The Applications have no resources finalizer, so
# their resources stay. It is safe to run more than once.
remove_live_diff_state() {
  kubectl --request-timeout=30s -n argocd delete applications.argoproj.io \
    "${LIVE_DIFF_APPLICATIONS[@]}" "${LIVE_DIFF_ERROR_APPLICATIONS[@]}" --ignore-not-found >/dev/null
  remove_live_diff_settings
}

# dump_live_diff_applications saves each live diff Application and fails
# the smoke with the given message.
dump_live_diff_applications() {
  local message="$1"
  local app failure_dir
  failure_dir="$(artifact_dir live-diff/failed)"
  for app in "${LIVE_DIFF_APPLICATIONS[@]}" "${LIVE_DIFF_ERROR_APPLICATIONS[@]}"; do
    kubectl -n argocd get application "${app}" -o yaml > "${failure_dir}/${app}.yaml" 2>&1 || true
  done
  fail "${message}; see ${failure_dir}"
}

# sync_live_diff_applications syncs every live diff Application at
# LIVE_DIFF_REVISION_A. Each one must then be Synced: at the first tag, the
# live objects match Git after normalization, or a later OutOfSync status
# would not prove anything.
sync_live_diff_applications() {
  local output_dir
  output_dir="$(artifact_dir live-diff)"
  argocd app sync "${LIVE_DIFF_APPLICATIONS[@]}" --timeout 300 > "${output_dir}/argocd-sync.log" 2>&1 \
    || dump_live_diff_applications "Argo CD could not sync the live diff Applications at ${LIVE_DIFF_REVISION_A} (log: ${output_dir}/argocd-sync.log)"
  argocd app wait "${LIVE_DIFF_APPLICATIONS[@]}" --sync --health --timeout 300 > "${output_dir}/argocd-wait.log" 2>&1 \
    || dump_live_diff_applications "the live diff Applications are not Synced and Healthy at ${LIVE_DIFF_REVISION_A} (log: ${output_dir}/argocd-wait.log)"
}

# compare_live_diff_revision_b applies the Applications of
# LIVE_DIFF_REVISION_B, as a parent Application would sync a changed
# Application spec. Then it points every live diff Application at that tag
# with a hard refresh, and waits until Argo CD has compared it. The
# controller removes the refresh annotation when the refresh is done.
compare_live_diff_revision_b() {
  local app revision_b state expected
  revision_b="$(git -C "${LIVE_DIFF_WORK}" rev-parse "${LIVE_DIFF_REVISION_B}^{commit}")"
  kubectl -n argocd apply -f "${LIVE_DIFF_WORK}/applications" >/dev/null
  for app in "${LIVE_DIFF_APPLICATIONS[@]}" "${LIVE_DIFF_ERROR_APPLICATIONS[@]}"; do
    kubectl -n argocd patch application "${app}" --type merge \
      -p "{\"metadata\":{\"annotations\":{\"argocd.argoproj.io/refresh\":\"hard\"}},\"spec\":{\"source\":{\"targetRevision\":\"${LIVE_DIFF_REVISION_B}\"}}}" >/dev/null
  done
  expected="${revision_b}|${LIVE_DIFF_REVISION_B}|"
  for app in "${LIVE_DIFF_APPLICATIONS[@]}" "${LIVE_DIFF_ERROR_APPLICATIONS[@]}"; do
    state=""
    for _ in {1..90}; do
      state="$(kubectl -n argocd get application "${app}" \
        -o jsonpath='{.status.sync.revision}{"|"}{.status.sync.comparedTo.source.targetRevision}{"|"}{.metadata.annotations.argocd\.argoproj\.io/refresh}')"
      [[ "${state}" == "${expected}" ]] && break
      sleep 2
    done
    [[ "${state}" == "${expected}" ]] \
      || dump_live_diff_applications "Argo CD did not compare Application argocd/${app} at ${LIVE_DIFF_REVISION_B} (last revision|targetRevision|refresh: ${state})"
  done
}

capture_live_diff_argocd_applications() {
  local app output_dir
  output_dir="$(artifact_dir live-diff/argocd-applications)"
  for app in "${LIVE_DIFF_APPLICATIONS[@]}" "${LIVE_DIFF_ERROR_APPLICATIONS[@]}"; do
    kubectl -n argocd get application "${app}" -o json > "${output_dir}/${app}.json"
  done
}

# live_diff_drydock_args sets LIVE_DIFF_DRYDOCK_ARGS to the flags that every
# drydock diff of the two tags shares. --show-ignored-fields turns off
# drydock's own Helm noise filter, so only Argo CD normalization applies.
live_diff_drydock_args() {
  LIVE_DIFF_DRYDOCK_ARGS=(--repo "${LIVE_DIFF_WORK}"
    --path "${LIVE_DIFF_WORK}"
    --ref "${LIVE_DIFF_REVISION_B}"
    --ref-orig "${LIVE_DIFF_REVISION_A}"
    --repo-map "${LIVE_DIFF_REPO_URL}=${LIVE_DIFF_WORK}"
    --render-cache-dir "${WORK_DIR}/render-cache-live-diff"
    --changed-only=false
    --show-ignored-fields
    --exit-code=false
    --offline
    -o json)
}

# capture_live_diff_drydock_diff diffs the two tags. It leaves out the
# Applications whose comparison must fail, because that failure fails the
# whole command, and keeps stderr for the expected warnings. Then it diffs
# each of those Applications on its own and keeps the exit code and stderr.
capture_live_diff_drydock_diff() {
  local app output_dir failure_dir stderr_file exit_code
  local diff_args=()
  output_dir="$(artifact_dir live-diff)"
  failure_dir="$(artifact_dir live-diff/drydock-failures)"
  stderr_file="${output_dir}/drydock-diff.stderr"
  live_diff_drydock_args
  diff_args=("${LIVE_DIFF_DRYDOCK_ARGS[@]}")
  for app in "${LIVE_DIFF_ERROR_APPLICATIONS[@]}"; do
    diff_args+=(--discover-ignore "applications/${app}.yaml")
  done
  if ! (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" diff apps "${diff_args[@]}" \
    > "${output_dir}/drydock-diff.json" 2> "${stderr_file}"); then
    echo "argocd render parity smoke: drydock diff of the live diff fixture failed; its stderr follows" >&2
    cat "${stderr_file}" >&2 || true
    fail "drydock diff of the live diff fixture failed; see ${stderr_file}"
  fi
  for app in "${LIVE_DIFF_ERROR_APPLICATIONS[@]}"; do
    exit_code=0
    (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" diff app "${app}" "${LIVE_DIFF_DRYDOCK_ARGS[@]}" \
      > "${failure_dir}/${app}.json" 2> "${failure_dir}/${app}.stderr") || exit_code="$?"
    printf '%s\n' "${exit_code}" > "${failure_dir}/${app}.exit"
  done
}

# capture_live_diff_drydock_renders renders each compared Application at
# LIVE_DIFF_REVISION_B, the checked-out tree of LIVE_DIFF_WORK. The
# comparison requires every expected resource in it, so a resource that
# drydock never rendered cannot pass as unchanged.
capture_live_diff_drydock_renders() {
  local app output_dir
  output_dir="$(artifact_dir live-diff/drydock-renders)"
  for app in "${LIVE_DIFF_APPLICATIONS[@]}"; do
    if ! (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" build app "${app}" \
      --path "${LIVE_DIFF_WORK}" \
      --repo-map "${LIVE_DIFF_REPO_URL}=${LIVE_DIFF_WORK}" \
      --render-cache-dir "${WORK_DIR}/render-cache-live-diff" \
      --offline \
      > "${output_dir}/${app}.yaml" 2> "${output_dir}/${app}.stderr"); then
      cat "${output_dir}/${app}.stderr" >&2 || true
      fail "drydock build of live diff Application ${app} failed; see ${output_dir}/${app}.stderr"
    fi
  done
}

compare_live_diff_smoke() {
  local output_dir
  output_dir="$(artifact_dir live-diff)"
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-live-diff-smoke \
    --expected "${LIVE_DIFF_EXPECTED}" \
    --argocd-app-dir "${output_dir}/argocd-applications" \
    --drydock-diff "${output_dir}/drydock-diff.json" \
    --drydock-stderr "${output_dir}/drydock-diff.stderr" \
    --drydock-failure-dir "${output_dir}/drydock-failures" \
    --drydock-render-dir "${output_dir}/drydock-renders" \
    --out "${output_dir}/summary.txt")
}

run_live_diff_smoke() {
  local namespace
  load_live_diff_applications
  for namespace in "${LIVE_DIFF_NAMESPACES[@]}"; do
    ensure_namespace "${namespace}"
  done
  log_step "Applying the live diff settings to argocd-cm"
  LIVE_DIFF_STATE_CHANGED="true"
  apply_live_diff_settings
  log_step "Applying and syncing the live diff Applications at ${LIVE_DIFF_REVISION_A}"
  kubectl -n argocd apply -f "${LIVE_DIFF_FIXTURE_PATH}/repo/applications" >/dev/null
  sync_live_diff_applications
  log_step "Comparing the live diff Applications at ${LIVE_DIFF_REVISION_B}"
  compare_live_diff_revision_b
  capture_live_diff_argocd_applications
  log_step "Capturing the drydock diff between ${LIVE_DIFF_REVISION_A} and ${LIVE_DIFF_REVISION_B}"
  capture_live_diff_drydock_diff
  capture_live_diff_drydock_renders
  log_step "Comparing live diff sync status"
  compare_live_diff_smoke
  log_step "Removing the live diff Applications and argocd-cm settings"
  remove_live_diff_state
  LIVE_DIFF_STATE_CHANGED="false"
}

# The argocd-cm keys that the source type gate phase changes.
SOURCE_TYPE_GATE_KEYS=(helm.enable kustomize.enable jsonnet.enable)

# save_source_type_gate_keys records the live values of the source type gate
# keys as a merge patch that restores them: a key that is missing, or empty,
# which Argo CD reads the same, is restored as missing.
save_source_type_gate_keys() {
  local key value patch=""
  for key in "${SOURCE_TYPE_GATE_KEYS[@]}"; do
    value="$(kubectl --request-timeout=30s -n argocd get configmap argocd-cm \
      -o go-template="{{ with .data }}{{ with index . \"${key}\" }}{{ . }}{{ end }}{{ end }}")"
    [[ -n "${patch}" ]] && patch+=","
    if [[ -z "${value}" ]]; then
      patch+="\"${key}\":null"
    else
      value="${value//\\/\\\\}"
      value="${value//\"/\\\"}"
      patch+="\"${key}\":\"${value}\""
    fi
  done
  SOURCE_TYPE_GATES_RESTORE="{\"data\":{${patch}}}"
}

# remove_source_type_gates_state deletes the source type gate Applications
# and restores the source type gate keys that save_source_type_gate_keys
# recorded, or removes them when nothing was recorded. The Applications
# have no resources finalizer, so the delete does not wait for a prune.
remove_source_type_gates_state() {
  kubectl --request-timeout=30s -n argocd delete -f "${SOURCE_TYPE_GATES_MANIFESTS}/applications.yaml" --ignore-not-found >/dev/null
  kubectl --request-timeout=30s -n argocd patch configmap argocd-cm --type merge -p "${SOURCE_TYPE_GATES_RESTORE:-${SOURCE_TYPE_GATES_RESET}}" >/dev/null
}

# reset_source_type_gate_keys removes the source type gate keys at the start
# of a run. The fixture install sets none of them, so a key that is there
# was left by an earlier run that failed under --keep-cluster, and it would
# turn tools off for every Application of this run.
reset_source_type_gate_keys() {
  kubectl --request-timeout=30s -n argocd patch configmap argocd-cm --type merge -p "${SOURCE_TYPE_GATES_RESET}" >/dev/null
}

# source_type_gates_capture_ok reports whether the Argo CD capture of a
# source type gate Application is the plain directory render: it holds the
# ConfigMap named like the Application and nothing that Helm, Kustomize, or
# Jsonnet would add. Without this check, both sides rendering with the tools
# on would pass the comparison.
source_type_gates_capture_ok() {
  local app="$1"
  local capture="$2"
  [[ -s "${capture}" ]] || return 1
  grep -qF "name: ${app}" "${capture}" || return 1
  ! grep -qE "name: (kustomized-|parity-gates-jsonnet-evaluated|parity-gates-chart-template|parity-oci-helm-nested)" "${capture}"
}

# capture_source_type_gates_manifest captures one source type gate
# Application. `argocd app manifests` asks the API server, which reads the
# keys from its settings manager and sends them to the repo-server with the
# manifest request. The settings manager reads argocd-cm through an
# informer, so the first request can come before it sees the patched keys.
# The capture is retried with a hard refresh until it shows the plain
# directory render.
capture_source_type_gates_manifest() {
  local app="$1"
  local output_dir="$2"
  local capture="${output_dir}/${app}.yaml"
  for _ in {1..60}; do
    if argocd app manifests "${app}" > "${capture}" 2> "${OUT_DIR}/argocd-${app}.stderr" \
      && source_type_gates_capture_ok "${app}" "${capture}"; then
      rm -f "${OUT_DIR}/argocd-${app}.stderr"
      return 0
    fi
    argocd app get "${app}" --hard-refresh >/dev/null 2>&1 || true
    sleep 2
  done
  fail "Argo CD did not render ${app} as a plain directory with the tools off; see ${capture} and ${OUT_DIR}/argocd-${app}.stderr"
}

run_source_type_gates_smoke() {
  local app argocd_dir drydock_dir stderr_file
  log_step "Turning Helm, Kustomize, and Jsonnet off in argocd-cm"
  save_source_type_gate_keys
  SOURCE_TYPE_GATES_STATE_CHANGED="true"
  kubectl -n argocd patch configmap argocd-cm --type merge -p "${SOURCE_TYPE_GATES_SETTINGS}" >/dev/null
  log_step "Applying the source type gate Applications"
  kubectl -n argocd apply -f "${SOURCE_TYPE_GATES_MANIFESTS}/applications.yaml" >/dev/null
  for app in "${SOURCE_TYPE_GATES_APPLICATIONS[@]}"; do
    wait_for_application argocd "${app}"
  done
  log_step "Capturing the source type gate manifests"
  argocd_dir="$(artifact_dir source-type-gates/argocd-manifests)"
  drydock_dir="$(artifact_dir source-type-gates/drydock-manifests)"
  for app in "${SOURCE_TYPE_GATES_APPLICATIONS[@]}"; do
    capture_source_type_gates_manifest "${app}" "${argocd_dir}"
    stderr_file="${OUT_DIR}/drydock-${app}.stderr"
    # The chart-only Application reads the chart that the OCI warm run
    # already holds in the chart cache.
    if ! (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" build app "argocd/${app}" \
      --path "${SOURCE_TYPE_GATES_REPO_PATH}" \
      --repo-map "${FIXTURE_REPO_URL}=${SOURCE_TYPE_GATES_REPO_PATH}" \
      --oci-cache-dir "${OCI_CACHE_DIR}" \
      --chart-cache-dir "${CHART_CACHE_DIR}" \
      --oci-ca-file "${OCI_CA_FILE}" \
      --offline > "${drydock_dir}/${app}.yaml" 2> "${stderr_file}"); then
      echo "argocd render parity smoke: drydock capture of ${app} failed; its stderr follows" >&2
      cat "${stderr_file}" >&2 || true
      fail "drydock capture of ${app} failed; see ${stderr_file}"
    fi
    rm -f "${stderr_file}"
  done
  log_step "Comparing the source type gate manifests"
  (cd "${REPO_ROOT}" && go run ./scripts/argocd-parity-compare \
    --argocd-dir "${argocd_dir}" \
    --drydock-dir "${drydock_dir}" \
    --out-dir "${OUT_DIR}/compare-source-type-gates" \
    --ignore-file "${IGNORE_FILE}")
  log_step "Restoring the Helm, Kustomize, and Jsonnet keys in argocd-cm"
  remove_source_type_gates_state
  SOURCE_TYPE_GATES_STATE_CHANGED="false"
}

# set_kustomize_build_options sets kustomize.buildOptions on the live
# argocd-cm. Other argocd-cm keys keep their values.
set_kustomize_build_options() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  kubectl --request-timeout=30s -n argocd patch configmap argocd-cm --type merge \
    -p "{\"data\":{\"kustomize.buildOptions\":\"${value}\"}}" >/dev/null
}

# remove_kustomize_build_options_state deletes the Applications of every
# kustomize build options phase and sets kustomize.buildOptions back to the
# value of the fixture install. The Applications have no resources
# finalizer, so the delete does not wait for a prune.
remove_kustomize_build_options_state() {
  local phase
  for phase in "${KUSTOMIZE_BUILD_OPTIONS_PHASES[@]}"; do
    kubectl --request-timeout=30s -n argocd delete \
      -f "${KUSTOMIZE_BUILD_OPTIONS_PATH}/${phase}/repo/kbuild-${phase}/applications.yaml" --ignore-not-found >/dev/null
  done
  set_kustomize_build_options "${KUSTOMIZE_BUILD_OPTIONS_DEFAULT}"
}

# capture_kustomize_build_options_manifest captures one render Application
# of a phase. `argocd app manifests` asks the API server, whose settings
# manager reads argocd-cm through an informer, so a capture can still use
# the value of the earlier phase. The capture is retried with a hard
# refresh until it holds the present text and not the absent text of the
# expected.psv line. An empty text is not checked, and a capture may be
# empty when the phase sends the build elsewhere.
capture_kustomize_build_options_manifest() {
  local app="$1"
  local present="$2"
  local absent="$3"
  local output_dir="$4"
  local capture="${output_dir}/${app}.yaml"
  for _ in {1..60}; do
    if argocd app manifests "${app}" > "${capture}" 2> "${OUT_DIR}/argocd-${app}.stderr" \
      && { [[ -z "${present}" ]] || grep -qF -- "${present}" "${capture}"; } \
      && { [[ -z "${absent}" ]] || ! grep -qF -- "${absent}" "${capture}"; }; then
      rm -f "${OUT_DIR}/argocd-${app}.stderr"
      return 0
    fi
    argocd app get "${app}" --hard-refresh >/dev/null 2>&1 || true
    sleep 2
  done
  fail "Argo CD did not render ${app} with the kustomize.buildOptions of its phase (want \"${present}\", not \"${absent}\"); see ${capture} and ${OUT_DIR}/argocd-${app}.stderr"
}

# wait_for_kustomize_build_options_failure waits until the Application has
# a ComparisonError condition that contains the expected text. The message
# can span lines, because it quotes the kustomize error. The controller
# reads argocd-cm through its own informer, so the loop asks for a hard
# refresh while the condition differs.
wait_for_kustomize_build_options_failure() {
  local app="$1"
  local message="$2"
  local failure_dir messages
  for _ in {1..90}; do
    messages="$(kubectl -n argocd get application "${app}" \
      -o jsonpath='{range .status.conditions[?(@.type=="ComparisonError")]}{.message}{"\n"}{end}' 2>/dev/null || true)"
    if [[ "${messages}" == *"${message}"* ]]; then
      return 0
    fi
    argocd app get "${app}" --hard-refresh >/dev/null 2>&1 || true
    sleep 2
  done
  failure_dir="$(artifact_dir kustomize-build-options)"
  kubectl -n argocd get application "${app}" -o yaml > "${failure_dir}/${app}.yaml" 2>&1 || true
  fail "Application argocd/${app} did not report a ComparisonError that contains \"${message}\"; see ${failure_dir}/${app}.yaml"
}

# drydock_kustomize_build_options builds one Application of a phase repo
# offline. Its stdout and stderr go to the two files.
drydock_kustomize_build_options() {
  local repo="$1"
  local app="$2"
  local stdout_file="$3"
  local stderr_file="$4"
  (cd "${REPO_ROOT}" && "${DRYDOCK_CMD[@]}" build app "argocd/${app}" \
    --path "${repo}" \
    --repo-map "${FIXTURE_REPO_URL}=${repo}" \
    --offline > "${stdout_file}" 2> "${stderr_file}")
}

run_kustomize_build_options_phase() {
  local phase="$1"
  local phase_dir="${KUSTOMIZE_BUILD_OPTIONS_PATH}/${phase}"
  local repo="${phase_dir}/repo"
  local manifests="${repo}/kbuild-${phase}"
  local argocd_dir drydock_dir kind app first second stderr_file renders=0
  log_step "Setting kustomize.buildOptions for the ${phase} phase"
  KUSTOMIZE_BUILD_OPTIONS_STATE_CHANGED="true"
  kubectl --request-timeout=30s -n argocd patch configmap argocd-cm --type merge \
    --patch-file "${manifests}/argocd-cm.yaml" >/dev/null
  kubectl -n argocd apply -f "${manifests}/applications.yaml" >/dev/null
  argocd_dir="$(artifact_dir "kustomize-build-options/${phase}/argocd-manifests")"
  drydock_dir="$(artifact_dir "kustomize-build-options/${phase}/drydock-manifests")"
  # The loop reads the file on descriptor 3, so the commands in it cannot
  # read its lines from stdin.
  while IFS='|' read -r -u 3 kind app first second; do
    [[ -z "${kind}" || "${kind}" == \#* ]] && continue
    wait_for_application argocd "${app}"
    stderr_file="${OUT_DIR}/drydock-${app}.stderr"
    case "${kind}" in
      render)
        capture_kustomize_build_options_manifest "${app}" "${first}" "${second}" "${argocd_dir}"
        if ! drydock_kustomize_build_options "${repo}" "${app}" "${drydock_dir}/${app}.yaml" "${stderr_file}"; then
          echo "argocd render parity smoke: drydock capture of ${app} failed; its stderr follows" >&2
          cat "${stderr_file}" >&2 || true
          fail "drydock capture of ${app} failed; see ${stderr_file}"
        fi
        renders=$((renders + 1))
        ;;
      fail)
        wait_for_kustomize_build_options_failure "${app}" "${first}"
        if drydock_kustomize_build_options "${repo}" "${app}" /dev/null "${stderr_file}"; then
          fail "drydock rendered ${app}, want it to fail with \"${second}\""
        fi
        grep -qF -- "${second}" "${stderr_file}" \
          || fail "drydock failed ${app} without \"${second}\"; see ${stderr_file}"
        echo "kustomize build options ${phase}: ${app} fails on both sides" >&2
        ;;
      *)
        fail "unknown line kind ${kind} in ${phase_dir}/expected.psv"
        ;;
    esac
    rm -f "${stderr_file}"
  done 3< "${phase_dir}/expected.psv"
  if [[ "${renders}" -gt 0 ]]; then
    log_step "Comparing the kustomize build options ${phase} manifests"
    (cd "${REPO_ROOT}" && go run ./scripts/argocd-parity-compare \
      --argocd-dir "${argocd_dir}" \
      --drydock-dir "${drydock_dir}" \
      --out-dir "${OUT_DIR}/compare-kustomize-build-options-${phase}" \
      --ignore-file "${IGNORE_FILE}")
  fi
  kubectl --request-timeout=30s -n argocd delete -f "${manifests}/applications.yaml" --ignore-not-found >/dev/null
}

run_kustomize_build_options_smoke() {
  local phase
  for phase in "${KUSTOMIZE_BUILD_OPTIONS_PHASES[@]}"; do
    run_kustomize_build_options_phase "${phase}"
  done
  log_step "Restoring kustomize.buildOptions in argocd-cm"
  remove_kustomize_build_options_state
  KUSTOMIZE_BUILD_OPTIONS_STATE_CHANGED="false"
}

main() {
  local argocd_version
  argocd_version="$(resolve_argocd_version)"
  echo "Argo CD render parity smoke: ${argocd_version}" >&2
  log_step "Ensuring /etc/hosts entry for the OCI registry"
  ensure_registry_hosts_entry
  log_step "Generating OCI registry TLS certificate"
  generate_registry_certificate
  log_step "Installing Argo CD CLI ${argocd_version}"
  install_argocd_cli "${argocd_version}"
  if [[ "${CREATE_CLUSTER}" == "true" ]]; then
    log_step "Creating kind cluster ${CLUSTER_NAME}"
    kind create cluster --name "${CLUSTER_NAME}"
  else
    log_step "Using existing kind cluster ${CLUSTER_NAME}"
    kind export kubeconfig --name "${CLUSTER_NAME}"
  fi
  # Before the Git server: a fallback-mirror pull rewrites the container
  # plugin policy in the git snapshot that step commits.
  log_step "Preparing the pinned container plugin image"
  prepare_parity_alpine_image
  log_step "Preparing the argocd-vault-plugin sidecar image"
  prepare_avp_image
  log_step "Preparing fixture Git server"
  prepare_fixture_git_image
  install_fixture_git_server
  log_step "Preparing fixture OCI registry"
  prepare_registry_image
  install_fixture_registry
  log_step "Starting OCI registry port-forward"
  start_registry_port_forward
  log_step "Pushing OCI parity artifacts"
  push_oci_artifact "${OCI_ARTIFACT_PATH}" "${OCI_ARTIFACT_REPOSITORY}" "${OCI_ARTIFACT_TAG}" config
  verify_oci_artifact_manifest "${OCI_ARTIFACT_REPOSITORY}" "${OCI_ARTIFACT_TAG}" config
  push_oci_artifact "${OCI_APPSET_ARTIFACT_PATH}" "${OCI_APPSET_ARTIFACT_REPOSITORY}" "${OCI_APPSET_ARTIFACT_TAG}" appset
  verify_oci_artifact_manifest "${OCI_APPSET_ARTIFACT_REPOSITORY}" "${OCI_APPSET_ARTIFACT_TAG}" appset
  log_step "Pushing nested OCI parity Helm chart"
  push_oci_chart
  verify_oci_chart_manifest
  log_step "Installing Argo CD ${argocd_version}"
  install_argocd "${argocd_version}"
  log_step "Installing the parity CMP sidecar"
  install_cmp_sidecar "${argocd_version}"
  # Restarting argocd-server must happen before login_argocd starts the
  # port-forward it binds to a single server pod.
  log_step "Enabling Applications in tenant namespaces"
  configure_application_namespaces
  log_step "Logging in to Argo CD"
  login_argocd
  log_step "Removing source type gate keys that an earlier run left in argocd-cm"
  reset_source_type_gate_keys
  # A run that failed under --keep-cluster can leave the value of a
  # kustomize build options phase, which changes every Kustomize render.
  set_kustomize_build_options "${KUSTOMIZE_BUILD_OPTIONS_DEFAULT}"
  log_step "Applying parity fixture Applications"
  apply_fixture_apps
  log_step "Waiting for the OCI generator ApplicationSets"
  wait_for_oci_applicationsets
  log_step "Waiting for expected Applications"
  wait_for_applications
  log_step "Waiting for expected ApplicationSet errors"
  wait_for_applicationset_errors
  log_step "Waiting for RollingSync ApplicationSet conditions"
  wait_for_rolling_sync_conditions
  assert_application_inventory
  log_step "Capturing the Argo CD Application inventory"
  capture_argocd_applications
  log_step "Waiting for the OCI Application comparison"
  wait_for_oci_application
  log_step "Capturing Argo CD rendered manifests"
  capture_argocd_manifests
  log_step "Warming drydock OCI artifact and chart caches"
  warm_drydock_oci_cache
  log_step "Warming drydock Helm repository caches"
  warm_drydock_helm_repo_cache
  log_step "Capturing drydock rendered manifests"
  capture_drydock_manifests
  log_step "Capturing the drydock Application inventory"
  capture_drydock_applications
  log_step "Rendering the argocd-vault-plugin oracle inside the sidecar"
  capture_avp_oracle_manifests
  assert_avp_markers
  assert_app_revision_captures
  compare_manifests
  compare_applications
  assert_namespace_gate
  assert_crd_rejected_application
  compare_rolling_sync_conditions
  compare_applicationset_errors
  compare_avp_oracle_manifests
  compare_tracking_manifests
  run_live_diff_smoke
  run_directory_decode_smoke
  if [[ "${RUN_PROJECT_POLICY_SMOKE}" == "true" ]]; then
    run_project_policy_smoke
  else
    log_step "Skipping project-policy smoke"
  fi
  run_source_type_gates_smoke
  run_kustomize_build_options_smoke
  run_missing_project_smoke
  echo "Argo CD render parity smoke complete: ${OUT_DIR}" >&2
}

main
