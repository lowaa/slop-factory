# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/lowaa/slop-factory/security/advisories/new),
not in a public issue or pull request.

Include what you found, how to reproduce it, and the commit or image tag you
tested against. You'll get an acknowledgement within a week, and a fix or a
plan for one once the report is confirmed. Once a fix is released, the
advisory is published with credit to you unless you'd rather stay anonymous.

## Supported versions

Fixes land on `main` and ship in the next image published from it
(`ghcr.io/lowaa/slop-haus-api`, `ghcr.io/lowaa/slop-haus-frontend`). Older
`v*` releases are not patched.

## Scope

In scope: slop-haus (the server, the web UI, the supervisor and the scripts
it hands to agent workspaces) and the libraries in this repository.

Out of scope: the coding agents slop-haus drives and what they do with the
permissions you give them, and deployments that don't follow the
configuration the documentation describes.
