# Security Policy

Margince handles customer relationship data under a multi-tenant,
agent-governed security model. Reports about weaknesses in that model
are welcome and taken seriously.

## Reporting a vulnerability

Report vulnerabilities **privately** through GitHub Security Advisories:
**[open a draft advisory](https://github.com/gradionhq/margince-poc-v1/security/advisories/new)**
(or: the repository's "Security" tab → "Report a vulnerability"). If you
cannot use GitHub, mail **security@gradion.com** instead. Do not open a
public issue or pull request for a security finding — a public report
before a fix ships puts every deployment at risk.

What to include: the affected endpoint/tool/component, a minimal
reproduction (requests, payloads, or a failing test), and the impact you
believe it has (cross-tenant read, privilege escalation, agent
governance bypass, …).

What to expect: an acknowledgement within **3 business days** and an
initial assessment — in scope or not, and our read on severity — within
**10 business days**. After that you are kept informed through the
advisory thread until the fix ships. We credit you in the advisory and
the changelog unless you prefer otherwise. This is a pre-release proof of
concept maintained by a small team, so we do not commit to a fix
deadline; we do commit to telling you where the report stands.

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
compromised host or database, and denial-of-service against a dev
deployment (`MARGINCE_ENV=dev` deliberately relaxes trust switches).

## Supported versions

This is a pre-release proof of concept: **only the `main` branch is
supported**. There are no release branches yet and no backports; fixes
land on `main`.

## No bounty

There is currently no bug bounty program and no promise of monetary
reward. Credit in the advisory and the changelog is what we offer, and we
will not quietly fix a report without it.

## Safe harbour

We will not pursue or support legal action against anyone who reports in
good faith under this policy: research only against your own deployment,
no access to data that is not yours, no denial of service against a
deployment you do not run, and no disclosure before a fix ships or we
agree on a date. Report promptly once you find something, and give us a
reasonable chance to fix it.
