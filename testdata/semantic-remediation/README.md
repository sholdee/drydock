# Semantic Remediation Fixtures

These fixtures are static Phase 0 inputs for future semantic remediation
checks. Pending and documented-boundary cases are intentionally registered as
fixture inventory before drydock behavior changes are implemented.

Keep this tree portable and offline:

- use only relative fixture paths and `example.invalid` source URLs
- do not add maintainer-local repository paths
- do not include real credentials or secret values
- do not add scripts that require network, cluster, Argo CD, or CLI shellouts

Fixture directories may be syntactically valid without being active test
inputs yet. Activation belongs in focused tests or the semantic fixture
registry, not in these static files alone.
