# ADR-0056 — System mail, account recovery, and the notifications still owed

**Status:** Active — the mail channel and account recovery are built.
The notifications engine is **not built and not finally specified**; see below.
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

## Notifications — owed, and not finally specified

The product needs a way to tell a person something happened without mailing them
every time: a task falling due, a mention, an approval waiting on them, a report
they asked for. This decision claims that ground so nothing else quietly grows a
second answer to it.

**What counts as a notification, decided 2026-08-19.** Something happened that
changes what a person believes about their data. A resolved potential duplicate
qualifies: two records they thought were separate are now one. An agent
enriching a field does not: it filled in something they already expected to be
filled. The test is not "did something happen" but "would this person answer a
question differently now". Applying it is what keeps the volume sane, so an
agent working through two hundred records does not produce two hundred lines.

The shape, deliberately simplified and **subject to change**:

- A notification names its recipient, its kind, what it is about, and whether it
  has been read. One state, not two — the earlier draft distinguished "seen"
  from "read" and that distinction was dropped, because nothing can measure the
  difference and a state nobody can verify is not worth a column.
- A person can say which kinds reach them and by which route — in the product,
  by mail digest, or not at all.
- Something reads the event stream and fans events out to the people who should
  see them. It sends mail through the channel above rather than opening a second
  one.

**The daily and weekly briefing are owed too, and are not built.** The briefing
machinery exists — `backend/internal/compose/briefs` ranks and scores what
matters to a person, and it is read on demand. What does not exist is a cadence:
nothing arrives daily or weekly, and there is no place to choose. The digest
route above is that same delivery, so the two are one design pass rather than
two. Who sets the frequency — the operator for everyone, each person for
themselves, or an operator default a person can override — is open, and the
third is the usual answer.

**None of this is built.** There is no notification table, no module, and no
route in the contract. Anything here that reads as settled is not: the entity
shape, the preference model, the digest cadence and the fan-out rule all need a
proper design pass before code. Treat this section as the placeholder that keeps
the question owned, not as a specification to build from — and when that pass
happens, replace this section in the same change.

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
language 2026-08-19. The source bundled three parts into this decision: the
mail channel, account recovery, and a notifications engine with a `notification`
entity, per-user delivery preferences, a `/notifications` endpoint and an in-app
notification centre. The first two are built. The third was never started, and
this record keeps it as an open obligation in simplified form rather than
dropping it — the need is real and still wanted, so removing it would have lost
the fact that it is owed. A later decision split the
reset flow's two halves so that redeeming a token works even where requesting
one cannot, and added the admin-issued link for installations with no mail
channel.
