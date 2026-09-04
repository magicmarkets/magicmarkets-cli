# Security Policy

## Supported Versions

Only the latest release of `magicmarkets-cli` receives security fixes.
There are no maintained older major versions.

## Reporting a Vulnerability

**Do not open a public issue for a security vulnerability.**

Please use GitHub's [private vulnerability reporting](../../security/advisories/new)
for this repository (the "Report a vulnerability" button under the
Security tab). This lets you share details, proof-of-concept code, and
affected versions privately with maintainers before anything is public.

Report anything that could let an attacker:

- read or exfiltrate a user's `MAGICMARKETS_API_KEY` or other credentials
- place, modify, or close orders the user did not authorize
- bypass the trading gate (`MAGICMARKETS_ALLOW_TRADING`) on a
  money-moving CLI command or MCP tool
- execute arbitrary code via crafted API responses, config files, or CLI
  input

We'll acknowledge new reports as promptly as we can and follow up with next
steps once the issue is confirmed.

## Scope

This policy covers the `magicmarkets-cli` source code in this repository
(the CLI, its MCP server, and the `internal/magicmarkets` client library).
It does not cover the Magic Markets trading platform or API itself — for
issues with magicmarkets.com or its backend, see that site for a contact
path.
