# Security Policy

Margince handles customer relationship data under a multi-tenant,
agent-governed security model. Reports about weaknesses in that model
are welcome and taken seriously.

## Reporting a vulnerability

Please report vulnerabilities **privately** via GitHub Security
Advisories on the `gradionhq` repository ("Security" tab → "Report a
vulnerability"). Do not open a public issue or pull request for a
security finding — a public report before a fix ships puts every
deployment at risk.

What to include: the affected endpoint/tool/component, a minimal
reproduction (requests, payloads, or a failing test), and the impact you
believe it has (cross-tenant read, privilege escalation, agent
governance bypass, …). We will acknowledge the report, keep you informed
through the advisory thread, and credit you in the fix unless you prefer
otherwise.

## Scope

In scope — anything that breaks a documented security invariant of this
codebase, in particular:

- **Tenant isolation**: reading or writing another workspace's rows
  despite RLS and the composite same-workspace foreign keys.
- **Row-scope / RBAC**: access to records outside the caller's
  own/team/all scope, including via error, replay, or conflict paths
  (existence-hiding is a contract: out-of-scope answers 404).
- **Agent governance (ADR-0055)**: executing a 🟡 action without an
  approval, redeeming an approval twice or across content changes,
  agent self-approval, exceeding the granting human's rights, or a
  mutating operation admitted without a declared tier.
- **Authentication**: session or passport forgery, fixation, or a
  revoked credential that still binds.
- **The write shape**: a mutation that skips the audit or outbox row,
  or provenance accepted from a request body.
- **Injection and SSRF** in any handler, tool, or connector.

Out of scope: vulnerabilities in third-party dependencies without a
demonstrated impact here (report those upstream), findings requiring a
compromised host or database, denial-of-service against a dev
deployment (`MARGINCE_ENV=dev` deliberately relaxes trust switches), and
issues in the separate specification repository.

## Supported versions

This is a pre-release proof of concept: **only the `main` branch is
supported**. There are no release branches yet and no backports; fixes
land on `main`.

## No bounty

There is currently no bug bounty program and no promise of monetary
reward. We do credit reporters in the advisory and the changelog.
