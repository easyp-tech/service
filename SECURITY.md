# Security policy

## Supported versions

Fixes land on the latest minor release line, currently **1.3.x**, and ship as a
patch release. Older lines do not get backports: upgrade to the latest patch
release of the current line.

This covers the service (`easyp-svc`), its container image, the Helm chart
(`oci://ghcr.io/easyp-tech/charts/easyp-service`) and the `api` and `sdk` Go
modules.

## Reporting a vulnerability

Do not open a public issue. Report it privately through GitHub:
**Security → Report a vulnerability** on this repository
(<https://github.com/easyp-tech/service/security/advisories/new>).

Include what makes it reproducible:

- the version (`easyp-svc --version`, or the image tag);
- the licence tier (community or enterprise);
- how the service is deployed (Helm, Docker Compose, binary);
- the relevant configuration, **with secrets removed**;
- the steps that trigger it and what an attacker gains.

The report stays private between you and the maintainers until a fix is
released. The fix is published as a GitHub security advisory alongside the patch
release, crediting the reporter unless you ask otherwise.

## What is not a vulnerability

Some behaviour is a documented limitation rather than a defect. These are
described in the README, together with what to do about them:

- [plugins run as local processes without a sandbox](README.md#plugins-are-not-sandboxed):
  registering a plugin is as privileged as running code on the host;
- [there is no user model](README.md#there-is-no-user-model): one shared list of
  write tokens, no per-user identity.

A report that one of these can be used to do exactly what the README says it
can is not a vulnerability. A way around a boundary the README says *does*
hold — the plugin's environment, the plugins directory, output size, timeouts,
authentication, licence limits — is, and is welcome.
