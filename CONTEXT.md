# drydock Context

This file names domain terms used by maintainers and agents while changing
drydock. It is not a user guide and does not replace the canonical behavior
documents in `docs/`.

## Domain Terms

**Desired State** is the rendered Kubernetes resource set Argo CD would
reconcile for an Application source. drydock compares desired state without
querying live Argo CD or Kubernetes.

**Application** is an Argo CD `Application` discovered from repository files,
ApplicationSet output, explicit rendered discovery, or trusted plugin bootstrap
output.

**Application Source** is one entry from an Application's `spec.source` or
`spec.sources` after Argo CD source precedence and `$ref` resolution rules are
applied.

**Plugin Policy** is drydock's trusted policy contract for config management
plugin compatibility beyond built-in native adapters.

**Policy-Backed Plugin Render** is a render of an Application Source selected
by Plugin Policy. Native policy engines stay in process. Command-backed exec
and container engines require trusted policy provenance plus explicit
`--enable-plugins`.

**Rendered Diff Report** is the static HTML artifact generated from a desired
state diff. It is self-contained and does not require runtime assets.

**Cache Entry** is a lifecycle-managed Git, chart, or remote-resource cache
root recognized by `drydock cache`.

**Plugin Cache Mount** is durable render-time material for trusted container
plugins. It is controlled by Plugin Policy and is separate from `drydock cache`
lifecycle entries.

## Internal Architecture Terms

These terms are maintainer vocabulary for implementation shape. They are not
new user-facing product names.

**Repository Side** is one local repository tree participating in analysis,
such as the current tree, baseline tree, left diff side, or right diff side.

**Build Side** is the internal processing of one Repository Side through policy
loading, discovery, settings, Application selection, render cache reuse,
rendering, diagnostics, statuses, and cache events.
