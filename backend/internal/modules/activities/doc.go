// Package activities owns the activity timeline — logging (with
// source-system idempotency), reading and listing activities and their
// polymorphic links to person/organization/deal records — as store +
// contract mapping + transport handlers + the activities slice of the
// datasource provider, flat per ADR-0054 §3.
//
// Tables owned: activity, activity_link.
//
// Activities have no owner_id; their visibility walks the linked
// records' row scope via platform/auth.ActivityScopeClause — the scope
// rule lives in the platform (one spelling, ADR-0054 §8) because
// people's promotion-evidence check enforces the same clause. Imports
// shared + platform + the generated contract only; never a sibling
// module. Every write rides storekit's audit+outbox shape and every
// entry point is gated by platform/auth.
package activities
