# Security policy

Velo holds passwords, session and API tokens, and uploaded files, so security bugs matter even on a small self-hosted install.

## Reporting a vulnerability

Report it privately through [GitHub's private vulnerability reporting](https://github.com/parabol123654/velo/security/advisories/new), not in a public issue, pull request or discussion.

Include what you can of:

- the Velo version (`velo version`) or commit, and binary or Docker;
- the steps to reproduce, or a proof of concept;
- what an attacker gains (for example: reads another project with a project-restricted token, signs in without a password);
- whether it needs a valid account or token, or `--trust-proxy`.

Velo has one maintainer, so replies are best effort. You will hear back once the report has been read, and again when a fix is ready. Please wait for the fixed release before publishing details; you will be credited in the advisory unless you ask not to be.

## Supported versions

Fixes go into the next release from `main`. Only the latest release gets security fixes; upgrade in place (older databases open in newer versions).

## In scope

Anything that breaks the rules in [SPEC.md](./SPEC.md#accounts-and-access), for example:

- authentication, sessions, login throttling, invites;
- API token scopes (read-only, project-restricted) and the SSE stream filtering;
- uploads: who can read them, content types served inline;
- the GitHub webhook signature check;
- MCP, CLI or agent runner behavior that lets issue text run commands.

Out of scope: attacks that need admin access or access to the server's data directory, missing hardening on an install exposed without HTTPS, and denial of service by an authenticated user.
