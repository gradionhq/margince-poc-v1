# 11 — Double-opt-in token has no issuance/delivery surface in the contract

**Spec area:** contract/crm.yaml (`RecordConsentRequest.double_opt_in_token`) +
data-model.md §3.4

## The gap

data-model §3.4 makes a DOI purpose's grant effective only after "a confirmed
DOI event (`RecordConsentRequest.double_opt_in_token`)" — but no operation in
crm.yaml *mints* that token or delivers it to the data subject. The contract
defines the redemption half of the round-trip and is silent on the issuance
half.

Taken literally, the only implementable readings are:

1. accept any non-empty string as the token — which lets a caller fabricate
   the confirmation and voids the Art 7(1) demonstrability claim, or
2. issue the token server-side with no contract surface — which is what the
   build now does: `consent.Store.IssueDoubleOptIn` mints a single-use,
   72h-expiring token (sha256 stored, plaintext returned once;
   `consent_doi_token`, migration 0034) and `recordConsent` consumes it.

## Ask

Define the issuance path: either a mint endpoint (e.g.
`POST /people/{id}/consent/double-opt-in` → token issued + confirmation mail
queued through the outbound surface) or an explicit statement that issuance is
a capture-surface concern (booking/forms) with the delivery channel named.
Also state the intended token TTL and single-use semantics so implementations
agree.

## Status

open
