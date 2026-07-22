// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot

import "strings"

// The five v3 engagement classes share the single canonical "activity" type
// (OVA-MAP-1), but a HubSpot object id is unique only WITHIN an object type —
// a call and a meeting can carry the same numeric id. So an overlay
// activity's mirror external_id is namespaced by its source class
// (OVA-MAP-7): "<class>:<id>" (e.g. "calls:123"), which keeps two classes
// from colliding on the mirror's (workspace, object_class, external_id) key
// and lets a single-record refresh recover which class to re-fetch. Every
// HubSpot API call still needs the RAW object id, so the adapter strips the
// namespace on the way out and re-applies it on the way in.
var engagementClasses = map[string]bool{
	objectClassCalls:    true,
	objectClassMeetings: true,
	objectClassEmails:   true,
	objectClassNotes:    true,
	objectClassTasks:    true,
}

// isEngagementClass reports whether objectClass is one of the five that map
// onto the canonical activity type and therefore carry a namespaced id.
func isEngagementClass(objectClass string) bool { return engagementClasses[objectClass] }

// mirrorActivityExternalID namespaces an incumbent id for the mirror when
// objectClass shares the canonical activity type (OVA-MAP-7):
// ("calls", "123") → "calls:123". Any other class keeps its bare incumbent
// id, so contacts/companies/deals/leads are unchanged.
func mirrorActivityExternalID(objectClass, incumbentID string) string {
	if isEngagementClass(objectClass) {
		return objectClass + ":" + incumbentID
	}
	return incumbentID
}

// incumbentActivityID reverses mirrorActivityExternalID for a HubSpot API
// call that needs the raw object id: ("calls", "calls:123") → "123". A bare
// id (a non-engagement class, or an id that was never namespaced) passes
// through untouched.
func incumbentActivityID(objectClass, mirrorID string) string {
	return strings.TrimPrefix(mirrorID, objectClass+":")
}
