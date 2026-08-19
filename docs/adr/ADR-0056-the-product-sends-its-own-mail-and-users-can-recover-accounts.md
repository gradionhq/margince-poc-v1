# ADR-0056 — The product sends its own mail, and a user can recover an account

**Status:** Active — the mail channel and account recovery are built.
**Decided:** 2026-07-06

## The decision

Mail the product sends on its own behalf — a password reset, an invite — goes
through one outbound channel that the operator configures. That channel is
separate from sending mail as a user through their own mailbox, which is
unchanged. A user who forgot their password requests a reset, receives a
single-use time-boxed token by mail, and sets a new password with it. An admin
can also mint a set-password link and hand it over directly, which is the path
for an installation that has no mail channel at all. Gradion runs no mail
infrastructure; the operator owns the relay and its credential.

## Why

Without a mail channel there is no way to invite a member, and without recovery
a non-SSO user who forgets a password is locked out forever with no self-service
path. Both flows need the same thing — a way to send one message — so they are
one subsystem rather than two. The operator hosting the relay keeps the product
installable in places that will not route mail through a third party.

## What it binds in this repository

- `backend/internal/platform/mailer/mailer.go` is the transport: a `Mailer`
  interface with a single `Send(ctx, to, subject, textBody)` method and an
  `SMTP` implementation. TLS is required — a relay offering no STARTTLS gets the
  send refused rather than a cleartext downgrade — with a loopback relay as the
  one exception. Recipient and subject are rejected if they contain a newline,
  which closes header injection.
- `backend/internal/platform/deployconfig/deployconfig.go` holds the operator's
  configuration under `email.smtp`. The password resolves either from a
  configured secret or from `email.smtp.password_file`, and enabling email
  without a host fails validation at startup.
- `backend/internal/modules/identity/reset.go` carries both halves.
  `RequestPasswordReset` always answers 202 so an attacker cannot learn which
  addresses exist, and an invalid, used or expired token yields one neutral
  refusal. `CreatePasswordReset` and `RedeemPasswordReset` are the service
  methods; `OperatorResetPassword` is the admin path.
- `backend/internal/modules/identity/userpasswordlink.go` mints the admin-issued
  set-password link. It uses the same token table, the same `password_reset`
  purpose and the same seven-day lifetime as an invite, and only the delivery
  differs. Minting one for a member who already has a password is
  account-takeover-capable by design, which is why its audit row is the control
  rather than bookkeeping.
- `backend/migrations/core/0081_auth_token.up.sql` creates the token table.
- `/auth/forgot-password` and its sibling reset operations are in
  `backend/api/crm.yaml`. `GET /auth/capabilities` reports `password_reset`,
  which is true only when the outbound channel is configured and healthy, so the
  login screen never shows a link the installation cannot honor. Requesting a
  reset without a mailer answers 501.

## History

Adopted from the retired specification, decided 2026-07-06. Rewritten in plain
language 2026-08-19.

The source decision paired this mail channel with a notifications engine — a
`notification` entity, per-user delivery preferences, a `/notifications`
endpoint and an in-app notification centre. The mail channel and account
recovery shipped; the engine was never started and its design is not settled.
It was removed from this record on 2026-08-19 rather than kept as a sketch: a
decision record describes a decision, and an unbuilt engine with open design
questions is a plan. The work is tracked as issue #1820, which carries the
rulings that narrow it.
