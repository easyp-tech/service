# What and why

<!--
What changes, and what made it worth changing. The diff shows the first; only
you know the second, and it is what a reader six months from now will want.
-->

## Checks that are easy to miss

These are the ones this repository has actually been caught by. Delete the
lines that do not apply.

- [ ] **Touched `deploy/`** — ran `bash deploy/charts/easyp-service/tests/render.sh`.
      It reads more than the chart: certificate mounts, mimir's rule paths in
      every compose file that mounts its config, the alert-name parity between
      the chart and the compose stack, and whether the default image tag is
      shaped like one that exists.
- [ ] **Added or renamed an alert** — wrote the matching section in
      `docs/RUNBOOKS.md`. The heading is the anchor its `runbook_url` points
      at, so the test above fails without it.
- [ ] **Changed a config default** — checked `easyp-svc config print --changed`
      still shows what a deployment actually overrides. The dev configs carry
      only differences from the defaults, so a changed default silently changes
      what those files mean.
- [ ] **Changed metric or label names** — the dashboards and the alert rules
      both name them literally, and neither fails loudly when a name goes away.
      An empty panel reads as "nothing is happening".

## Before merging

Seven checks are required and the branch has to be up to date with `master`.
`gh pr merge --squash --auto` will wait for both and merge on its own.
