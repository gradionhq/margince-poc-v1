// English catalog. Keys are the contract; de.ts must mirror them exactly
// (compile-time via satisfies, runtime via i18n.test.ts). Placeholders use
// {name} and are filled by t(key, params).
export const en = {
  "theme.toDark": "Dark theme",
  "theme.toLight": "Light theme",
  // The three appearance choices, as the options of a chooser — the account menu
  // offers all of them at once, so each is named by what it IS rather than by
  // what picking it does. The two labels above stay the names of the ICON-ONLY
  // control on sign-in and onboarding, where one button stands for the theme it
  // switches to. "System" names the machine whose preference it follows, not a
  // third appearance: it resolves to one of the other two and keeps resolving.
  "theme.light": "Light",
  "theme.dark": "Dark",
  "theme.system": "System",

  "trust.accept": "Accept",
  "trust.edit": "Edit",
  "trust.dismiss": "Dismiss",
  "trust.save": "Save",
  "trust.typedByYou": "typed by you",
  "trust.typedByHuman": "typed by a person",
  "trust.typedByPrefix": "typed by",
  "trust.sourceUnknown": "source not recorded",
  "trust.agentTag": "Automated by {agent}",
  "trust.connectorTag": "via {connector}",
  "trust.dismissed": "Suggestion dismissed.",
  "trust.stagedProposal": "staged proposal",
  "trust.resolvedValue": "resolved value",
  "trust.editValue": "Edit {description}",
  "trust.evidenceFrom": "Evidence from {source}",
  "trust.evidenceLine": "line {lines}",
  "trust.evidenceLines": "lines {lines}",

  "history.created": "— created —",
  "history.oldValue": "Previous value",
  "history.newValue": "New value",
  "history.cleared": "— cleared —",
  "history.passport": "Agent passport",
  "history.empty": "No changes recorded",
  "history.fieldEmpty":
    "Set on create and never changed — the audit log records no edits. An empty history is honest, not a gap.",
  "history.filterEmpty": "No changes match this filter.",
  "history.clearFilter": "Clear filters",
  "history.allFields": "All fields",
  "history.actorAll": "All",
  "history.actorHuman": "Human",
  "history.actorAgent": "Agent",
  "history.tabChanges": "Changes",
  "history.tabFields": "Field history",

  "confidence.high": "high",
  "confidence.med": "medium",
  "confidence.low": "low",

  "autonomy.auto": "auto-execute",
  "autonomy.confirm": "confirm-first",

  "nav.home": "Home",
  "nav.contacts": "Contacts",
  "nav.companies": "Companies",
  "nav.leads": "Leads",
  "nav.deals": "Pipeline",
  "nav.tasks": "Tasks",
  "nav.inbox": "Approvals",
  "nav.reports": "Reports",
  "nav.ai": "Ask Margince",
  "nav.settings": "Settings",
  "nav.automations": "Automations",
  "nav.group.records": "Records",
  "nav.group.work": "Work",
  "nav.group.intelligence": "Intelligence",
  "nav.dedupe": "Duplicates",
  "nav.offers": "Offer",
  "nav.share": "Sharing",
  "nav.search": "Search results",

  "shell.railAria": "Primary navigation",
  "shell.skipToContent": "Skip to content",
  "shell.logoAria": "Margince",
  "shell.searchEverything": "Search everything…",
  "shell.breadcrumbAria": "Breadcrumb",
  "shell.approvals": "Approvals",
  "shell.license.none": "No license",
  "shell.license.refused": "License refused",
  "shell.approvalsWaiting": "Approvals — {count} waiting",
  "shell.signOutAria": "Sign out",
  "shell.collapse": "Collapse sidebar",
  "shell.expand": "Expand sidebar",
  "shell.accountAria": "Account",
  "shell.theme": "Theme",
  "shell.more": "More",
  "shell.unknownPage": "Not found",
  "shell.closeMenu": "Close",
  // The sidebar's second level. The control READS one word at every depth; its
  // accessible name says where it leads, and the level of destinations needs a
  // name of its own to be led back to.
  "shell.navBack": "Back",
  "shell.navBackTo": "Back to {name}",
  "shell.navTop": "Destinations",
  // At phone width a section's entries are reached from the page head. The
  // control READS the entry it is on; the name says what pressing it does and
  // keeps that word inside itself (WCAG 2.5.3).
  "shell.sectionSwitch": "{name} — change section",
  "attention.selected": "{n} selected",
  "locale.name.en": "English",
  "locale.name.de": "Deutsch",
  "locale.name.vi": "Tiếng Việt",
  "locale.switchLabel": "Language",

  "screen.pending":
    "Not built yet — this surface arrives with its build ticket.",

  // The composed extension tier (ADR-0069): #/ext/<unit>. The registry is
  // generated per installation, so these two strings are the only part of a
  // unit surface the core catalogs own.
  "ext.notFound":
    "No extension named “{name}” is enabled on this installation.",
  "ext.operations": "Published operations",

  // The reference extension's own screen (#/ext/notes) carries no keys here:
  // a unit that ships a screen ships its copy with it, under
  // extensions/<unit>/frontend/i18n/, namespaced `ext<Unit>.` and merged into
  // the catalogue by gen-composition (see i18n/index.tsx).

  "search.placeholder": "Search people, companies, deals, activities, leads…",
  "search.prompt": "Type what you are looking for.",
  "search.empty": "No matches for “{q}”.",
  "search.group.person": "People",
  "search.group.organization": "Organizations",
  "search.group.deal": "Deals",
  "search.group.activity": "Activities",
  "search.group.lead": "Leads",
  "search.tier.mirrored": "from a connected system",
  "search.tier.unverified": "unverified",

  "context.title": "Related evidence",
  "context.empty": "Nothing related yet.",

  "palette.aria": "Command palette",
  "palette.placeholder": "Jump to, or ask anything…",
  "palette.empty": "No matches.",
  "palette.askAi": "Ask AI: \u201c{query}\u201d",
  "palette.typeScreen": "Screen",
  "palette.typeAction": "Action",
  "palette.typeRecord": "Record",
  "palette.seeAll": "See all results for “{query}”",
  "action.newDeal": "New deal",
  "action.readCompany": "Read a company",
  "action.booking": "Booking page",

  "common.close": "Close",

  "explain.open": "Explain this number",
  "explain.title": "How this number is built",
  "explain.rate": "rate {rate} on {date}",

  "brief.nothingSent": "Nothing sent yet",
  "board.count": "{count} deals",
  "board.weighted": "weighted {value}",
  "board.mixedCurrencies": "several currencies — no single total",
  "deal.stalled": "stalled",
  "deal.singleThreaded": "single-threaded",
  "deal.staged": "staged",
  "deal.archived": "archived",
  "record.notShown": "Not shown",
  "record.timelineLoading": "Loading this record’s history…",
  "record.timeline": "Timeline",
  "record.edit": "Edit",
  "record.save": "Save",
  "record.archive": "Archive",
  "record.disqualify": "Disqualify…",
  "record.archiveConfirm":
    "Are you sure? This archives the record — there is no undo control.",
  "record.archived": "Archived",
  "record.archivedReadOnly":
    "This company is archived. Restore it to change anything on it.",
  "record.share": "Share",
  "record.moreActions": "More actions",
  "record.fullHistory": "Full history",

  "share.title": "Share this record",
  "share.ceiling.pre": "A grant changes who can see ",
  "share.ceiling.recordEmphasis": "exactly this one record",
  "share.ceiling.mid":
    " — nothing else about a person's scope moves. A share is capped at your own access, ",
  "share.ceiling.noWider": "no wider",
  "share.ceiling.post": ".",
  "share.unknownRecord": "This isn't a record that can be shared.",
  "share.grantAccess": "Grant access",
  "share.subject": "Person or team",
  "share.holdsRead": "Has read",
  "share.holdsWrite": "Has write",
  "share.kindPerson": "Person",
  "share.kindTeam": "Team",
  "share.access": "Access level",
  "share.access.read": "Read",
  "share.access.write": "Write",
  "share.access.readNote":
    "Can open and read this record — cannot edit or send.",
  "share.access.writeNote":
    "Can open, edit, and add to this record — not change ownership or sharing.",
  "share.expiry": "Expiry",
  "share.expiry.none": "No expiry (until revoked)",
  "share.expiry.day": "Expires in 24 hours",
  "share.expiry.week": "Expires in 7 days",
  "share.expiry.month": "Expires in 30 days",
  "share.expiryConsequence.one":
    "Access auto-revokes in {days} day. You can revoke it sooner at any time.",
  "share.expiryConsequence.other":
    "Access auto-revokes in {days} days. You can revoke it sooner at any time.",
  "share.expiryConsequenceNone":
    "Access lasts until you revoke it — it will not end on its own.",
  "share.reason": "Reason",
  "share.grant": "Grant access",
  "share.update": "Update access",
  "share.unchanged":
    "Nothing changed. {name} already had {access} access to this record.",
  "share.downgradeTitle": "Reduce access?",
  "share.downgradeBody":
    "{name} has {from} access to this record. Continuing leaves them with {to} access only. Either direction is recorded in the audit trail.",
  "share.downgradeConfirm": "Reduce to {to}",
  "share.seatCeiling":
    "This seat is read-only, so it cannot hold write access to a record. Raise the seat first, or grant read.",
  "share.whoHasAccess": "Who has access",
  "share.grantedBy": "granted by",
  "share.revoke": "Revoke",
  "share.revokeConfirm":
    "Revoke this grant? The subject loses this record's access at the next request — there is no undo control.",
  "share.approvalRequired":
    "This share needs approval before it takes effect — it's been queued to the approval inbox, not applied yet.",
  "share.teamMembers.one": "Team · {count} member",
  "share.teamMembers.other": "Team · {count} members",
  "share.rosterLoading": "Loading people and teams…",
  "share.rosterErrorUsers":
    "Couldn't load the people list — teams are shown below.",
  "share.rosterErrorTeams":
    "Couldn't load the teams list — people are shown below.",
  "share.rosterErrorBoth": "Couldn't load people or teams.",
  "share.rosterEmpty": "No shareable people or teams found.",

  "edit.versionSkew":
    "This record changed since you opened it — reload and try again.",

  "merge.person": "Merge contact",
  "merge.org": "Merge company",
  "merge.searchPlaceholder": "Search…",
  "merge.pickTarget": "Select the surviving record",
  "merge.confirm": "Merge {source} into {target}? {source} will be archived.",
  "merge.submit": "Merge",

  "tab.overview": "Overview",
  "tab.relationships": "People & companies",
  "tab.partner": "Partner",
  "tab.rollup": "Roll-up",
  "tab.history": "History",

  "rollup.weightedPipeline": "Weighted pipeline",
  "rollup.closedWon": "Closed-won (current quarter)",
  "rollup.activity30d": "Activity (30d)",
  "rollup.accounts": "Aggregated accounts",
  "rollup.excluded": "{count} account(s) not visible to you were excluded",
  "rollup.fxUnavailable":
    "A currency conversion rate is missing — the roll-up cannot be computed.",
  "rollup.computedAt": "Computed at {when}",

  "nav.partners": "Partners",
  "deal.partnerSourced": "via",
  "deal.partnerInfluenced": "helped by",
  "deal.partnerAttribution": "What the partner did",
  "deal.attributionUnset": "Not specified — treated as brought us the deal",
  "deal.attributionSourced": "Brought us this deal (earns commission)",
  "deal.attributionInfluenced":
    "Helped on a deal we already had (no commission)",
  "partnerDeals.panelTitle": "Deals they brought",
  "partnerDeals.panelSub":
    "Deals at other companies that came through this partner",
  "partnerDeals.none": "No deals brought in yet",
  "partnerDeals.column.deal": "Deal",
  "partnerDeals.column.customer": "Customer",
  "partnerDeals.column.attribution": "Their part",
  "partnerDeals.column.amount": "Deal value",
  "partnerDeals.column.status": "Status",
  "commission.panelTitle": "Commission",
  "commission.panelSub": "What this partner has earned on deals they brought",
  "commission.none": "Nothing earned yet",
  "commission.column.deal": "Deal",
  "commission.column.amount": "Earned",
  "commission.column.rate": "Rate",
  "commission.column.basis": "Deal value",
  "commission.column.status": "Status",
  "commission.status.accrued": "Accrued",
  "commission.status.approved": "Approved",
  "commission.status.paid": "Paid",
  "commission.status.void": "Reversed",
  "partner.setup": "Make this a partner",
  "partner.edit": "Edit partner",
  "partner.none": "Not a partner yet",
  "partner.organization": "Organization",
  "partner.role": "Partner role",
  "partner.roleAll": "All roles",
  "partner.certStatus": "Certification status",
  "partner.certStatusAll": "All statuses",
  "partner.marginTier": "Margin tier",
  "partner.stage": "Relationship stage",
  "partner.nextStep": "Next step",
  "partner.nextStepDue": "Next step due",
  "partner.servedSegments": "Served segments",
  "partner.servedSegmentsHint": "comma-separated",
  "partner.role.hosting": "Hosting",
  "partner.role.consulting": "Consulting",
  "partner.role.strategic": "Strategic",
  "partner.cert.applied": "Applied",
  "partner.cert.certified": "Certified",
  "partner.cert.suspended": "Suspended",
  "partner.marginTier.tier1": "Intro (15%)",
  "partner.marginTier.tier2": "Active Collab (20%)",
  "partner.marginTier.tier3": "Partner closed (25%)",
  "partner.stage.research": "Research",
  "partner.stage.identified": "Identified",
  "partner.stage.contacted": "Contacted",
  "partner.stage.inConversation": "In conversation",
  "partner.stage.fitConfirmed": "Fit confirmed",
  "partner.stage.agreementPending": "Agreement pending",
  "partner.stage.active": "Active",
  "partner.stage.activeReferring": "Active — referring",
  "partner.stage.dormant": "Dormant",
  "partner.stage.noFit": "No fit",

  "rel.add": "Add relationship",
  "rel.kind": "Kind",
  "rel.role": "Role",
  "rel.startedAt": "Started",
  "rel.endedAt": "Ended",
  "rel.current": "current",
  "rel.endedOn": "until {when}",
  "rel.remove": "Remove",
  "rel.removeConfirm":
    "Are you sure? This removes the relationship — there is no undo control.",
  "rel.empty": "No relationships yet",
  "rel.counterparty": "Linked to",
  "rel.dates": "Dates",
  "rel.pickCounterparty": "Select the other side",
  "rel.addConfirm": "Add a {kind} link to {target}.",
  "rel.kind.employment": "Employment",
  "rel.kind.dealStakeholder": "Deal stakeholder",
  "rel.kind.projectStakeholder": "Project stakeholder",
  "rel.kind.partnerOf": "Partner of",
  "rel.kind.referredBy": "Referred by",
  "rel.kind.coSellWith": "Co-sell with",

  "common.error": "Couldn't load this view.",
  // What a failure that carries no server problem is allowed to say. A rejected
  // fetch and a bug in our own code both report in wording nobody authored for
  // a reader, so the screen states the fact it can stand behind and stops.
  "common.errorNoCause": "The request failed. No cause reported.",
  "common.retry": "Retry",
  "common.empty": "Nothing here yet.",
  "common.saving": "Saving…",
  "common.loading": "Loading…",
  // A reference whose name READ failed, which is not the same as a record that
  // has no name: the id stays reachable through the title, and the reference
  // never becomes a link, because a name that did not load cannot be trusted as
  // a destination.
  "ref.nameLoadFailed": "Name didn't load",

  // The app-level boundary's fallback. It says what happened and what to do
  // next, and nothing about the error itself: a render throw carries our own
  // internals, which the reader can neither read nor act on. Two sentences,
  // no dash (VOICE-RULE-5).
  "app.errorTitle": "This view stopped working.",
  "app.errorBody": "Try it again. If it keeps failing, reload the page.",
  "app.errorRetry": "Try again",

  // The card-level render boundary (design-system/cardboundary.tsx). It says
  // less than the app-level one because it has taken less: the page and its
  // navigation are still there, and only this card is gone.
  "card.errorTitle": "This card stopped working.",
  "card.errorRetry": "Try again",

  // The nine-state honesty vocabulary (design-system/surfacestate.tsx). These
  // words belong to the STATE and to no particular surface, which is why they
  // are keyed `state.*` rather than under any one screen — the same sentence
  // has to read correctly under a deal list and under a retention card. What
  // there is none OF stays the caller's word, passed as `emptyLabel`.
  "state.withheld": "Hidden — your role cannot read this",
  "state.unavailable":
    "Could not be loaded — this may not be the whole picture",
  "state.unsupported":
    "Not available in this mode — the connected system does not hold it",
  "state.failed": "This section did not load.",
  "state.loading": "Loading this section…",
  "state.retry": "Try again",
  "state.stale": "Last known values — not refreshed since",
  "state.staleAsOf": "Last known values, as of {when}",
  "state.partial": "Showing part of the list",
  "state.partialCount": "{count} more not shown",

  "list.search": "Search",
  "list.showArchived": "Show archived",
  "list.loadMore": "Load more",
  "list.sortNewest": "Newest",
  "list.viewAll": "All",
  "list.viewAZ": "A–Z",
  "list.viewHot": "Hot",
  "list.overlayReadOnly":
    "Sorting and filters read through HubSpot — open it there",

  // The list surface (design-system/listtable.tsx). The count says "loaded"
  // rather than a total on purpose: paging is a keyset cursor, so the number
  // of rows in hand is the only figure the client can state honestly.
  "table.range": "{first}–{last} of {count} {unit}",
  "table.pagination": "Pages",
  "table.page": "Page {number}",
  "table.prev": "‹ Prev",
  "table.next": "Next ›",
  "table.rowsPerPage": "Rows per page",
  "table.perPage": "{count} per page",
  "table.sortedBy": "sorted by {column}",
  "table.columns": "Columns",
  "table.shownColumns": "Shown columns",
  "table.compact": "Compact",
  "table.sortBy": "Sort by {column}",
  "table.noMatches": "No {unit} match these filters.",
  "table.clearFilters": "Clear filters",
  "table.none": "No {unit} yet.",
  "table.actions": "Actions",
  "table.rangeLoaded": "{first}–{last} of {count} {unit} loaded so far",
  "unit.contacts": "contacts",
  "unit.companies": "companies",
  "unit.deals": "deals",
  "unit.leads": "leads",
  "unit.partners": "partners",
  "unit.products": "products",
  "unit.offerTemplates": "offer templates",
  "table.filter": "Filter",
  "table.filterSearch": "Search attributes",
  "table.addFilter": "Add a filter",
  "table.filterIs": "is",
  "table.filterCondition": "Condition",
  "table.filterMore": "More actions for the {filter} filter",
  "table.deleteFilter": "Delete filter",
  "table.filterValueSearch": "Search {filter} values",
  "table.filterTypeToSearch": "Type to search",
  "table.filterSearching": "Searching…",
  "table.filterSearchFailed": "The search failed. Try again.",
  "table.filterNoMatches": "No matches.",
  "overlay.unavailable":
    "Not available while reading from HubSpot — open it in HubSpot",
  "overlay.chipLabel": "Reading from HubSpot",
  "overlay.chipAria":
    "This installation reads records from a HubSpot mirror instead of native tables. Open Settings → Integrations to manage the connection.",
  "overlay.refused":
    "Not available while reading from HubSpot — the mirror can't serve this write.",
  "overlay.filterUnsupported":
    "This filter or sort isn't available while reading from HubSpot — remove it and try again.",
  "overlay.emptyOwnerHint":
    "An empty list here usually means the owner's HubSpot email doesn't match a user in this organization, not an empty HubSpot portal.",
  "overlay.partialWriteBack":
    "Only the fields HubSpot accepts are written back — anything else here, including custom fields and owner, is not applied at all; HubSpot's current value is kept.",

  "overlay.title": "HubSpot mirror",
  "overlay.sub":
    "Connect the organization's incumbent CRM so records read from its mirror instead of native tables.",
  "overlay.loading": "Loading the incumbent connection…",
  "overlay.notConfigured": "Overlay mode isn't configured in this deployment.",
  "overlay.loadFailed": "Couldn't load the incumbent connection.",
  "overlay.empty":
    "No incumbent is connected. Connect HubSpot to read records from its mirror.",
  "overlay.adminOnly":
    "You do not have permission to change the HubSpot connection.",
  "overlay.region": "Region",
  "overlay.regionEu1": "EU",
  "overlay.regionUs": "United States",
  "overlay.token": "Private-app token",
  "overlay.tokenHint": "Sealed into the vault; never shown again.",
  "overlay.connect": "Connect HubSpot",
  "overlay.reconnect": "Reconnect",
  "overlay.connectConfirmTitle": "Connect HubSpot for the whole organization?",
  "overlay.reconnectConfirmTitle":
    "Reconnect HubSpot for the whole organization?",
  "overlay.connectConfirmBody":
    "This switches every seat's reads to HubSpot's mirror immediately, and records become read-only wherever the mirror can't serve a write. This affects the whole installation, not just your own session.",
  "overlay.statusActive": "Connected",
  "overlay.statusRevoked": "Revoked",
  "overlay.statusError": "Sync error",
  "overlay.connectedAt": "Connected {at}",
  "overlay.syncTitle": "Mirror sync",
  "overlay.syncLoadFailed": "Couldn't load sync status.",
  "overlay.syncEmpty": "Nothing has synced yet.",
  "overlay.syncStateFresh": "Fresh",
  "overlay.syncStatePending": "Pending sync",
  "overlay.syncStateStale": "Stale",
  "overlay.backfillDone": "Backfill complete",
  "overlay.backfillPending": "Backfill in progress",
  "overlay.lastSynced": "Last synced {at}",
  "overlay.neverSynced": "Never synced",
  "overlay.budgetTitle": "API budget",
  "overlay.budgetLoadFailed": "Couldn't load the budget window.",
  "overlay.budgetHeadroom": "Headroom: {headroom}",
  "overlay.budgetEmpty":
    "The incumbent reported no budget window for this period.",
  "overlay.budgetSources":
    "Force-fresh {forceFresh} · Poller {poller} · Capture {capture}",
  "overlay.budgetSearch": "Search API: {consumed} / {limit} per second",
  "overlay.bandOk": "Healthy",
  "overlay.bandWarn": "Approaching limit",
  "overlay.bandShed": "Shedding load",
  "overlay.reconcile": "Sync now",
  "overlay.reconcileQueued":
    "Sweep queued — the worker picks it up on its next poll (about every 2 minutes).",
  "overlay.disconnect": "Disconnect",
  "overlay.disconnectTitle": "Disconnect HubSpot?",
  "overlay.disconnectBody":
    "This purges the mirrored data and switches the organization back to native records. The audit trail is kept.",

  "overlay.userMap.title": "Mirror user mapping",
  "overlay.userMap.sub":
    "Who each user in this organization is as a {principal} user. This mapping is the whole of their mirror visibility.",
  "overlay.userMap.cost":
    "A user with no mapping sees no mirrored records at all — their lists come back empty.",
  "overlay.userMap.loading": "Loading the user mapping…",
  "overlay.userMap.loadFailed": "Couldn't load the user mapping.",
  "overlay.userMap.adminOnly":
    "You do not have permission to review who is mapped.",
  "overlay.userMap.notOverlay":
    "This organization reads from native tables, so there is nothing to map.",
  "overlay.userMap.notConfigured":
    "Overlay mode isn't configured in this deployment.",
  "overlay.userMap.empty": "This organization has no users to map.",
  "overlay.userMap.view": "Grouping",
  "overlay.userMap.viewByUser": "By user",
  "overlay.userMap.viewByOwner": "By {principal} user",
  "overlay.userMap.principal.hubspot": "HubSpot",
  "overlay.userMap.principal.generic": "connected CRM",
  "overlay.userMap.you": "You",
  "overlay.userMap.matchEmail": "Matched by email",
  "overlay.userMap.matchManual": "Manual override",
  "overlay.userMap.map": "Map…",
  "overlay.userMap.change": "Change…",
  "overlay.userMap.unmap": "Unmap",
  "overlay.userMap.cancel": "Cancel",
  "overlay.userMap.pickerLabel": "Search {principal} users",
  "overlay.userMap.truncated":
    "The {principal} directory is longer than this list — someone you can't find here may be past the cut-off.",
  "overlay.userMap.directoryFailed":
    "Couldn't read the {principal} directory, so nobody can be picked right now.",
  "overlay.userMap.notMapped": "Not mapped",
  "overlay.userMap.chip.noEmailMatch": "No email match",
  "overlay.userMap.chip.ambiguousEmail": "Ambiguous email",
  "overlay.userMap.chip.blockedByAdmin": "Unmapped by an admin",
  "overlay.userMap.chip.notYetSynced": "Not synced yet",
  "overlay.userMap.chip.directoryUnavailable": "Reason unknown",
  "overlay.userMap.reason.noEmailMatch":
    "No {principal} user has this email address.",
  "overlay.userMap.reason.ambiguousEmail":
    "Two or more {principal} users share this email address, so no automatic match is safe.",
  "overlay.userMap.reason.blockedByAdmin":
    "An admin unmapped this user, and automatic matching will not map them again.",
  "overlay.userMap.reason.notYetSynced":
    "The {principal} directory hasn't listed this user yet.",
  "overlay.userMap.reason.directoryUnavailable":
    "Couldn't read the whole {principal} directory, so no reason can be derived.",
  "overlay.userMap.staleChip": "No longer in the {principal} directory",
  "overlay.userMap.staleNote":
    "This manual mapping grants no visibility. It is reported, never withdrawn automatically — the decision stays yours.",
  "overlay.userMap.unmapTitle": "Unmap this user?",
  "overlay.userMap.unmapSelfTitle": "Unmap yourself?",
  "overlay.userMap.unmapBody":
    "{user} will stop seeing every mirrored record until they are mapped again.",
  "overlay.userMap.unmapSelfBody":
    "You will stop seeing every mirrored record until you are mapped again. This tab stays reachable, so you can undo it here.",
  "overlay.userMap.sharedSeat": "Shared seat — {count} users",
  "overlay.userMap.ownerEmpty": "Nobody is mapped to a {principal} user yet.",
  "overlay.userMap.unmappedCountOne":
    "1 user is not mapped and isn't shown here — switch to By user to fix that.",
  "overlay.userMap.unmappedCount":
    "{count} users are not mapped and aren't shown here — switch to By user to fix that.",
  "overlay.userMap.partialView":
    "This grouping and count cover the users loaded so far. Load more to see the rest.",

  "people.name": "Name",
  "people.email": "Email",
  "list.owner": "Owner",
  "list.unowned": "Unassigned",
  "list.created": "Created",
  "list.lastActivity": "Last activity",
  "list.filterOwnerMe": "My records",
  "list.filterOwnerAll": "Any owner",
  "list.filterOwnerUnassigned": "Unassigned",
  "views.save": "Save view",
  "views.saveConfirm": "Save",
  "views.saveTitle": "Save this view",
  "views.name": "Name",
  "list.viewMine": "Mine",
  "list.viewCustomers": "Customers",
  "list.viewProspects": "Prospects",
  "org.filterLifecycleAll": "Any stage",
  "org.filterRelTypeAll": "Any type",
  "org.filterSizeBandAll": "Any size",
  "person.consent": "Consent",
  "consent.grant": "Grant",
  "consent.withdraw": "Withdraw",
  "consent.doubleOptIn": "Issue double opt-in",
  "consent.doiIssued": "One-time token (shown once):",
  "consent.doiExpires": "Expires",
  "consent.noRecord": "no record",
  "consent.noPurposes": "This organization tracks no consent purposes yet.",
  "consent.defaultDeny":
    "Outbound is default-deny per purpose: a send is blocked unless an active, proven grant exists for that purpose. A grant for one purpose never authorizes another.",
  "consent.proofLog": "Proof log",
  "consent.proofEmpty":
    "No consent decision recorded for this purpose. An empty log is honest, not a gap.",
  "consent.sourceUnknown": "source not recorded",
  "consent.tokenLabel": "Confirmation token",
  "consent.tokenHint":
    "This purpose needs a double opt-in: paste the one-time token to make the grant effective.",
  "consent.actorHuman": "Human",
  "consent.actorAgent": "Agent",
  "consent.actorSystem": "System",
  "consent.actorConnector": "Connector",
  "consent.actorUnknown": "actor not recorded",
  "consent.purposesUnavailable":
    "Couldn't load the consent purpose catalogue, so which purposes need a double opt-in can't be shown right now.",

  "org.name": "Company",
  "org.description": "What they do",
  "org.website": "Website",
  "org.contactCount": "Contacts",
  "org.openDealCount": "Open deals",
  // Offered only where there is no partner programme yet: the tab that holds
  // the form appears once one exists, so this is how the first one is made.
  // Where the account stands with us, and what it is to us — the two
  // questions the retired classification answered with one value.
  "org.lifecycle": "Account lifecycle",
  "org.relationshipTypes": "Relationship to us",
  "org.sizeBand": "Company size",
  "org.lifecycle.unknown": "Not assessed",
  "org.lifecycle.target": "Target",
  "org.lifecycle.prospect": "Prospect",
  "org.lifecycle.opportunity": "Opportunity",
  "org.lifecycle.customer": "Customer",
  "org.lifecycle.former_customer": "Former customer",
  "org.lifecycle.disqualified": "Disqualified",
  "org.relType.customer": "Customer",
  "org.relType.partner": "Partner",
  "org.relType.supplier": "Supplier",
  "org.relType.investor": "Investor",
  "org.relType.portfolio_company": "Portfolio company",
  "org.relType.competitor": "Competitor",
  "org.relType.other": "Other",
  // Why a stored fact contradicts its own field. The fact is still shown
  // with its evidence — a reader can tell, and hiding it would be worse.
  "co.factSuspect.phoneShapedLocation": "Looks like a phone number",
  "co.factSuspect.notAPhone": "Does not look like a phone number",
  "co.factSuspect.notAYear": "Does not look like a year",
  "co.factSuspect.notAnEmail": "Does not look like an email address",
  "co.factSuspect.notASize": "Does not look like a headcount",
  // The three readings the overview leads with, and what performing a
  // suggestion means. "Whose move" is the question the 0-100 score was
  // mistaken for.
  "co.strip.title": "Where this account stands",
  "co.strip.convertedAsOf": "{count} converted, rates from {date}",
  "co.strip.noOpenDeals": "No open deals",
  "co.strip.pipeline": "Open pipeline",
  "co.description.label": "Description",
  "co.description.placeholder": "Add description",
  "co.strip.netInvoiced": "Net invoiced · 12 mo",
  "co.strip.notAssessed": "Not assessed",
  "co.strip.lifetimeOf": "{amount} lifetime",
  // The collapsed slot's label, shown once in place of the strip's money
  // readings when the connection cannot answer any of them. Generic on
  // purpose: it stands for all of them at once.
  "co.strip.finance": "Finance",
  "co.strip.financeUnknown": "—",
  "co.strip.fin.noConnection": "Connect your accounting",
  "co.strip.fin.unmapped": "Not matched to a customer yet",
  "co.strip.fin.syncing": "Syncing…",
  "co.strip.fin.withheld": "You may not see this account's finance",
  "co.strip.fin.staleFigure": "Last synced a while ago — check the date",
  "co.strip.fin.errorFigure": "Last sync failed — this may not be current",
  "co.strip.fin.nothingBilled": "Nothing invoiced yet",
  "co.strip.fin.error": "Could not be read",
  "co.strip.fin.loading": "Loading…",
  "co.strip.unpriced": "No convertible amount on these deals",
  "co.strip.pricedPartly": "{priced} of {total} deals priced",
  "co.strip.expectedClose": "Expected close",
  "co.strip.health": "Relationship",
  "co.strip.healthOneSided": "One-sided",
  "co.strip.healthBalanced": "Balanced",
  "co.strip.replyShare": "{percent}% of the exchange is theirs",
  "co.strip.healthActive": "In conversation",
  "co.strip.healthQuiet": "Gone quiet",
  "co.strip.noInboundEver": "They have never written",
  "co.strip.account": "Stage",
  "co.strip.engagement.never_contacted": "Never contacted",
  "co.strip.engagement.active": "In conversation",
  "co.strip.engagement.waiting_on_them": "Waiting on them",
  "co.strip.engagement.waiting_on_us": "Waiting on us",
  "co.strip.engagement.dormant": "Gone quiet",
  "co.strip.openDeals": "{count} open",
  "co.strip.stalled": "{count} stalled",
  "co.suggest.act.draftReply": "Draft a reply",
  "co.suggest.act.openDeal": "Open the deal",
  "co.suggest.act.addTask": "Add the next step",
  // A conversation shown as one event says what it IS before what it
  // says: the reader is scanning for an event, not a sentence.
  "timeline.group.thread": "{count} messages",
  "timeline.group.threadOne": "1 message",
  "timeline.group.bulk": "sent to {count} people",
  "timeline.group.bulkOne": "sent to 1 person",
  "timeline.group.expand": "Open",
  "timeline.group.collapse": "Close",
  "timeline.group.openThread": "View the whole thread",
  "timeline.group.mayContinue": "may continue earlier",
  "tab.people": "People",
  "tab.deals": "Deals",
  "tab.tasks": "Tasks",
  "tab.timeline": "History",
  "tab.documents": "Documents",
  "tab.profile": "Profile",
  "tab.meetings": "Meetings",
  "tab.research": "Research",
  // The brief under the questions it answers, and what kind of claim each
  // sentence makes — a judgment must not read as a stored fact.
  "co.brief.section.snapshot": "What they are",
  "co.brief.section.fit": "Why it matters to us",
  "co.brief.section.health": "How it stands",
  "co.brief.section.activity": "What happened",
  "co.brief.section.next_step": "What to do",
  "co.brief.nature.fact": "Fact",
  "co.brief.nature.assessment": "Our read",
  "co.brief.nature.recommendation": "Suggested",
  // The rail's own details grid: the account's own fields, at a glance above
  // the collapsible sections.
  "co.details.title": "Details",
  "co.health.title": "How it stands",
  "co.health.empty": "No health reading on this account yet.",
  "co.health.dim.relationship": "Relationship",
  "co.health.dim.commercial": "Commercial",
  "co.health.dim.payment": "Payment",
  "co.health.rating.atRisk": "At risk",
  "co.health.rating.good": "Good",
  "co.health.rating.strong": "Strong",
  "co.health.payment.overdue": "Money is overdue right now.",
  "co.health.payment.late": "Typically pays {days} days after due.",
  "co.health.payment.onTime": "Pays on time.",
  "co.health.sinceInbound": "They last wrote {days} days ago",
  "co.health.replyBalance": "{percent}% of the exchange came from them",
  "co.health.activeContacts.one": "{count} person here has ever interacted",
  "co.health.activeContacts.other": "{count} people here have ever interacted",
  "co.health.openCommitments.one": "{count} open commitment",
  "co.health.openCommitments.other": "{count} open commitments",
  "co.health.singleThreaded": "One contact carries this account",
  "org.partnerSetUp": "Set up partner programme",
  "signal.kind.stalled_deal": "Deal stalled",
  "signal.kind.champion_left": "Champion left",
  "signal.kind.reengagement": "Worth re-engaging",
  "signal.kind.buying_intent": "Buying intent",
  "signal.kind.risk": "Risk",
  "signal.kind.other": "Other",
  "signal.kind.contract_ended": "Contract ending",
  "signal.kind.new_opportunity": "New opportunity",
  "signal.kind.commitment_made": "Something was promised",
  "signal.kind.ghosted_thread": "No answer",
  "co.routeIn.open": "Route in",
  "co.routeIn.title": "Who here talks to {name}",
  "co.routeIn.none": "Nobody here has written to them yet.",
  "co.routeIn.partial":
    "No way in among the connections this page could read — some were withheld or left out.",
  "co.routeIn.mayBeMore":
    "Some connections were withheld or left out, so there may be more.",
  "co.routeIn.band.strong": "in regular contact",
  "co.routeIn.band.some": "some contact",
  "co.routeIn.band.faint": "barely in contact",
  "co.routeIn.band.unknown": "contact on file, no pattern yet",
  "record.profile": "Profile",
  "record.context": "Context",
  "co.pulse.created": "Created {when}",
  // The later of the two directions \u2014 which side wrote last moved to the
  // daily brief's own detail line, so the header states only that the
  // relationship is or is not live.
  "co.pulse.lastExchange": "Last exchange {when}",
  "co.pulse.neverTouched": "Never contacted",
  "co.pulse.owner": "Owner",
  "co.pulse.strongestLead": "Way in",
  "co.pulse.strengthTail.one": "\u2014 the only contact here",
  "co.pulse.strengthTail.other": "\u2014 of {count} contacts here",
  "co.owner.notInRoster": "Current owner (no longer in the user list)",
  "co.pulse.unowned": "Unassigned",
  "co.since.first": "You are opening this account for the first time.",
  "co.partial":
    "Some of this page could not be loaded, so it may not show everything on this account.",
  "evidence.explain": 'Where "{value}" came from',
  "evidence.fullHistory": "Full history",
  "co.section.unavailable":
    "Could not be loaded — this may not be the whole picture",
  "finance.title": "Finance",
  "finance.titleHistorical": "Finance · historical",
  "finance.none": "Nothing recorded.",
  "finance.noConnection":
    "No financial source connected — connect one to see what this customer has been invoiced and whether they pay on time",
  "finance.unmapped":
    "Connected, but this company is not matched to a customer in the accounting system yet",
  "finance.netInvoiced": "Net invoiced · 12 months",
  "finance.overdue": "Overdue",
  "finance.behaviour": "Payment behaviour",
  "finance.behaviourShape": "Days late per settled invoice, oldest first",
  "finance.shareOfOpen": "{percent}% of everything open",
  "finance.overdueShareLabel": "Overdue share of the open balance",
  "finance.legendOverdue": "Overdue {amount}",
  "finance.legendOpen": "Open {amount}",
  "finance.medianAfterDue": "Typically {days} days after due",
  "finance.medianEarly": "Typically {days} days early",
  "finance.col.invoice": "Invoice",
  "finance.col.dates": "Issued → due",
  "finance.paidOn": "paid {when}",
  "finance.paidDayLate": "Paid 1 day late",
  "finance.paidDaysLate": "Paid {days} days late",
  "finance.overdueDay": "{days} day overdue",
  "finance.overdueDays": "{days} days overdue",
  "finance.col.amount": "Amount",
  "finance.col.status": "Status",
  "finance.unnumbered": "No number",
  "finance.moreInvoices": "More invoices in the accounting system",
  "finance.connect": "Connect finance",
  "finance.syncedFrom": "From {provider} · synced {when}",
  "finance.fromNeverSynced": "From {provider} · not yet synced",
  "finance.status.draft": "Draft",
  "finance.status.open": "Open",
  "finance.status.partiallyPaid": "Part paid",
  "finance.status.paid": "Paid",
  "finance.status.overdue": "Overdue",
  "finance.status.disputed": "Disputed",
  "finance.status.credited": "Credited",
  "finance.status.void": "Void",
  "commercial.closes": "closes {when}",
  "contracts.title": "Contracts",
  "contracts.empty": "No agreements on record",
  "contracts.noneActive": "No agreement is active today",
  "contracts.filter.all": "All",
  "contracts.filter.active": "Active",
  "contracts.status.draft": "Draft",
  "contracts.status.active": "Active",
  "contracts.status.expired": "Expired",
  "contracts.status.cancelled": "Cancelled",
  "contracts.status.superseded": "Superseded",
  "contracts.endsOn": "Ends {when}",
  "contracts.renewsOn": "Renews {when}",
  "contracts.endedPendingStatus": "Term ended \u2014 status change pending",
  "contracts.form.title": "Record an agreement",
  "contracts.form.name": "Title",
  "contracts.form.number": "Contract number",
  "contracts.form.value": "Value",
  "contracts.form.basis": "This value is",
  "contracts.basis.total": "the total for the whole term",
  "contracts.basis.annual": "twelve months of an open-ended agreement",
  "contracts.form.startsOn": "Starts",
  "contracts.form.endsOn": "Ends",
  "contracts.form.endsOnHint": "Leave empty for an open-ended agreement.",
  "contracts.form.renewalOn": "Renews",
  "contracts.form.noticeDays": "Notice period (days)",
  "contracts.form.noticeDaysHint":
    "How much notice a cancellation needs. The renewal warning fires before this deadline, not before the renewal date.",
  "contracts.form.signedOn": "Signed",
  "contracts.form.signedOnHint":
    "Only when a human knows it was signed \u2014 never taken from a deal's close date.",
  "contracts.form.save": "Record agreement",
  "contracts.form.errNoName": "An agreement needs a title.",
  "contracts.form.errTermOrder": "A term cannot end before it starts.",
  "contracts.add": "Add a contract",
  "contracts.rowMenu": "Contract actions",
  "contracts.value.perYear": "per year",
  "contracts.value.total": "for the whole term",
  "contracts.files": "Files",
  "contracts.noTerm": "No dates recorded",
  "contracts.openStart": "Open start",
  "contracts.openEnd": "Open-ended",
  "contracts.edit": "Edit",
  "contracts.archive": "Archive",
  "contracts.archive.title": "Archive this contract?",
  "contracts.archive.body":
    "\u201c{title}\u201d leaves the lists and the account totals. The record and its history stay, so what was true stays answerable \u2014 nothing is deleted.",
  "contracts.archive.confirm": "Archive",
  "contracts.form.editTitle": "Edit contract",
  "contracts.form.saveEdit": "Save changes",
  "contracts.form.file": "Signed document",
  "contracts.form.fileHint":
    "Drop the signed PDF here, or click to choose one. It is filed against this contract and appears in the account's documents.",
  "contracts.form.fileEmpty": "Drop a file here or click to choose",
  "contracts.form.fileAdd": "Drop another file here or click to choose",
  "contracts.perYear": "{amount} / year",
  "contracts.state.title": "Under contract · {count} active",
  "contracts.state.none": "No contract on record",
  "contracts.state.renewsOn": "Renews {when}",
  "contracts.state.endsOn": "Cancelled — ends {when}",
  "contracts.state.partial": "{priced} of {total} priced",
  "commercial.lastOffer": "Last offer · {deal}",
  "commercial.offerUnnumbered": "Offer",
  "commercial.validUntil": "valid until {when}",
  "commercial.offer.draft": "Draft",
  "commercial.offer.sent": "Sent",
  "commercial.offer.accepted": "Accepted",
  "commercial.offer.rejected": "Rejected",
  "commercial.offer.expired": "Expired",
  "commercial.offer.superseded": "Superseded",
  "co.coverage.contacts": "{count} contacts",
  "co.coverage.contactsAtLeast": "{count}+ contacts",
  "co.coverage.untried": "{count} never written to",
  "co.coverage.gaps": "{count} role gaps",
  "co.section.restricted": "Hidden \u2014 your role cannot read this",
  "co.next.title": "Next steps",
  "co.next.empty": "No open task on this account.",
  "co.next.overdue": "Overdue",
  "co.next.due": "Due {when}",
  "co.next.undated": "No date",
  "co.people.title": "People",
  "co.people.empty": "No contact linked to this account yet.",
  "co.people.singleThread":
    "One contact only \u2014 the account is single-threaded",
  "co.people.consentGranted": "May contact",
  "co.people.consentWithdrawn": "Withdrawn",
  "co.people.consentUnknown": "No consent on file",
  "co.brief.title": "The account, in short",
  "co.brief.unavailable":
    "The account reading could not be loaded, so this is not the whole picture.",
  "co.brief.empty":
    "There is not enough on this account yet to read anything from it.",
  "co.brief.rewrite": "Write it again",
  "co.brief.rewriting": "Writing…",
  "co.brief.by.model": "Written by Margince",
  "co.brief.by.deterministic": "Assembled from your records",
  "co.brief.generatedAt": "as of {when}",
  "co.growthFit.title": "What they are worth to you",
  "co.growthFit.unavailable":
    "This assessment could not be read. Nothing about the company has changed.",
  "co.growthFit.assembling":
    "Working out what this account is worth — the first assessment reads the record and takes a moment.",
  "co.growthFit.reassess": "Assess it again",
  "co.growthFit.reassessing": "Assessing…",
  "co.growthFit.band.strong": "Strong fit",
  "co.growthFit.dim.industryFit": "Industry fit",
  "co.growthFit.dim.companySize": "Company size",
  "co.growthFit.dim.transformationNeed": "Transformation need",
  "co.growthFit.dim.access": "Access",
  "co.growthFit.band.moderate": "Moderate fit",
  "co.growthFit.band.weak": "Weak fit",
  "co.growthFit.band.unknown": "Not enough to judge",
  "co.growthFit.completeness": "{present} of {expected} inputs recorded",
  "co.growthFit.missing": "Still missing",
  "co.growthFit.capped": "Held back: {reason}.",
  "co.growthFit.nextStep": "Next: {step}.",
  "co.growthFit.positive": "Argues for",
  "co.growthFit.negative": "Argues against",
  "co.growthFit.whitespace": "Room to sell",
  "co.growthFit.objections": "Likely pushback",
  "co.growthFit.angle": "Suggested approach",
  "co.writeEmail": "Write email",
  "co.dossier.title": "What this company is",
  "co.dossier.unavailable":
    "This description could not be read. Nothing about the company has changed.",
  "co.dossier.empty":
    "Nothing has been recorded about this company yet. Read their website, or fill in the profile below.",
  "co.dossier.stale": "Read over a month ago",
  "co.dossier.rewrite": "Write it again",
  "co.dossier.rewriting": "Writing…",
  "co.dossier.section.summary": "In short",
  "co.dossier.section.products_services": "What they sell",
  "co.dossier.section.markets": "Where and to whom",
  "co.dossier.section.buying_center": "Who decides",
  "co.dossier.section.differentiation": "What sets them apart",
  "co.dossier.section.firmographics": "Size, age and registration",
  "co.evidence.unavailable":
    "This receipt could not be read. The record itself is unchanged.",
  "co.evidence.producedBy": "recorded by {who}",
  "co.evidence.retrievedAt": "Read {when}",
  "co.evidence.verifiedAt": "Confirmed by a person {when}",
  "co.evidence.confidence": "The model was {percent}% confident",
  "co.evidence.gaps": "Not recorded: {fields}.",
  "co.evidence.kind.site_read": "Read from their website",
  "co.evidence.kind.connector": "From a connected system",
  "co.evidence.kind.human": "Entered by a person",
  "co.evidence.kind.migration": "Imported",
  "co.evidence.kind.rule": "Derived",
  "co.brief.cite.deal": "deal",
  "co.brief.cite.activity": "activity",
  "co.brief.cite.person": "contact",
  "co.brief.cite.organization": "account",
  "co.brief.cite.fact": "fact",
  "co.brief.cite.profile_field": "profile field",
  // Several sources of one kind that have no screen to open collapse into one
  // counted chip, rather than a run of identical labels.
  "co.brief.cite.deal.many": "{count} deals",
  "co.brief.cite.activity.many": "{count} activities",
  "co.brief.cite.person.many": "{count} contacts",
  "co.brief.cite.organization.many": "{count} accounts",
  "co.brief.cite.fact.many": "{count} facts",
  "co.brief.cite.profile_field.many": "{count} profile fields",
  "approval.kind.advance_deal": "Move a deal forward",
  "approval.kind.close_date_correction": "Correct a close date",
  "approval.kind.deal_follow_up": "Add a follow-up on a deal",
  "approval.kind.promote_lead": "Promote a lead",
  "approval.kind.archive_record": "Archive a record",
  "approval.kind.merge_records": "Merge two records",
  "approval.kind.share_record": "Share a record",
  "approval.kind.update_record": "Update a record",
  "approval.kind.create_record": "Create a record",
  "approval.kind.send_email": "Send an email",
  "approval.kind.held_draft": "Review a drafted email",
  "approval.kind.book_meeting": "Book a meeting",
  "approval.kind.send_offer": "Send an offer",
  "approval.kind.quota_release": "Let an agent continue",
  "approval.kind.coldstart": "Fill in a new account",
  "approval.kind.enrich": "Enrich from the web",
  "approval.kind.deepread": "Read the company site",
  "approval.kind.linkedin_match": "LinkedIn match",
  "approval.kind.site_lead": "Add a person found on the site",
  "approval.kind.capture_counterparty": "Add someone from your mail",
  "approval.kind.org_name_promotion": "Rename an account",
  "approval.kind.lifecycle_change": "Account stage",
  "approval.kind.transcript_proposal": "Add a next step from a transcript",
  "approval.kind.fx_rate_proposal": "Refresh exchange rates",
  "approval.kind.ai_model_rate_proposal": "Refresh model prices",
  "co.assistant.title": "Ask about this account",
  "co.assistant.aiTag": "AI-assisted",
  "co.decisions.open": "Review {count} waiting",
  "co.decisions.title": "Decisions waiting",
  "co.decisions.group": "{count} × {kind}",
  "co.decisions.empty": "Nothing is waiting on a decision here.",
  "co.ask.title": "Ask Margince",
  "co.ask.q.whats_open": "What's open here?",
  "co.ask.q.meeting_prep": "Prep me for a meeting",
  "co.ask.q.whats_changed": "What's changed recently?",
  "co.ask.nothing": "Nothing here that you can see would answer that.",
  "co.ask.failed": "That question could not be answered — try it again.",
  "co.suggest.title": "Worth doing next",
  "co.suggest.kind.no_reply": "No reply",
  "co.suggest.kind.stalled_deal": "Stalled deal",
  "co.suggest.kind.no_next_step": "Nothing scheduled",
  "co.suggest.kind.lifecycle_conflict": "Record disagrees",
  "co.suggest.more": "{count} more not shown here.",
  "co.suggest.dismiss": "Not now",
  "co.suggest.dismissFailed":
    "That could not be dismissed — it is still showing for you",
  "co.suggest.viewTasks": "View tasks",
  "co.suggest.commitment.overdueCount": "{count} overdue",
  "co.suggest.commitment.openCount": "{count} open",
  "co.suggest.commitment.overdueAtLeast": "{count}+ overdue",
  "co.suggest.commitment.openAtLeast": "{count}+ open",
  "co.deals.title": "Deals",
  "co.deals.empty": "No open deal on this account.",
  "co.deals.wonLifetime": "Won to date",
  "co.deals.lostCount": "{count} lost",
  "co.deals.noStage": "No stage",
  "co.commercial.title": "Commercial",
  "co.commercial.lostFigure": "Lost deals",
  "co.commercial.allDeals": "All deals",
  "co.commercial.truncated":
    "This account has more open deals than fit here. Open All deals to see the rest.",
  "co.connections.title": "Connections",
  "co.connections.empty": "Nothing linked to this account yet.",
  "co.connections.ourSide": "Your side",
  "co.connections.theirSide": "At this account",
  "co.connections.expand": "See it larger",
  "co.connections.collapse": "Close",
  "co.connections.introPath": "Route in",
  "co.connections.intro.askForIntro": "Ask for an introduction",
  "co.connections.intro.writeDirectly": "Write to them directly",
  "co.connections.intro.via": "Through",
  "co.connections.more": "{count} more not shown here.",
  "co.connections.withheld": "Hidden from you: {groups}",
  "co.connections.rel.employment": "works here",
  "co.connections.rel.has_deal": "open deal",
  "co.connections.rel.deal_stakeholder": "stakeholder on a deal",
  "co.connections.rel.parent": "parent company",
  "co.connections.rel.child": "subsidiary",
  "co.connections.rel.partner_of.counterparty": "partner on this account",
  "co.connections.rel.partner_of.owner": "this account is their partner",
  "co.connections.rel.referred_by.counterparty": "referred this account",
  "co.connections.rel.referred_by.owner": "referred by this account",
  "co.connections.rel.co_sell_with": "co-selling",
  "co.connections.rel.owns": "owns this account",
  "co.connections.rel.in_contact_with": "in contact",
  "co.connections.noSignal": "no signal yet",
  "linkedinImport.title": "LinkedIn connections",
  "linkedinImport.sub":
    "Import your own export to see who your team already knows",
  "linkedinImport.explainer":
    "LinkedIn gives you a Connections.csv under Settings → Data privacy → Get a copy of your data. Uploading it here shows who on your team already knows someone at an account. The connections do NOT become contacts: they never appear in search, lists or contact pages, and nobody can write to or email them.",
  "linkedinImport.profileLabel": "Your LinkedIn profile URL",
  "linkedinImport.profilePlaceholder": "https://www.linkedin.com/in/…",
  "linkedinImport.saveProfile": "Save profile",
  "linkedinImport.connectedNote":
    "Connected. Imported connections are attributed to this profile, so the CRM can say which colleague knows someone rather than that \u201cthe company\u201d does.",
  "linkedinImport.notConnectedNote":
    "Not connected yet. Adding your profile URL attributes any connections you import to you by name.",
  "linkedinImport.whichFile":
    "The file you want is Connections.csv \u2014 the export archive holds a dozen others.",
  "linkedinImport.choose": "Choose Connections.csv",
  "linkedinImport.noMatchesYet":
    "No matches yet, which is normal in a new organization: your connections are matched against contacts the CRM knows, and those arrive as your mail is read. This runs again every hour, so matches appear as the CRM fills up.",
  "linkedinImport.working": "Reading your export…",
  "linkedinImport.imported": "Connections imported",
  "linkedinImport.confirmed": "Matched to a contact",
  "linkedinImport.suggested": "Awaiting your confirmation",

  // The review queue and the reach table (ADR-0078 §2.1b).
  "linkedinReach.title": "Where your network reaches",
  "linkedinReach.sub":
    "Accounts on file where you already know somebody, most connections first.",
  "linkedinReach.empty":
    "None of your connections work at an account on file yet.",
  "linkedinReach.allUnresolved":
    "All {unresolved} of your connections work somewhere that is not an account on file yet.",
  "linkedinReach.account": "Account",
  "linkedinReach.connections": "You know",
  "linkedinReach.onFile": "Already contacts",
  "linkedinReach.onFileOf": "{onFile} of {total}",
  "linkedinReach.footnote":
    "Showing {shown} of {total} accounts. {unresolved} connections work somewhere that is not an account on file yet.",
  "linkedinImport.skipped": "Rows skipped (no usable name)",
  "co.connections.group.contacts": "contacts",
  "co.connections.group.deals": "deals",
  "co.connections.group.intro_path": "the warm intro",
  "co.connections.group.our_side": "who here is connected",
  "co.signals.title": "Signals",
  "co.signals.empty": "No open signal on this account.",
  "chronology.label": "What to show in the timeline",
  "chronology.activities": "Activities",
  "chronology.changes": "Changes",
  "chronology.all": "All",
  "chronology.changesEmpty":
    "No field on this record has been changed since it was created.",
  "chronology.allEmpty": "Nothing has happened on this record yet.",
  "chronology.truncated":
    "Older entries are not shown here — there are more of both kinds than this view can put in order. Pick Activities or Changes to read further back.",
  "chronology.truncatedActivities":
    "There are more activities here than fit. Only the most recent ones are listed.",
  "timeline.sent": "Sent",
  "timeline.received": "Received",
  "timeline.withheld": "Content for participants only",
  "compose.audience": "Visibility",
  "compose.audienceTitle": "Who may read this message?",
  "compose.audienceLegend": "Visibility of this one message",
  "compose.audienceWorkspace": "Everyone in the organization",
  "compose.audienceWorkspaceHint":
    "Anyone who may see the contact reads this message too.",
  "compose.audienceParticipants": "Participants only",
  "compose.audienceParticipantsHint":
    "Only the people on this message read its subject and body. Others see that a message was exchanged that day, nothing more.",
  "compose.audienceConfirm": "Save visibility",
  "compose.audienceNote":
    "Applies to this message only \u2014 not to the thread and not to the contact.",
  "timeline.textMore": "Read it",
  "timeline.textLess": "Show less",
  "timeline.tailMore": "Show signature and quoted text",
  "timeline.tailLess": "Hide signature and quoted text",
  "co.profileField.display_name": "Company name",
  "co.profileField.offer_summary": "What they sell",
  "co.profileField.icp": "Who they sell to",
  "co.profileField.buying_center": "Who decides there",
  "co.profileField.value_proposition": "What they promise",
  "co.profileField.usp": "How they differentiate",
  "co.profileField.customer_pains": "Pain they solve",
  "co.profileField.desired_outcomes": "Outcome they promise",
  "co.profileField.buying_intents": "What triggers a purchase",
  "co.profileField.common_objections": "Objections they meet",
  "co.profileField.sales_motion": "How they sell",
  "co.profileField.legal_name": "Registered legal name",
  "co.profileField.registered_address": "Registered address",
  "co.profileField.register_vat": "Register / VAT ID",
  "co.profileField.industry": "Industry",
  "co.profileField.history": "History",
  "co.profile.title": "Company profile",
  "co.reach.window": "Contact status for the last 90 days",
  "co.reach.answered": "Answered",
  "co.reach.silent": "No reply",
  "co.reach.untried": "Not approached",
  "co.role.set": "Set role",
  "co.role.setOn": "What is {name} on this deal?",
  "co.role.explain":
    "The champion argues for you when you are not in the room. The economic buyer signs. Naming both is what turns a list of contacts into a picture of the decision.",
  "co.role.onDeal": "On which deal",
  "co.role.role": "Role",
  "co.role.champion": "champion",
  "co.role.economic_buyer": "economic buyer",
  "co.role.blocker": "blocker",
  "co.role.influencer": "influencer",
  "co.role.user": "end user",
  "co.people.missing":
    "No {roles} is named on the open deal yet — set one on the contact who is.",
  "co.people.untriedHint": "{count} people here have never been approached.",
  "co.people.untriedHintOne": "One person here has never been approached.",
  "co.evidence.extractedUnconfirmed": "AI extracted · not yet confirmed",
  "co.evidence.previous": "Previous claim",
  "co.evidence.next": "Next claim",
  "co.evidence.title": "Where this came from",
  "co.relationships.title": "Linked people and companies",
  "co.tools.title": "Data & tools",
  "co.prep.withheld":
    "Parts of this account are hidden from you, so this reading is incomplete.",
  "co.read.newActivityOne": "One new item since your last visit.",
  "co.read.newActivityMany": "{count} new items since your last visit.",
  "co.factField.founded_year": "Founded",
  "co.factField.employee_range": "Employees",
  "co.factField.phone": "Phone",
  "co.factField.contact_email": "Contact email",
  "co.factField.location": "Location",
  "co.factField.service": "Service",
  "co.factField.product": "Product",
  "co.factField.capability": "Capability",
  "co.factField.served_industry": "Serves",
  "co.factField.company_size": "Size",
  "co.factField.geography": "Geography",
  "co.factField.language": "Language",
  "co.factField.certification": "Certification",
  "co.factField.partner": "Partner",
  "co.factField.named_customer": "Customer",
  "co.factField.technology": "Technology",
  "co.factField.quantified_outcome": "Result",
  "co.facts.showAll": "Show all {count}",
  "co.facts.showLess": "Show fewer",
  "co.tags.lists": "Lists",
  "co.tags.tags": "Tags",
  "co.tags.noLists": "Not on any list.",
  "co.tags.noTags": "No tags applied.",
  "co.deal.new": "New deal",
  "co.tags.apply": "Add tag",
  "co.tags.pick": "Tag name",
  "co.lists.add": "Add to list",
  "co.lists.pick": "List name",
  "co.recent.title": "Recent activity",
  "co.recent.empty": "Nothing logged with them yet.",
  "co.recent.viewHistory": "View history",
  "co.tags.title": "Lists & tags",
  "co.timeline.empty": "Nothing logged on this account yet.",
  "co.overlayFallback":
    "This account is served from the connected system of record, so the company view is not assembled here. Open it in that system to see the full picture.",
  "org.domains": "Domains",
  "org.firmographicsEmpty":
    "Nothing read yet — grounded profile fields appear here once a site read confirms them.",
  "org.facts": "Facts read from the site",
  "org.factCategory.company": "Company",
  "org.factCategory.offering": "Offering",
  "org.factCategory.market": "Market",
  "org.factCategory.signal": "Signals",

  "lead.score": "Score",
  "lead.status": "Status",
  "lead.nextTask": "Next step",
  "lead.openTaskCount": "{count} open tasks",
  "lead.noNextTask": "No next step",
  "lead.scoreNoSignals": "No qualifying signals",
  "lead.source": "Source",
  "lead.project": "Project",
  "lead.openLinkedIn": "Open LinkedIn profile",
  "lead.filterSource": "Source",
  "lead.filterSourceAll": "All sources",
  "lead.source.manual": "Created manually",
  "lead.source.inbound": "Inbound",
  "lead.source.webform": "Web form",
  "lead.source.referral": "Referral",
  "lead.source.import": "Import",
  "lead.source.crawl": "Web research",
  "lead.source.unknown": "Unknown source",
  "lead.sourceFromConnector":
    "Written by a connector — it keeps its own source.",
  "leadSources.title": "Lead sources",
  "leadSources.sub":
    "Where leads come from. Used in the New lead form, as a filter, and by the score.",
  "leadSources.readOnly": "Only an admin or ops seat changes this list.",
  "leadSources.labelFor": "Label of source {key}",
  "leadSources.intentFor": "Intent of {label}",
  "leadSources.intent": "Intent",
  "leadSources.intent.high": "High interest",
  "leadSources.intent.neutral": "Neutral",
  "leadSources.intent.low": "Low interest",
  "leadSources.intentHint":
    "High adds points to the score, Low subtracts; a change applies on each lead's next rescore.",
  "leadSources.leadCount": "{count} leads",
  "leadSources.builtIn": "built-in",
  "leadSources.builtInKept":
    "Built-in sources can be renamed and switched off, not removed.",
  "leadSources.inUse": "{count} leads use this source — switch it off instead.",
  "leadSources.deactivateInstead": "switch off instead",
  "leadSources.activeFor": "{label} is active",
  "leadSources.remove": "Remove",
  "leadSources.removeTitle": "Remove this source?",
  "leadSources.removeBody":
    '"{label}" is not used by any lead and will disappear from the list.',
  "leadSources.newLabel": "New source",
  "leadSources.newPlaceholder": "Trade show",
  "leadSources.add": "Add source",
  "leadSources.discoveredSub":
    "From integrations and imports — values that live on leads but are not in the list yet. Add one to give it a label and a weight.",
  "leadSources.adopt": "Add to list",
  "leadReasons.title": "Disqualification reasons",
  "leadReasons.sub":
    "What a rep picks when a lead is dropped. The reason shows on the lead and can be filtered on.",
  "leadReasons.labelFor": "Label of reason {label}",
  "leadReasons.leadCount": "{count} leads",
  "leadReasons.inUse":
    "{count} leads carry this reason — switch it off instead.",
  "leadReasons.newLabel": "New reason",
  "leadReasons.add": "Add reason",
  "leadReasons.removeTitle": "Remove this reason?",
  "leadReasons.removeBody":
    '"{label}" is not used by any lead and will disappear from the list.',
  "leadHandling.title": "Lead handling",
  "leadHandling.sub": "How this installation treats a new lead.",
  "leadHandling.firstResponse": "First-response target",
  "leadHandling.firstResponseHint":
    "Off by default. On, every open lead carries a reply deadline, the list gains the Overdue view, and overdue leads sort first.",
  "leadHandling.targetMinutes": "Target (minutes)",
  "leadHandling.targetOutOfRange":
    "Enter a whole number of minutes between 15 and 10080 (7 days).",
  "leadHandling.targetHint":
    "How long a lead may wait for its first reply once it is routed (or created). 15 minutes to 7 days.",
  "lead.boardCount": "{count} leads",
  "lead.duplicateFound":
    "A lead with this email or LinkedIn profile already exists.",
  "lead.promote": "Qualify…",
  "lead.promoteIneligible": "needs an email address and an open status",
  "lead.filterStatus": "Status",
  "lead.filterStatusAll": "All statuses",
  "lead.filterScore": "Score",
  "lead.filterScoreAll": "Any score",
  "lead.bulkSelected": "{count} selected",
  "lead.bulkOwner": "New owner",
  "lead.bulkOwnerPick": "Pick an owner",
  "lead.bulkAssign": "Assign",
  "lead.bulkDisqualify": "Disqualify",
  "lead.bulkFailed": "{count} not applied —",
  "lead.bulkFailedRow": "could not be saved",
  "lead.bulkSelectRow": "Select {name}",
  "lead.sla.breached": "Overdue",
  "lead.sla.atRisk": "Due soon",
  "lead.sla.withinTarget": "On time",
  "lead.sla.answeredAt": "First response on {at}",
  "lead.sla.dueBy": "First response due by {at}",
  "lead.sla.overdueSince": "First response was due {at}",
  "lead.filterSla": "Response",
  "lead.filterSlaAll": "Any",
  "list.viewOverdue": "Overdue",
  "lead.filterScoreHot": "80 and up",
  "lead.filterScoreWarm": "60 and up",
  "lead.filterScoreCool": "40 and up",
  "lead.details": "Details",
  "lead.detailsUnset": "Not set",
  "lead.terminalReadOnly": "This lead is closed and takes no changes.",
  "lead.boardTerminalOnly":
    "The board shows open leads only. These leads are promoted or disqualified.",
  "person.fromLead": "From lead",
  "lead.promotedTitle": "Promoted to a contact",
  "lead.promotedMerged":
    "This lead merged into a contact we already knew — no duplicate was created.",
  "lead.promotedCreated": "This lead became a new contact.",
  "lead.promotedAt": "Promoted",
  "lead.promotedTrigger": "Trigger:",
  "lead.promotedEvidence": "Evidence:",
  "lead.previewPending": "Checking whether we already know this person…",
  "lead.previewCreate": "Promoting will create a new contact.",
  "lead.previewMerge": "Promoting will merge into the existing contact",
  "lead.previewMergeWithheld":
    "Promoting will merge into an existing contact you cannot see.",
  "lead.demote": "Reverse promotion",
  "lead.demoteDialog": "Reverse this promotion?",
  "lead.demoteExplain":
    "The lead returns to the queue as “Working”. A contact the promotion created is archived; a contact it merged into stays as it is. A contact on a live deal cannot be reversed.",
  "lead.demoteReason": "Reason (recorded in the audit trail)",
  "lead.demoteConfirm": "Reverse",
  "lead.promotedOutcomePending": "Reading what this promotion did…",
  "lead.promotedOutcomeUnavailable":
    "We cannot show whether this merged or created a contact.",
  "lead.terminalPromoted": "Promoted — this lead is now read-only.",
  "lead.statusNew": "New",
  "lead.statusContacted": "Contacted",
  "lead.statusEngaged": "Engaged",
  "lead.statusPromoted": "Qualified",
  "lead.statusDisqualified": "Disqualified",
  "lead.disqualified": "Disqualified",
  "lead.status.new": "New",
  "lead.status.contacted": "Contacted",
  "lead.status.engaged": "Engaged",
  "lead.explainScore": "Explain this score",
  "lead.scoreOverridden": "Human override: {reason}",
  "lead.machineScore": "Machine score was {score}",
  "lead.overrideScore": "Override score",
  "lead.clearOverride": "Clear override",
  "lead.overrideReason": "Reason",
  "lead.shortfall.lead": "What this score has to work with:",
  "lead.shortfall.engagementMoves":
    "A reply or a meeting is what moves it most.",
  "lead.shortfall.noSource":
    "No source on record — nothing says where this lead came from.",
  "lead.shortfall.sourcePenalised":
    "Came in as “{source}”, which counts against the score.",
  "lead.shortfall.noTitle": "No job title on record.",
  "lead.shortfall.titleNotSenior":
    "“{title}” isn’t one of the senior titles the model looks for.",
  "lead.shortfall.sourceNoIntent":
    "Came in as “{source}”, which carries no buying intent on its own.",
  "lead.scoreNotStoredYet":
    "The breakdown for this score isn’t stored yet — the next update will show it.",
  "lead.scoreLoading": "Working out why…",
  "lead.scoreNoFactors": "Nothing counted toward this score yet.",
  "lead.scoreFactorsExplainMachine":
    "You set this score by hand. The factors below explain what the model says: {score}.",
  "lead.scoreDecayed": "{base} halving every 14 days",
  "lead.scoreSources": "{count} activities",
  "lead.scoreReconciles": "{raw} adds up, rounds to {rounded}, scored {score}",
  "lead.factor.decision_maker_title": "Decision-maker title",
  "lead.factor.high_intent_source": "Came from a high-intent source",
  "lead.factor.low_intent_source": "Came from a low-intent source",
  "lead.factor.reply": "They replied",
  "lead.factor.meeting_held": "Meeting held",
  "lead.factor.meeting_booked": "Meeting booked",
  "lead.signalsTitle": "What you know about this lead",
  "lead.signalUnset": "Not entered",
  "lead.signalClear": "Withdraw",
  "lead.signalBandPick": "Pick a value",
  "lead.signalMore": "More…",
  "lead.signalProvenanceHint":
    "Untouched, an answer is stored as an estimate with no confidence claimed.",
  "lead.signalEvidenceQuality": "How reliable is this?",
  "lead.signalConfidence": "Confidence",
  "lead.signalConfidenceUnstated": "Not stated",
  "lead.signalConfidenceValue": "{value}% confidence",
  "lead.signalRecordedAt": "Recorded {at}",
  "lead.signalSuperseded": "Previously {value}; replaced by {source}",
  "lead.signalAutomaticSource": "an automatic source",
  "lead.signalReason": "How do you know?",
  "lead.signalReasonHint":
    "Optional. Whatever you write is stored with the score.",
  "lead.signalReasonUnstated": "No source given. Entered by hand.",
  "lead.signalSave": "Add to the score",
  "lead.signal.web_traffic": "Web traffic",
  "lead.signal.employees": "Employees",
  "lead.signal.budget_hint": "Budget",
  "lead.signal.ask.web_traffic": "Website traffic?",
  "lead.signal.ask.employees": "Company size?",
  "lead.signal.ask.budget_hint": "Budget?",
  "lead.signal.fact": "Verified",
  "lead.signal.assumption": "Estimated",
  "lead.signal.judgement": "My assessment",
  "lead.signal.web_traffic.low": "Low",
  "lead.signal.web_traffic.medium": "Medium",
  "lead.signal.web_traffic.high": "High",
  "lead.signal.employees.1-10": "1–10",
  "lead.signal.employees.11-50": "11–50",
  "lead.signal.employees.51-200": "51–200",
  "lead.signal.employees.201+": "201+",
  "lead.signal.budget_hint.none": "No budget",
  "lead.signal.budget_hint.unknown": "Unknown",
  "lead.signal.budget_hint.some": "Some budget",
  "lead.signal.budget_hint.confirmed": "Budget confirmed",
  "lead.factor.manual:web_traffic": "Web traffic (your input)",
  "lead.factor.manual:employees": "Employees (your input)",
  "lead.factor.manual:budget_hint": "Budget (your input)",
  "lead.ownerLabel": "Owner",
  "lead.ownerYou": "You",
  "lead.overriddenBadge": "overridden",
  "lead.unassigned": "Unassigned",
  "lead.terminalDisqualified": "Disqualified — this lead is now read-only.",
  "lead.marker": "Lead",
  "lead.assign": "Assign",
  "lead.assignToMe": "Assign to me",
  "lead.assignTo": "Assign this lead to",
  "lead.assignChoose": "Choose a colleague",
  "lead.assignNobodyElse": "No other user to assign this lead to.",
  "lead.saveOverride": "Save override",
  "lead.overrideScoreValue": "Score",
  "lead.trigger.inboundReply": "Inbound reply",
  "lead.trigger.meetingBooked": "Meeting booked",
  "lead.trigger.meetingHeld": "Meeting held",
  "lead.trigger.humanQualify": "Human qualified",
  "lead.evidenceNote": "Evidence note (optional)",
  "lead.segregation":
    "Leads are kept apart from Contacts. A lead becomes a contact only when you qualify it.",
  "lead.segregationDismiss": "Got it",
  "lead.emptyMine": "You own no leads.",
  "lead.showAll": "Show all",
  "lead.assignedAway": "{names} assigned to {owner} — no longer in Mine.",
  "lead.viewNew": "New",
  "lead.viewNeedsFollowUp": "Needs follow-up",
  "lead.viewEngaged": "Engaged",
  "lead.ladder": "Lead status",
  "lead.ladder.new": "New — nobody has reached out yet.",
  "lead.ladder.overlay":
    "The mirror does not move a lead's status; change it in the source system.",
  "lead.ladder.automatic": "{label} · set automatically from captured activity",
  "lead.ladder.automaticWith": "{label} · set automatically — {what} on {at}",
  "lead.ladder.byHand": "{label} · set by hand",
  "lead.ladder.theyReplied": "they replied",
  "lead.ladder.meetingBooked": "a meeting was booked",
  "lead.ladder.meetingHeld": "a meeting was held",
  "lead.ladder.qualified": "Qualified — this lead is a contact now.",
  "lead.ladder.qualifiedOn": "Qualified on {at} — this lead is a contact now.",
  "lead.ladder.disqualified": "Disqualified.",
  "lead.ladder.disqualifiedWithReason": "Disqualified: {reason}",
  "lead.qualify.title": "Qualify {name}",
  "lead.qualify.contact": "Contact",
  "lead.qualify.alsoDeal": "Also open a deal",
  "lead.qualify.pipeline": "Pipeline",
  "lead.qualify.stage": "Stage",
  "lead.qualify.dealName": "Deal name",
  "lead.qualify.amount": "Amount ({currency})",
  "lead.qualify.amountHint":
    "Optional. Whole units; the installation's base currency.",
  "lead.qualify.amountInvalid": "Enter a number, or leave it empty.",
  "lead.qualify.amountNoCurrency":
    "Waiting for the installation's base currency — try again in a moment, or leave the amount empty.",
  "lead.qualify.why": "Why",
  "lead.qualify.reasonReplied": "Reason: they replied on {at}.",
  "lead.qualify.reasonMeetingBooked": "Reason: a meeting was booked for {at}.",
  "lead.qualify.reasonMeetingHeld": "Reason: a meeting was held on {at}.",
  "lead.qualify.reasonHuman": "Reason: qualified by you.",
  "lead.qualify.confirm": "Qualify",
  "lead.qualify.confirmWithDeal": "Qualify and open deal",
  "lead.qualify.done": "{name} is now a contact:",
  "lead.disqualify.title": "Disqualify {name}",
  "lead.disqualify.reason": "Reason",
  "lead.disqualify.pickReason": "Pick a reason",
  "lead.disqualify.reasonRequired": "Pick a reason first.",
  "lead.disqualify.note": "Note (optional)",
  "lead.disqualify.confirm": "Disqualify",

  "deals.viewBoard": "Board",
  "deals.viewTable": "Table",
  "deals.amount": "Value",
  "deals.lastSignal": "Last signal",
  "deals.lastSignalNone": "no signal yet",
  "deals.stage": "Stage",
  "deals.close": "Expected close",
  "deals.confirmAdvance": "Move to {stage}?",
  "deals.confirmTerminal":
    "This closes the deal as {status}. Confirm first — nothing happens until you do.",
  "deals.lostReason": "Lost reason",
  "deals.winNoEvidence":
    "This deal has no signed contract attached, so tell us how it was won. The answer is kept on the deal and counted in reports.",
  "deals.winReason": "How was it won?",
  "deals.winReasonPick": "Pick how it was won",
  "deals.winReasonImported": "Imported from another system",
  "deals.winReasonPurchaseOrder": "On a purchase order",
  "deals.winReasonVerbal": "Verbally, in person or by phone",
  "deals.winReasonRenewalByEmail": "Renewed by email",
  "deals.winReasonOther": "Something else",
  "deals.winReasonDetail": "What was it?",
  "deals.confirm": "Confirm",
  "deals.cancel": "Cancel",
  "deals.advanced": "Moved to {stage}",
  "deal.pendingApprovals": "Awaiting your confirmation",
  "deal.stakeholders": "Stakeholders",
  "deal.edit": "Edit deal",
  "deal.ownerKeep": "Keep current owner",
  "deal.ownerMe": "Assign to me",
  "deal.ownerUnassign": "Unassign",
  "deal.partnerOrg": "via Partner",
  "deal.forecastCategory": "Forecast category",
  "deal.waitUntil": "Wait until",
  "deal.fxBase": "Base {value} · rate {rate} as of {date}",
  "deal.archive": "Archive deal",
  "deal.archiveConfirm":
    "Archiving removes this deal from the active pipeline. This cannot be undone from the UI.",
  "deal.archivedReadOnly": "This deal is archived and takes no changes.",
  "deal.reopen": "Reopen",
  "deal.reopenPick": "Move this deal back to an open stage",
  "deal.reopenConfirm": "Reopen",
  "deal.fcCommit": "Commit",
  "deal.fcBestCase": "Best case",
  "deal.fcPipeline": "Pipeline",
  "deal.fcOmitted": "Omitted",
  "deal.fcSlipped": "Slipped",
  "deal.fcUncategorised": "No category yet",

  "deals.pipeline": "Pipeline",
  "deals.filterStalled": "Stalled only",
  "deals.filterOwnerMe": "My deals",
  "deals.filterPartnerSourced": "Partner-sourced",
  "deals.filterStageAll": "All stages",
  "deals.filterOrgAll": "All companies",
  "deals.filterStalledAll": "All deals",
  "deals.filterOwnerAll": "All owners",
  "deals.filterPartnerAll": "All sources",
  "deals.sortNewest": "Newest",
  "deals.unit": "deals",
  "deals.bulkSelected": "{count} selected",
  "deals.bulkSelectRow": "Select {name}",
  "deals.bulkOwner": "New owner",
  "deals.bulkOwnerPick": "Pick an owner",
  "deals.bulkAssign": "Assign",
  "deals.bulkStage": "Move to stage",
  "deals.bulkStagePick": "Pick a stage",
  "deals.bulkMove": "Move",
  "deals.bulkArchive": "Archive",
  "deals.bulkArchiveConfirmTitle.one": "Archive this deal?",
  "deals.bulkArchiveConfirmTitle.other": "Archive {count} deals?",
  "deals.bulkArchiveConfirmBody":
    "They leave every list and report, and there is no way to bring one back from here yet.",
  "deals.bulkFailed": "{count} not applied —",
  "deals.bulkFailedRow": "could not be saved",

  "deal.offers": "Offers",
  "deal.newOffer": "New offer",
  "deal.offerNeedsCurrency":
    "Price this deal first — an offer is written in the deal's own currency.",
  "deal.offerNumber": "Offer #",
  "deal.offerRevision": "Rev.",
  "deal.offersEmpty": "No offers yet",

  "offer.revision": "Revision {revision}",
  "offer.backToDeal": "Back to deal",
  "offer.totals": "Totals",
  "offer.net": "Net",
  "offer.tax": "Tax",
  "offer.gross": "Gross",
  "offer.edit": "Edit header",
  "offer.currency": "Currency",
  "offer.buyerOrg": "Buyer organization",
  "offer.buyerOrgConfirm": "Buyer organization: {name}",
  "offer.template": "Template",
  "offer.validUntil": "Valid until",
  "offer.introText": "Intro text",
  "offer.termsText": "Terms text",
  "offer.lines": "Line items",
  "offer.addLine": "Add line",
  "offer.position": "Pos.",
  "offer.description": "Description",
  "offer.unit": "Unit",
  "offer.quantity": "Quantity",
  "offer.unitPrice": "Unit price",
  "offer.discountPct": "Discount %",
  "offer.taxRate": "Tax %",
  "offer.lineTotal": "Line total",
  "offer.unpriced": "unpriced — excluded from total",
  "offer.removeLine": "Remove",
  "offer.pickProduct": "Pick product",
  "offer.pickProductConfirm": "Product: {name}",
  "offer.send": "Send",
  "offer.sendConfirm": "Send this offer to the buyer?",
  "offer.sendBody": "The offer becomes read-only until the buyer responds.",
  "offer.accept": "Accept",
  "offer.acceptConfirm": "Mark this offer as accepted?",
  "offer.acceptBody":
    "The deal's amount and currency will be updated to match this offer.",
  "offer.reject": "Reject",
  "offer.rejectConfirm": "Mark this offer as rejected?",
  "offer.rejectReason": "Reason (optional)",
  "offer.regenerate": "Regenerate revision",
  "offer.aiDisclosureTitle": "AI-assisted disclosure",
  "offer.diffAdded": "{count} line(s) added",
  "offer.diffRemoved": "{count} line(s) removed",
  "offer.diffChanged": "{count} line(s) changed",
  "offer.renderPdf": "Render PDF",
  "offer.viewPdf": "View PDF",
  "offer.pdfUnavailable": "PDF rendering not available on this deployment.",

  "inbox.sub":
    "everything staged, waiting on your call — nothing runs without it",
  "inbox.viaTool": "via {verb}",
  "inbox.approveEdited": "Approve edited",
  "inbox.reject": "Reject",
  "inbox.tab.pending": "Pending",
  "inbox.tab.decided": "Decided",
  "inbox.rejectReason": "Reason",
  "inbox.draftSubject": "Subject",
  "inbox.draftBody": "Message",
  "inbox.rejectReasonHint": "Shared with the person this was staged for.",
  "inbox.tokenTitle": "Approval token",
  "inbox.tokenOnce": "Copy it now — you'll only see this token once.",
  "inbox.copy": "Copy",
  "inbox.copied": "Copied",
  "inbox.tokenDone": "Done",
  "inbox.dismiss": "Dismiss",
  "inbox.versionSkew":
    "This record changed since it was staged — re-stage it before deciding.",
  "inbox.reRead": "Re-read",
  "inbox.alreadyDecided": "Already decided — nothing left to do here.",
  "inbox.expired": "Expired",
  "inbox.expiresIn": "expires in {countdown}",
  "inbox.detail": "Approval detail",
  "inbox.status.approved": "Approved",
  "inbox.status.rejected": "Rejected",
  "inbox.status.expired": "Expired",

  // One act's proposals, read as one question (a site read publishes the
  // company's facts plus a lead per person on the team page). The count is
  // always two or more — a bundle of one is drawn as an ordinary row — so these
  // sentences need no singular form; the per-member report does, because any
  // one of its outcomes can hold a single proposal.
  "inbox.bundle.why":
    "One action staged these {count} proposals together. Answer them all at once, or open them and decide one at a time.",
  "inbox.bundle.members": "The {count} proposals",
  "inbox.bundle.approveAll": "Approve all {count}",
  "inbox.bundle.rejectAll": "Reject all {count}",
  "inbox.bundle.approveAllConfirm":
    "Every proposal is still decided on its own: one that has lapsed, or that somebody has already answered, is reported back rather than overwritten, and the rest go through.",
  "inbox.bundle.rejectReasonHint":
    "Recorded on every proposal this rejects, and shared with the people they were staged for.",
  "inbox.bundle.expiresIn": "first expires in {countdown}",
  "inbox.bundle.result.approved": "{count} approved",
  "inbox.bundle.result.rejected": "{count} rejected",
  "inbox.bundle.result.alreadyDecided.one":
    "{count} already carried a verdict, which stands as it was.",
  "inbox.bundle.result.alreadyDecided.other":
    "{count} already carried verdicts, which stand as they were.",
  "inbox.bundle.result.expired.one":
    "{count} had already lapsed — propose it again to decide it.",
  "inbox.bundle.result.expired.other":
    "{count} had already lapsed — propose them again to decide them.",
  "inbox.bundle.result.effectFailed.one":
    "{count} was approved, but its change did not land — that record is unchanged.",
  "inbox.bundle.result.effectFailed.other":
    "{count} were approved, but their changes did not land — those records are unchanged.",

  "home.brief": "Morning brief",
  "home.sub": "ranked from live signals — staged actions first",
  "home.staged": "Waiting on you",
  "home.stalled": "Stalled deals",
  "home.pipeline": "Open pipeline",
  "home.pipelineWeighted": "{amount} weighted",
  "home.pipelineCount.one": "{count} open deal",
  "home.pipelineCount.other": "{count} open deals",
  "home.pipelinePartial":
    "{count} deals are not in these figures — your access does not cover them.",
  "home.pipelineUnavailable": "This figure could not be loaded.",
  "home.queue": "Today's queue",
  "home.asOf": "as of {at}",
  "home.refresh": "Refresh brief",
  "home.refreshing": "Ranking…",
  "home.generate": "Generate my first brief",
  "home.noneTitle": "No brief yet",
  "home.noneBody":
    "Your morning brief ranks the deals worth your first hour — winnability, revenue, timing, momentum, and warmth, each factor with its evidence. Generate the first run once you have open deals.",
  "home.honestShort":
    "Only {count} deals cleared the bar — the queue is never padded.",
  "home.overflow":
    "{shown} of {count} qualifying deals — the honest-short top slice.",
  "home.quietRun":
    "Nothing cleared the bar this morning. No invented urgency — enjoy the quiet.",
  "home.act": "Done",
  "home.dismiss": "Dismiss",
  "home.actedState": "acted",
  "home.dismissedState": "dismissed",
  "home.why": "Why this ranks",
  "home.evidence": "{count} evidence rows",
  "home.evidenceOne": "1 evidence row",
  "home.score": "score {pct}%",
  "home.openDeal": "Open deal",
  "home.factorWinnability": "Winnability",
  "home.factorRevenue": "Revenue",
  "home.factorTiming": "Timing",
  "home.factorMomentum": "Momentum",
  "home.factorWarmth": "Warmth",

  "home.digest": "Overnight capture",
  "home.digestFor": "digest for {date}",
  "home.digestSynced": "Emails synced",
  "home.digestPeople": "People created",
  "home.digestOrgs": "Companies created",
  "home.digestApprovals": "Approvals pending",
  "home.digestDedupe": "Duplicates to review",
  "home.digestClassify":
    "Classified overnight: {commitments} commitments · {meetings} meetings · {noise} noise",

  "enrich.title": "Read from the website",
  "enrich.sub":
    "evidence-or-omit — fills only empty fields, and only after your approval",
  "enrich.cta": "Read now",
  "enrich.reading": "Reading…",
  "enrich.staged":
    "Staged — nothing written yet. Accept it in your inbox; only empty fields fill.",
  "enrich.toInbox": "Open inbox",
  "enrich.from": "read from {url}",

  "deepread.title": "Read the full site",
  "deepread.sub":
    "Reads up to 12 pages of the company's website. Findings are staged for your review — nothing is written until you accept.",
  "deepread.cta": "Read full site",
  "deepread.starting": "Starting…",
  "deepread.unavailable": "Site reading is not configured on this server.",
  "deepread.statusQueued": "Queued",
  "deepread.statusDeferred": "Waiting for AI budget",
  "deepread.statusRunning": "Reading…",
  "deepread.statusDone": "Done",
  "deepread.statusPartial": "Stopped early",
  "deepread.statusFailed": "Failed",
  "deepread.statusCancelled": "Cancelled",
  "deepread.resumesAt": "Resumes automatically {when}.",
  "deepread.pagesSoFar.one": "{count} page read so far",
  "deepread.pagesSoFar.other": "{count} pages read so far",
  "deepread.stoppedEarly": "Stopped early: {reason}",
  "deepread.stopBudget": "model budget",
  "deepread.stopPageCap": "page cap",
  "deepread.stopByteCap": "byte cap",
  "deepread.stopDeadline": "deadline",
  "deepread.factCount.one": "{count} evidenced fact staged",
  "deepread.factCount.other": "{count} evidenced facts staged",
  "deepread.pagesRead": "Pages read",
  "deepread.skippedPages": "Pages skipped",
  "deepread.skipRobots": "robots.txt",
  "deepread.skipOffDomain": "off domain",
  "deepread.skipPageCap": "page cap",
  "deepread.skipByteCap": "byte cap",
  "deepread.skipUnreadable": "unreadable",
  "deepread.proposals": "{count} proposals waiting for your review",
  "deepread.proposalsOne": "1 proposal waiting for your review",
  "deepread.kindHome": "Home",
  "deepread.kindImpressum": "Impressum",
  "deepread.kindAbout": "About",
  "deepread.kindTeam": "Team",
  "deepread.kindServices": "Services",
  "deepread.kindProducts": "Products",
  "deepread.kindContact": "Contact",
  "deepread.kindOther": "Other",

  "transcriptread.title": "Read this transcript",
  "transcriptread.sub":
    "Find the next steps and commitments this conversation states. Nothing is written until you confirm.",
  "transcriptread.cta": "Read transcript",
  "transcriptread.starting": "Starting…",
  "transcriptread.unavailable":
    "Transcript reading is not configured on this server.",
  "transcriptread.statusQueued": "Queued",
  "transcriptread.statusRunning": "Reading…",
  "transcriptread.statusDone": "Done",
  "transcriptread.statusFailed": "Failed",
  "transcriptread.lineCount.one": "{count} line read",
  "transcriptread.lineCount.other": "{count} lines read",
  "transcriptread.proposals": "{count} next steps waiting for your review",
  "transcriptread.proposalsOne": "1 next step waiting for your review",
  "transcriptread.nothingStated":
    "Read in full. This conversation states no next steps.",
  "transcriptread.failedFallback":
    "This transcript could not be read. Nothing was staged.",

  "create.cancel": "Cancel",
  "create.multiselect.required": "Required — select at least one.",
  "create.save": "Create",
  "create.saving": "Creating…",
  "create.contact": "New contact",
  "create.company": "New company",
  "create.lead": "New lead",
  "create.deal": "New deal",
  "create.fullName": "Full name",
  "create.firstName": "First name",
  "create.lastName": "Last name",
  "create.personTitle": "Title",
  "create.email": "Email",
  "create.phone": "Phone",
  "create.linkedin": "LinkedIn",
  "create.linkedinUrl": "LinkedIn URL",
  "create.displayName": "Company name",
  "create.legalName": "Legal name",
  "create.industry": "Industry",
  "create.sizeBand": "Company size",
  "co.address.summary": "Address",
  "co.address.add": "Add an address",
  "create.addressLine1": "Street and number",
  "create.addressLine2": "Address line 2",
  "create.city": "City",
  "create.region": "State / region",
  "create.postalCode": "Postal code",
  "create.country": "Country (ISO-3166, e.g. DE)",
  "create.companyName": "Company",
  "create.dealName": "Deal name",
  "create.amount": "Value",
  "create.currency": "Currency",
  "create.stage": "Stage",
  "create.organization": "Company",
  "create.expectedClose": "Expected close",

  "field.unset": "Not set",
  "field.addEmail": "Add email",
  "field.addPhone": "Add phone",
  "field.addDomain": "Add domain",
  "field.addLegalName": "Add legal name",
  "field.addIndustry": "Add industry",
  "field.addLinkedinUrl": "Add LinkedIn URL",
  "field.addFullName": "Add name",
  "field.addTitle": "Add title",
  "field.addAddressLine1": "Add street and number",
  "field.addAddressLine2": "Add address line 2",
  "field.addPostalCode": "Add postal code",
  "field.addCity": "Add city",
  "field.addRegion": "Add state / region",
  "field.addCountry": "Add country code, e.g. DE",
  "field.country": "Country",
  "field.domain": "Domain",
  "field.domainRequired":
    "A domain cannot be cleared here — use the full editor to remove one.",
  "field.emailType": "Type",
  "field.emailWork": "Work",
  "field.emailPersonal": "Personal",
  "field.emailOther": "Other",
  "field.phoneType": "Type",
  "field.phoneWork": "Work",
  "field.phoneMobile": "Mobile",
  "field.phoneHome": "Home",
  "field.phoneOther": "Other",
  "field.primary": "Primary",
  "field.removeRow": "Remove",
  "field.yes": "Yes",
  "field.no": "No",

  "dedupe.viewExisting": "View existing record",

  "today.title": "Today on this account",
  "today.failed":
    "This could not be assembled. The rest of the page still shows what it could read.",
  "today.quiet": "Nothing here needs you today.",
  "today.withheld":
    "Hidden from you: {sections}. This list is assembled without them.",
  "today.source.nextSteps": "open tasks",
  "today.source.nextMeeting": "the calendar",
  "today.source.deals": "deals",
  "today.meeting.prepare": "Prepare meeting",
  "today.source.people": "the contacts",
  "today.source.standing": "whose move it is and the signals",
  "today.source.activities": "what was said",
  "today.route.ofThoseShown": "of the contacts shown",
  "today.tile.meeting": "Next meeting",
  "today.tile.route": "Best route",
  "today.tile.lastInteraction": "Last exchange",
  "today.exchange.subjectWhen": "{subject} · {when}",
  "today.tile.risk": "Risk",
  "today.tile.signal": "Worth knowing",
  "today.silence.days": "no answer in {count} days",
  "today.route.headline": "{colleague} → {contact}",
  "today.draft.to": "Draft follow-up to {name}",
  "today.draft.act": "Draft",

  "evidence.confirm": "Confirm",
  "evidence.correct": "Correct",
  "evidence.save": "Save",
  "evidence.saving": "Saving…",
  "evidence.cancel": "Cancel",
  "evidence.correctedValue": "Corrected value",
  "evidence.confirmedAt": "Confirmed by a person {when}",
  "evidence.humanSet": "Set by a person",
  "co.routes.untried": "Untried — nobody here has written to them",
  "co.routes.more": "+{count} more",
  "acctCoverage.open": "Compare coverage",
  "acctCoverage.title": "Who covers this account",
  "acctCoverage.contact": "Contact",
  "acctCoverage.findContact": "Find a contact",
  "acctCoverage.untried": "Untried",
  "acctCoverage.noMatch": "No contact matches that.",
  "acctCoverage.columnCap":
    "Showing {cap} colleagues — deselect one to add another.",
  "acctCoverage.partial":
    "This grid was built from a partial read, so a blank cell may mean the read stopped short rather than that nobody has tried.",
  "acctCoverage.noneButPartial":
    "Nobody connected was returned, but the read was capped — this is not a claim that nobody covers this account.",
  "acctCoverage.noneAtAll":
    "Nobody here has exchanged messages with anyone at this company yet.",
  "docs.title": "Documents",
  "docs.empty": "No documents on this account yet.",
  "docs.noneInCategory": "No documents of that kind on this account.",
  "docs.allOnAgreements":
    "Every document here is filed against an agreement above.",
  "docs.allSuperseded":
    "Only superseded documents are left here. Show them to read the history.",
  "docs.superseded.show": "Show superseded",
  "docs.superseded.hide": "Hide superseded",
  "docs.superseded.hiddenOne": "1 superseded document is hidden.",
  "docs.superseded.hiddenMany": "{count} superseded documents are hidden.",
  "docs.superseded.shownOne": "1 superseded document is listed below.",
  "docs.superseded.shownMany": "{count} superseded documents are listed below.",
  "docs.reading.show": "Read this document",
  "docs.reading.hide": "Hide the reading",

  // Adding one. The "About" wording is doing real work: it decides whether the
  // file becomes evidence in a deal or a paper about the account, and only the
  // first can be read for deal fields — so the hint says so rather than leaving
  // the reader to discover it from a panel that never appears.
  "docs.add.action": "Add a document",
  "docs.add.title": "Add a document",
  "docs.add.about": "About",
  "docs.add.aboutHint":
    "A document on a deal can be read for deal fields; one on the company cannot.",
  "docs.add.thisCompany": "This company",
  "docs.add.aDeal": "A deal",
  "docs.add.dealSearch": "Search this account's deals",
  "docs.add.dealSearchReach":
    "The search covers this account's {deals} newest deals and offers the first {matches} matches. A deal older than those cannot be picked here.",
  "docs.add.category": "Category",
  "docs.add.name": "Title",
  "docs.add.nameHint": "Optional. Left blank, the row shows the filename.",
  "docs.add.file": "File",
  "docs.add.fileHint": "Up to {size}.",
  "docs.add.fileEmpty": "Drop the file here, or click to choose one",
  "docs.add.cancel": "Cancel",
  "docs.add.submit": "Upload",
  "docs.add.uploading": "Uploading…",
  "docs.add.errNoFile": "Choose a file to upload.",
  "docs.add.errNoDeal": "Pick the deal to file this against.",
  "docs.add.errRefused": "You may not add documents to this record.",
  "docs.add.errInFlight": "This document is still uploading.",
  "docs.add.errTooLarge":
    "That file is larger than {size}, which is the most this installation accepts. Choose a smaller one.",
  "docs.add.failedTitle": "The upload did not go through",
  "docs.add.failed": "Nothing was stored. Try again, or choose another file.",
  "docs.add.partialTitle": "Uploaded, but not filed",
  "docs.add.partial":
    "The file is on the record and listed below. Only its category and title were not saved, so it is filed under Other.",

  // The staged-document-reading panel (RD-AC-N-2/-3). Three states that must
  // stay apart in the words as well as in the data: not answered yet, answered
  // and states none of them, could not be read at all.
  "extraction.neverRead": "Nobody has read this file for deal fields yet.",
  "extraction.readIt": "Read this file",
  "extraction.readAgain": "Try reading it again",
  "extraction.starting": "Starting…",
  "extraction.startFailed":
    "That file could not be sent for reading. Nothing was changed.",
  "extraction.loading": "Checking whether this file has been read…",
  "extraction.reading": "Reading this file…",
  "extraction.failed": "This file could not be read.",
  "extraction.groundedNothing":
    "AI read this file and it states none of the deal fields.",
  "extraction.heading.one":
    "AI read this file — {count} field it can ground, staged for your record (accept to persist)",
  "extraction.heading.other":
    "AI read this file — {count} fields it can ground, staged for your record (accept to persist)",
  "extraction.accept.one": "Accept {count} field",
  "extraction.accept.other": "Accept {count} fields",
  "extraction.dismiss": "Dismiss",
  "extraction.dismissed": "Nothing was written. The file stays attached.",
  "extraction.acceptedLabel": "Accepted fields",
  "extraction.acceptedHeading.one":
    "{count} field accepted to the deal — original snippets retained",
  "extraction.acceptedHeading.other":
    "{count} fields accepted to the deal — original snippets retained",
  "extraction.acceptFailed":
    "Those fields were not written. Nothing on the deal changed.",
  "extraction.edit": "Edit",
  "extraction.editValue": "Edit {field}",
  "extraction.omitted.notStated": "omitted (not stated in this file)",
  "extraction.omitted.notConfident":
    "omitted (this file says something, but not clearly enough to accept)",
  "extraction.field.name": "Deal name",
  "extraction.field.amount": "Amount",
  "extraction.field.currency": "Currency",
  "extraction.field.closeDate": "Expected close date",
  "docs.category.all": "All",
  "docs.category.contract": "Contract",
  "docs.category.offer": "Offer",
  "docs.category.legal": "Legal",
  "docs.category.email": "Email attachment",
  "docs.category.message": "Message attachment",
  "docs.category.other": "Other",
  "docs.state.draft": "Draft",
  "docs.state.current": "Current",
  "docs.state.final": "Final",
  "docs.state.superseded": "Superseded",
  "log.title": "Log activity",
  "log.addTask": "Add task",
  "log.sub": "a note or task, straight onto this timeline",
  "log.kind": "Type",
  "log.kindNote": "Note",
  "log.kindTask": "Task",
  "log.kindMeeting": "Meeting",
  "log.subject": "Subject",
  "log.body": "Details",
  "log.transcriptLabel": "Transcript",
  "log.transcriptHint":
    "Paste it from your meeting tool (Teams, Zoom, Meet…) — speaker labels, if any, are kept.",
  "log.asTranscript": "This text is a transcript",
  "log.transcriptUpload": "Or upload a file",
  "log.transcriptUploadRejected": "Only a .txt file is accepted.",
  "log.transcriptUploadFailed":
    "Could not read that file — try pasting the text instead.",
  "log.dueAt": "Due date",
  "log.date": "Date",
  "log.save": "Log",
  "log.saving": "Logging…",

  "compose.reply": "Reply",
  "compose.relink": "Relink",
  "compose.draftWithAi": "Draft with AI",
  "compose.drafting": "Drafting…",
  "compose.discardDraft": "Discard draft",
  "compose.discardDraftHint":
    "Tells your Voice DNA this draft missed. The generated text is never kept.",
  "compose.aiDisclosureTitle": "AI-assisted draft",
  "compose.aiDisclosureFallback":
    "This draft was produced by AI. Read it and edit it before you send.",
  "compose.voiceVersion": "Built from your corpus · v{n}",
  "compose.provisional": "Provisional voice",
  "compose.provisionalHint":
    "Your Voice DNA is still being built. It already shapes this draft exactly as a finished one would — nothing is held back.",
  "compose.intent": 'Steer the draft (optional), e.g. "polite follow-up"',
  "compose.to": "To",
  "compose.cc": "Cc",
  "compose.subject": "Subject",
  "compose.noGroundableRecipient":
    "No contact on this account yet — write the message yourself, or add a contact first",
  "compose.draftTo": "Draft to",
  "compose.draftToUnset": "Choose a contact",
  "compose.relatedTo": "Related to",
  "compose.relatedToNone": "The account in general",
  "compose.basedOn": "Based on: {inputs}",
  "compose.whyThisDraft": "Why this draft?",
  "compose.body": "Body",
  "compose.purpose": "Consent purpose",
  "compose.purposeHint":
    "The send is allowed only if every recipient has granted consent for this purpose.",
  "compose.sendLaterLabel": "Send later (optional)",
  "compose.sendLaterHint":
    "Leave empty to send now. A scheduled message stays off the timeline until it goes out, and the consent and mailbox checks run again at that moment — if one refuses, the message is held for you instead of sent.",
  "compose.send": "Send",
  "compose.sendConfirmTitle": "Send this email?",
  "compose.sendBody":
    "You are sending this email now. This is an outbound, irreversible action.",
  // A moment picked in the field above turns this dialog into a different
  // promise, so it says a different thing. The three sentences it replaces all
  // claim the send is happening NOW and is irreversible; a scheduled message is
  // neither, and it can be moved or withdrawn until it goes.
  "compose.schedule": "Schedule it",
  "compose.scheduleConfirmTitle": "Schedule this email?",
  "compose.scheduleBody":
    "This does not go out now. It waits for the moment you picked, and the consent and mailbox checks run again then. Until it goes you can move it or take it back from Scheduled messages.",
  "compose.sendMessageConfirmTitle": "Send this message?",
  "compose.sendMessageBody":
    "You are sending this message now. This is an outbound, irreversible action.",
  "compose.consentBlockedTitle": "Send blocked — no consent",
  "compose.consentBlocked":
    "A recipient has not granted consent for this purpose, so the send was suppressed (default-deny).",
  "compose.consentGoto": "Review consent",
  "compose.draftUnavailable":
    "AI drafting is unavailable (the model is not configured). You can still write the email yourself.",
  "compose.sendUnavailable":
    "Sending is unavailable (no mailer is configured).",
  "compose.mailboxNotSendCapable":
    "Your mailbox is connected for capture but was never granted permission to send. Reconnect it and approve sending — a mailbox connected before sending existed cannot be upgraded in place.",
  "compose.mailboxNotSendCapableGoto": "Reconnect your mailbox",
  "compose.sharedUnsubscribeToken":
    "A message carrying an unsubscribe link reaches one addressee at a time, because that link is the recipient's own consent record. Send it once per recipient, with no Cc.",
  "compose.multiRecipientWarning":
    "This purpose carries an unsubscribe link, so a send to more than one addressee will be refused. Send it once per recipient, with no Cc.",
  "compose.relinkTitle": "Relink this activity",
  "compose.relinkTarget": "Search a person, organization, deal, or lead",
  "compose.relinkReplace": "Move instead of also-link",
  "compose.relinkReplaceHint":
    "Replaces the existing link of the same type rather than adding another.",
  "compose.relinkConfirm": "Relink",
  "compose.emptyRecipients": "Add at least one recipient.",
  "compose.removeRecipient": "Remove {recipient}",
  "compose.actionFailed": "The request failed. Please try again.",

  "tasks.overdue": "Overdue",
  "tasks.today": "Today",
  "tasks.upcoming": "Upcoming",
  "tasks.undated": "No due date",
  "tasks.complete": "Done",
  "tasks.snooze": "Snooze 1d",
  "tasks.detail": "Task",
  "tasks.isDone": "Completed",
  "tasks.logged": "Logged",
  "tasks.new": "New task",
  "tasks.subject": "Subject",
  "tasks.dueDate": "Due date",
  "tasks.remindAt": "Remind me at",
  "tasks.remind": "Remind me",
  "tasks.reminder": "Reminder",
  "tasks.setReminder": "Set reminder",
  "tasks.clearReminder": "Clear reminder",
  // The scheduled-send queue's front door, offered here because a message the
  // rep told the product to send later is work of theirs that has not happened
  // yet. "Waiting to send" covers a message that is queued AND one a gate
  // stopped: both are waiting, and only the page can say which.
  "tasks.scheduledWaiting.one": "One message is waiting to send.",
  "tasks.scheduledWaiting.other": "{count} messages are waiting to send.",
  "tasks.scheduledOpen": "Scheduled messages",

  "reports.sub": "deals by stage — unweighted next to weighted",
  "reports.currency": "Currency",
  "reports.count": "Deals",
  "reports.unweighted": "Unweighted",
  "reports.weighted": "Weighted",
  "reports.planNote":
    "the executed plan and the rows this number reconciles to",
  "reports.reportDeals": "Deals by stage",
  "reports.reportForecast": "Forecast",
  "reports.reportOpenByCompany": "Open deals per company",
  "reports.forecastBanner":
    "Each tile shows the raw total and, beneath it, the probability-weighted total — rounded per deal, so it always reconciles to Explain This Number.",
  "reports.company": "Company",
  "reports.openDeals": "Open deals",
  "explain.sources": "Source rows",

  "ai.sub": "bring your own agent — governed by the two-tier contract",
  "ai.carriedQuestion": "Your question",
  "ai.tiers": "What an agent may do",
  "ai.tierAutoExecute": "Read & draft run instantly.",
  "ai.tierAutoExecuteDetail":
    "Lookups, summaries, drafts — visible, reversible, logged.",
  "ai.tierConfirmationRequired": "Write & send wait for you.",
  "ai.tierConfirmationRequiredDetail":
    "External sends and record changes stage into the inbox first.",
  "ai.connect": "Connect an agent",
  "ai.connectDetail":
    "Mint a passport in Settings and point any MCP-capable agent at your organization. It reads only what you can see.",
  "ai.paletteHint": "Ask from anywhere with",

  "settings.identity": "Profile",
  "settings.preferences": "Preferences",
  "unsaved.title": "You have unsaved changes",
  "unsaved.body":
    "Leaving this page now discards what you have typed. Go back to save it first.",
  "unsaved.discard": "Discard changes",
  "settings.saved": "Saved.",
  "settings.signature": "Email signature",
  "settings.signatureSub":
    "Appended below every message you send, above the unsubscribe footer.",
  "settings.signatureLabel": "Your sign-off",
  "settings.signaturePlaceholder": "Marek Janetzke\nGradion · +49 40 123456",
  "settings.signatureHint":
    "Plain text. Leave it empty to send unsigned. The AI never writes a sign-off — this is the one that goes out.",
  "settings.signatureSaving": "Saving…",
  "settings.preferencesSub":
    "Theme is remembered in this browser. Language lasts for this session.",
  "role.admin": "Admin",
  "role.management": "Management",
  "role.manager": "Team Lead",
  "role.rep": "Member",
  "role.readOnly": "Read-only",
  "role.ops": "Ops",
  "inlineChoice.change": "Change {field}",
  "rbac.masked": "Masked value",
  "settings.passports": "Agent passports",
  "settings.passportsSub":
    "an agent acts as you, never above you — every call re-checks your RBAC",
  // What each passport scope admits, in words. The wire carries `read`/`draft`/
  // `write`/`send`/`enrich`; a human granting them is choosing what an agent may
  // do on their behalf, and the protocol token alone does not say — "write" and
  // "send" read as near-synonyms until one of them names the mailbox.
  "passport.scope.read": "Read records",
  "passport.scope.draft": "Draft messages",
  "passport.scope.write": "Change records",
  "passport.scope.send": "Send messages",
  "passport.scope.enrich": "Buy contact data",
  "passport.select": "Passport",
  "passport.noneOption": "No passport",
  "settings.passportsLendHint":
    "These are yours to lend. Connect an MCP client and it asks which one to hand over — the connection then carries exactly that passport's scopes.",
  "settings.passportLabel": "Agent name",
  "settings.mint": "Mint passport",
  "settings.minting": "Minting…",
  "settings.mintCancel": "Cancel",
  "settings.mintDone": "Done",
  "settings.passportScopes": "What this agent may do",
  "settings.passportScopesHint":
    "Pick at least one. An agent can never do more than you can.",
  "settings.passportScopesRequired":
    "Pick at least one thing this agent may do.",
  "agents.connected": "Connected agents",
  "agents.connectedSub":
    "MCP clients holding their own credential, derived from a passport you lent",
  "agents.noneConnected": "No agent is connected yet.",
  "agents.connectedOn": "connected {date}",
  "agents.lentFrom": "lent from “{label}”",
  "agents.disconnect": "Disconnect",
  "agents.disconnectNamed": "Disconnect {client}",
  "agents.disconnected": "disconnected",
  "agents.lapsed": "credential expired",
  "agents.renewing": "renewing",
  "agents.renewsBy": "credential renews by {date}",
  "agents.expiredOn": "credential expired {date}",
  "agents.revokeGrant": "End connection",
  "agents.revokeGrantNamed": "End the connection to {client}",
  "agents.disconnectConfirm":
    "This ends the whole connection, not just one credential: the agent loses access on its next call and cannot renew. Reconnecting means lending a passport again.",
  "agents.connectHow": "Connect an agent",
  "agents.connectSteps":
    "Mint a passport above, then run one of these. The client registers itself and brings you back here to choose which passport to lend.",
  "agents.connectAntigravityPath":
    "Antigravity has no add command — put that block in ~/.gemini/config/mcp_config.json.",
  "agents.connectorOff": "The MCP connector is off for this installation.",
  "agents.connectorOffDetail":
    "No agent can connect until an operator enables it. Your passports still work as REST credentials.",
  "settings.tokenOnce": "Copy it now — you'll only see this token once.",
  "settings.token": "token",
  "settings.autonomy": "Autonomy tiers",
  "settings.autonomySub": "what runs instantly vs. what waits in the inbox",
  "settings.tierRead": "Read, summarize, draft — runs instantly, fully logged.",
  "settings.tierSend":
    "Send email, book meetings, change records — waits for your approval.",
  "settings.tierAdvance": "Advance a deal stage — always confirm-first.",
  "settings.locked": "locked",
  "settings.purposes": "Consent purposes",
  "settings.purposesSub":
    "What this installation asks consent for, and which lawful basis each purpose stands on.",
  "settings.created": "created {date}",
  "settings.expires": "expires {date}",
  "settings.revoked": "revoked",
  "settings.revoke": "Revoke",
  "settings.revokeConfirm":
    "This passport's credential is invalidated immediately — the agent loses access on its next call.",
  "import.withheld":
    "Importing a file is an admin or ops action — this installation still has one, it is not yours to run.",
  "import.title": "Import a file",
  "import.sub":
    "Bring a CSV of prospects or companies into the estate. Nothing is written until you have read what it will do.",
  "import.objectLabel": "What the rows are",
  "import.object.lead": "Prospects",
  "import.object.organization": "Companies",
  "import.objectHint.lead":
    "Bulk prospects land as leads for a human to qualify — never as contacts.",
  "import.objectHint.organization":
    "Companies are matched by the name you map, so a re-upload corrects rather than duplicates.",
  "import.fileLabel": "The CSV to import",
  "import.choose": "Choose a file",
  "import.chooseAnother": "Choose a different file",
  "import.profiled": "Read from the first {rows} rows of the file.",
  "import.col.column": "Column",
  "import.col.filled": "Filled",
  "import.col.samples": "Values",
  "import.col.destination": "Goes to",
  "import.dontImport": "Don't import",
  "import.noSamples": "empty",
  "import.destinationFor": "Where {column} goes",
  "import.identifiedBy":
    "Rows are identified by {column}, so re-importing this file updates rather than duplicates.",
  "import.needsIdentifier":
    "Map a column to {field}. Without it no row can be recognized on a second upload, or undone.",
  "import.validate": "Check what this will do",
  "import.validating": "Checking…",
  "import.previewTitle": "What this import will do",
  "import.outcomeTitle": "What this import did",
  // Shown when the card read this run back on mount instead of the reader
  // having just caused it: an outcome with no press behind it reads as an
  // import that ran by itself.
  "import.resumedRun":
    "Picked up from earlier: this import ran on {when}. Everything below is still open to you.",
  "import.count.created": "Create",
  "import.count.updated": "Update",
  "import.count.unchanged": "Unchanged",
  "import.count.skipped": "Skipped",
  "import.rowsRead": "{rows} rows read, identified by {column}.",
  "import.issuesLead":
    "Some rows cannot be imported. They are listed with the line to open in your file.",
  "import.issueLine": "Line {line}:",
  "import.commit.one": "Import 1 row",
  "import.commit.other": "Import {rows} rows",
  "import.importing": "Importing…",
  "import.done": "The import finished.",
  "import.failed":
    "The import stopped after {checkpoint} rows. Resuming continues from there rather than starting again.",
  "import.resume": "Resume the import",
  "import.another": "Import another file",
  "import.undo.one": "Undo this import (1 row)",
  "import.undo.other": "Undo this import ({rows} rows)",
  "import.undoing": "Undoing…",
  "import.undoInterrupted":
    "The undo was interrupted partway through. Continuing picks up where it stopped, not from the start.",
  "import.continueUndo": "Continue the undo",
  "import.undone": "The import was undone.",
  "import.undoReversed.one": "1 row reversed.",
  "import.undoReversed.other": "{rows} rows reversed.",
  "import.undoKeptLead": "Kept — you edited these since the import:",
  "import.undoErroredLead":
    "Could not be reversed — left exactly as they stood:",
  "settings.dangerZone": "Danger zone",
  "settings.dangerZoneSub":
    "Non-production only — irreversible on this installation.",
  "settings.resetDataDesc":
    "Reset this installation to its first-boot state. Domain and configuration data is wiped; the organization and its users are preserved and stay signed in.",
  "settings.resetDataButton": "Reset data…",
  "settings.resetDataConfirmTitle": "Reset all data?",
  "settings.resetDataConfirmBody":
    "Type your organization's name to confirm. This cannot be undone.",
  "settings.resetDataConfirmName": "Type this organization name:",
  "settings.resetDataConfirmLabel": "Confirm organization name",
  "settings.resetDataResult":
    "Cleared {tables} tables, {jobs} job rows, {streams} event streams, {keys} cache keys and {objects} stored files.",
  "settings.resetDataDrainWarning":
    "A background job was still running when the reset began. It will fail against the wiped data — harmless, but expect one error in the log.",

  "settings.jobs": "Background jobs",
  "settings.jobsSub": "What the queue is holding, and whose work failed.",
  "jobs.adminOnly":
    "Only an admin can see background-job health. It reports work across the whole installation, so it is not shown more widely.",
  "jobs.empty":
    "Nothing in the background queue — no work waiting, running, retrying or dead.",
  "jobs.workspaceKinds": "This organization",
  "jobs.workspaceEmpty":
    "No background work of any kind for this organization.",
  "jobs.dispatcherKinds": "Fleet dispatchers",
  "jobs.dispatcherSub":
    "Rows that carry no organization: a dispatcher fans work out to every organization and does none of its own. Their counts belong to the installation, not to you.",
  "jobs.dispatcherEmpty":
    "No dispatcher rows. The periodic ticks re-insert them, so an empty list means none is scheduled right now.",
  "jobs.count.waiting": "{count} waiting",
  "jobs.count.running": "{count} running",
  "jobs.count.retrying": "{count} retrying",
  "jobs.count.dead": "{count} dead",
  "jobs.queue": "queue {queue}",
  "jobs.waitedSeconds.one": "oldest has waited {count} second",
  "jobs.waitedSeconds.other": "oldest has waited {count} seconds",
  "jobs.waitedMinutes.one": "oldest has waited {count} minute",
  "jobs.waitedMinutes.other": "oldest has waited {count} minutes",
  "jobs.waitedHours.one": "oldest has waited {count} hour",
  "jobs.waitedHours.other": "oldest has waited {count} hours",
  "jobs.waitedDays.one": "oldest has waited {count} day",
  "jobs.waitedDays.other": "oldest has waited {count} days",
  "jobs.deadTitle": "Dead work needs a hand",
  "jobs.deadBody":
    "{count} jobs are discarded or cancelled: that work will not happen without intervention. A discarded job spent every attempt; a cancelled one was stopped deliberately. Read the failures below before re-queueing anything.",
  "jobs.failures": "Recent failures",
  "jobs.failuresSub":
    "Most recent first, capped at 50. A bounded list, not a log.",
  "jobs.failuresEmpty": "No failures recorded.",
  "jobs.state.retryable": "retrying",
  "jobs.state.discarded": "discarded",
  "jobs.state.cancelled": "cancelled",
  "jobs.attempt": "attempt {attempt} of {max} · {when}",
  "jobs.remedy": "What to do: {remedy}",
  "jobs.jobId": "job {id}",
  "jobs.failingSince": "failing since {when}",
  "jobs.reasonVetted":
    "Each reason, class and remedy is the job layer's own wording, never the worker's raw cause. A failure it cannot phrase reports a fixed substitute and carries no class at all. A class invented for text nobody could vet would key your alerts on a guess.",
  "jobs.generatedAt": "Read at {time}",

  "audit.you": "You",
  "audit.system": "System",
  "audit.unknownMember": "Unknown member",
  "audit.viaAgent": "via an agent",
  "audit.viaConnector": "via a connector",
  "audit.viaNamed": "via {client}",
  "audit.noHumanAuthority": "No human authority recorded",
  "settings.auditSub": "every action, attributed — human, agent, or connector",
  "settings.auditAdminOnly":
    "Only an admin can read the full trail. It records every actor and every record they touched, so it is not shown more widely.",
  "settings.auditFilters": "Filters",
  "settings.auditEntries": "Audit log",
  "settings.auditActor": "Actor",
  "settings.auditEntity": "Entity type",
  "settings.auditEntityId": "Entity id",
  "settings.auditAction": "Action",
  "settings.auditFrom": "From",
  "settings.auditTo": "To",
  "settings.auditExpand": "Show change detail",
  "settings.auditRule": "Authorization rule",
  "settings.auditOnBehalf": "on behalf of",
  "settings.privacy": "Privacy inbox",
  "settings.privacySub": "data-subject requests with their statutory deadlines",
  "settings.due": "due {date}",

  "privacy.addPurpose": "Add purpose",
  "privacy.purposesReadOnly":
    "Read-only view — only an admin or ops can add a purpose.",
  "privacy.purposeKey": "Key",
  "privacy.purposeLabel": "Label",
  "privacy.purposeDoi": "Requires double opt-in",
  "privacy.purposeCreate": "Create purpose",
  "privacy.purposeAppendOnly":
    "A purpose cannot be renamed or removed once created — the catalogue is append-only. Choose the key carefully.",
  "privacy.facetAll": "All",
  "privacy.inboxAdminOnly":
    "Only an admin can see subject requests. They name the people who asked, so the queue is not shown more widely.",
  "privacy.overdue": "Overdue",
  "privacy.closed":
    "Closed — a closed request never reopens. A new concern is a new request.",
  "privacy.assignee": "Assignee",
  "privacy.assigneeUnassignable":
    "Once set, an assignee cannot be cleared here.",
  "privacy.resolution": "Resolution",
  "privacy.resolutionRequired": "Closing a request needs its answer.",
  "privacy.movedOn":
    "This request moved on — someone else decided it first. Re-read below.",
  "privacy.inProgress": "In progress",
  "privacy.fulfil": "Fulfil",
  "privacy.reject": "Reject",
  "privacy.newRequest": "New request",
  "privacy.kind": "Kind",
  "privacy.person": "Person",
  "privacy.subjectRef": "Subject reference",
  "privacy.dueAt": "Due",
  "privacy.openRequest": "Open request",
  "privacy.erasureNeedsPerson":
    "An erasure request must name a person in this organization — fulfilling it erases that record. A free-text subject cannot be erased.",
  "privacy.accessManual":
    "An access request is fulfilled by hand: record what you sent in the resolution. This system does not assemble or export the data for you.",
  "privacy.fulfilErasureTitle": "Fulfil erasure request",
  "privacy.erasureIrreversible":
    "This permanently erases the person across the whole system — record, captured activity, and derived values. It cannot be undone. The erasure is itself audited.",
  "privacy.typeErase": "Type ERASE to confirm",
  "privacy.erasureConfirm": "Erase + suppress",
  "privacy.legalHold":
    "Blocked — legal hold. This person is inside a statutory retention window, so erasure does not win here (Art. 17(3)(b)). The block applies to every role, including admin — there is no override. The attempt was audited.",

  "restricted.title": "Restricted records",
  "restricted.sub":
    "what a statutory retention obligation is holding after an erasure — which record, why, and until when. The correspondence itself is not shown: it is restricted precisely so it is not read.",
  "restricted.withheld":
    "Only an admin or ops can see which records a statutory obligation is holding. It reads through the same authority as the retention ladder.",
  "restricted.empty":
    "No record is being held — every erasure so far could be completed in full.",
  "restricted.kind": "Record",
  "restricted.occurred": "Dated",
  "restricted.deals": "Transaction",
  "restricted.noDeal": "No deal on record",
  "restricted.reason": "Held because",
  "restricted.until": "Held until",
  "restricted.redacted": "Redacted",
  "restricted.nothingRedacted": "Nothing removed",
  "restricted.redactedCount": "{count} fields removed",
  "restricted.class.commercialCorrespondence": "Commercial correspondence",
  "restricted.kind.email": "Email",
  "restricted.kind.call": "Call",
  "restricted.kind.meeting": "Meeting",
  "restricted.kind.message": "Message",
  "restricted.decide": "Decision",
  "restricted.reasonLabel": "Why",
  "restricted.reasonHint":
    "Recorded in the audit trail with your name. This is what makes the decision accountable, so say what you decided and on what basis.",
  "restricted.release.action": "Release",
  "restricted.release.title": "Release this record from the retention floor?",
  "restricted.release.body":
    "Releasing ERASES the record. It does not put it back in use: the erasure request this obligation suspended is still outstanding, so lifting the obligation completes it. This cannot be undone.",
  "restricted.release.confirm": "Release and erase",
  "restricted.pin.action": "Pin a record",
  "restricted.pin.idHint":
    "For correspondence the automatic rule cannot recognise — supplier and purchasing mail qualifies under §257 HGB and has no deal in this product to hang off. The record id is on its audit entry.",
  "restricted.pin.idMalformed":
    "That is not a record id. It looks like 8-4-4-4-12 hexadecimal characters, and the audit entry for the record shows it in full.",
  "restricted.pin.idPlaceholder": "Record id",
  "restricted.pin.title": "Place this record under the retention floor?",
  "restricted.pin.body":
    "The record is held for the statutory window: hidden from every ordinary view, unchangeable, and erased when the window closes. Its identifiers are redacted now.",
  "restricted.pin.confirm": "Pin and hold",
  "retention.title": "Retention",
  "retention.sub":
    "how long each kind of record is kept, and what happens when its window runs out",
  "retention.retainOnly": "Retain-only posture",
  "retention.retainOnlyHelp":
    "While this is on, this installation destroys nothing: no anonymising and no erasing, whatever a policy below says. Archiving still runs — an archived record is kept, not destroyed.",
  "retention.adminOnly": "Only an admin or ops can change retention.",
  "retention.withheld":
    "Only an admin or ops can see the retention ladder. It sets what this installation keeps for everybody, so it is not shown more widely.",
  "retention.addPolicy": "Add policy",
  "retention.create": "Create policy",
  "retention.scope": "Applies to",
  "retention.window": "Window in days",
  "retention.windowDays": "{days} days",
  "retention.windowInvalid": "A window is a whole number of days, at least 1.",
  "retention.action": "Action",
  "retention.actionHint":
    "Archive keeps the record; anonymise and erase destroy data, and are the two the retain-only posture holds back.",
  "retention.lawfulBasis": "Lawful basis",
  "retention.lawfulBasisHint":
    "Optional. The Art. 6 basis this window is argued from, for the auditor reading the row.",
  "retention.enabled": "Enabled",
  "retention.edit": "Edit",
  "retention.save": "Save policy",
  "retention.delete": "Delete policy",
  "retention.deleteTitle": "Delete retention policy?",
  "retention.deleteBody":
    "This drops the rule for {scope} entirely, so nothing in that scope ages out any more. To pause the rule and keep its window, turn Enabled off instead.",
  "retention.duplicateScope":
    "A policy for this scope already exists — each scope carries at most one rule. Edit the existing row instead.",
  "retention.empty":
    "No retention policy yet — nothing in this installation ages out.",
  "retention.effectActing": "Acting nightly",
  "retention.effectSuppressed": "Suppressed by retain-only",
  "retention.effectDisabled": "Disabled",
  "retention.suppressedWhy":
    "Enabled, but the retain-only posture is holding it back: this rule destroys data, so it will not act until the posture is turned off.",
  "retention.disabledWhy":
    "Turned off and kept — its window is preserved, and nothing in this scope ages out while it is off.",
  "retention.actionArchive": "Archive",
  "retention.actionAnonymize": "Anonymise",
  "retention.actionErase": "Erase",
  "retention.scopeLeadUnconverted": "Leads that never converted",
  "retention.scopeActivity": "All captured activity",
  "retention.scopeActivityTranscript": "Call transcripts",
  "retention.scopePersonNoConsentNoDeal": "People with no consent and no deal",
  "retention.scopeDealLost": "Lost deals",
  "retention.scopeDealWon": "Won deals",
  "retention.scopeAiCallPayloadContent": "AI call payloads",

  "settings.pipelines": "Pipelines",
  "settings.pipelinesReadOnly":
    "Read-only view — you may not change pipelines or their stages.",
  "settings.pipelinesSub":
    "The stages a deal moves through, one ladder per pipeline.",
  "pipeline.new": "New pipeline",
  "pipeline.edit": "Edit pipeline",
  "pipeline.name": "Name",
  "pipeline.default": "Default",
  "pipeline.notDefault": "Not default",
  "pipeline.position": "Position",
  "stage.new": "New stage",
  "stage.edit": "Edit stage",
  "stage.name": "Name",
  "stage.semantic": "Semantic",
  "stage.winProb": "Win probability",
  "stage.semOpen": "Open",
  "stage.semWon": "Won",
  "stage.semLost": "Lost",
  "stage.remove": "Remove",
  "stage.removeConfirm": "Remove stage",
  "stage.removeTitle": "Remove this stage?",
  "stage.removeBody":
    "“{name}” leaves the pipeline and the stages after it move up. Past stage changes stay readable. Deals still sitting on it have to move first.",

  "ob.url": "Website",
  "ob.urlScheme": "https://",
  "ob.back": "Back",
  "ob.restoring": "Restoring your setup…",
  "ob.readManual": "Tell me yourself",
  "ob.coreIntroTitle": "First, I need to know your legal company.",
  "ob.coreIntroBody":
    "I need its legal name, address and VAT or register number. Then I learn what you sell and to whom.",
  "ob.coreLegalKicker": "I start with legal identity",
  "ob.corePathLabel": "What I'll learn",
  "ob.corePathLegal": "Legal identity",
  "ob.corePathOffer": "Offer",
  "ob.corePathCustomer": "Customers",
  "ob.coreReadingPage": "I'm reading",
  "ob.coreWebsiteTitle": "Which website should I read?",
  "ob.coreWebsiteBody":
    "I read the legal notice first, then your products, customers and positioning.",
  "ob.corePreparing": "I'm preparing to read {host}",
  "ob.coreLegalReading": "I'm reading the legal identity on {host}",
  "ob.coreLegalReadingBody":
    "I look for the legal notice, address and register or VAT number. Unstated details stay empty.",
  "ob.coreBusinessReading": "I'm learning how the business works",
  "ob.coreBusinessReadingBody":
    "I'm connecting products, customers and positioning to the exact public text that supports them.",
  "ob.coreReady": "I found {count} cited company details",
  "ob.corePartial": "I found {count} useful details, with some gaps",
  "ob.coreReadyBody":
    "Nothing is saved yet. Review the legal identity first, then the offer and customer.",
  "ob.coreDeferredBody": "I'll resume this read automatically.",
  "ob.coreFailedBody":
    "I could not read this site well enough, so I stopped instead of guessing. You can tell me yourself.",
  "ob.coreFindingsTitle": "What I found and can support",
  "ob.coreFindingsBody":
    "Every value carries the public wording behind it. What I cannot verify, I leave empty.",
  "ob.ai.identity": "Hi, I'm Margince",
  "ob.ai.role": "Your company research AI",
  "ob.ai.speaker": "M",
  "ob.ai.speakerName": "Margince",
  "ob.ai.ready": "I'm ready to research",
  "ob.ai.configured": "Configured AI",
  "ob.ai.modelsUsed": "Models used in this task",
  "ob.ai.route": "Task · tier · provider",
  "ob.ai.calls": "AI calls",
  "ob.ai.tokens": "Tokens",
  "ob.ai.latency": "Model latency",
  "ob.ai.estimatedCost": "Estimated provider cost",
  "ob.ai.partialEstimate": "Partial · unpriced usage exists",
  "ob.ai.awaitingModel": "Shown after my first model call",
  "ob.ai.notAvailableYet": "Not available yet",
  "ob.ai.runtimeUnavailable": "Runtime details unavailable",
  // The runtime disclosure is a chip you can open rather than a permanent
  // band: cost is stated WHILE it is being spent, but a reader deciding
  // whether a legal entity is right should not have to read a billing table
  // to get to it.
  "ob.ai.runtimeChip": "What is answering, and what it costs",
  "ob.ai.answeringNow": "What is answering right now",
  "ob.ai.runScope": "This run only. The full log is in Settings → AI.",
  "ob.ai.tier.localSmall": "local, fast",
  "ob.ai.tier.cheapCloud": "cloud, efficient",
  "ob.ai.tier.premium": "premium reasoning",
  "ob.ai.tier.frontier": "frontier reasoning",
  "ob.ai.tier.localLarge": "local, advanced",
  // The rail footer's plain-language line: the exact ids sit one click away
  // in the runtime chip's "Configured AI" row, so this says only what a
  // non-technical reader needs at a glance — how many models, and where.
  "ob.ai.summary.cloud.one": "1 model, running in the cloud",
  "ob.ai.summary.cloud.other": "{count} models, running in the cloud",
  "ob.ai.summary.local.one": "1 model, running locally",
  "ob.ai.summary.local.other": "{count} models, running locally",
  "ob.ai.summary.hybrid.one": "1 model, split between cloud and local",
  "ob.ai.summary.hybrid.other": "{count} models, split between cloud and local",
  "ob.ai.summary.development.one": "1 model, development mode",
  "ob.ai.summary.development.other": "{count} models, development mode",
  "ob.ai.summary.none": "No model configured yet",
  "ob.ai.summaryProviders.one": "1 provider configured",
  "ob.ai.summaryProviders.other": "{count} providers configured",
  "ob.ai.readFirst": "Start company setup before asking about it.",
  "ob.ai.liveArtifact": "Live, reviewable artifact",
  "ob.ai.companyKnowledge": "What I understand about your company",
  "ob.ai.companyKnowledgeBody":
    "Website evidence stays separate from our conversation. You decide what becomes company context.",
  "ob.ai.companyKnowledgeManualBody":
    "Your answers and my suggestions stay editable here. You decide what becomes company context.",
  "ob.ai.askPlaceholder":
    "Ask me about a finding, correct a detail, or tell me what I missed…",
  "ob.ai.send": "Send to Margince",
  "ob.ai.reviewBoundary":
    "I can suggest changes here. I only apply them to your draft when you approve.",
  "ob.ai.confirmBoundary":
    "Nothing becomes company context until you confirm this draft.",
  "ob.ai.confirmCompany": "Confirm and save company",
  "ob.ai.thinking": "I'm checking the dossier and preparing an answer…",
  "ob.ai.suggestedChanges": "Suggested changes to your draft",
  "ob.ai.applyChanges": "Apply to my draft",
  "ob.ai.applied": "Applied to draft",
  "ob.ai.finding": "cited finding",
  "ob.ai.findings": "cited findings",
  "ob.continueManual": "Tell me instead",
  "ob.readStatus.queued": "I'm getting ready",
  "ob.readStatus.deferred": "I'm waiting for AI budget",
  "ob.readStatus.reading": "I'm reading now",
  "ob.readStatus.ready": "I've finished reading",
  "ob.readStatus.partial": "I've finished with some gaps",
  "ob.readStatus.failed": "I need your help",
  "ob.readStatus.confirmed": "I've saved your choices",
  "ob.readStatus.abandoned": "I've stopped",
  "ob.pagesRead": "pages I've read",
  "ob.legalEntitiesFound": "legal entities I've found",
  "ob.coverageDetails": "What I covered and couldn't read",
  "ob.legalFoundTitle": "Legal entities I found",
  "ob.legalFoundBody":
    "Each block keeps the registered name, address and register or VAT number. Pick yours in the review.",
  "ob.legalEntity": "Legal entity",
  "ob.confirmWebsite":
    "I grounded this in {count} public pages. Edit anything; untouched values keep their evidence.",
  "ob.confirmManual":
    "You told me this directly, so I'll store your answers as human assertions.",
  "ob.legalTitle": "Which legal entity should I use?",
  "ob.legalSub":
    "I found several entities in the legal notice. Choose yours and I'll fill in its details.",
  "ob.factsTitle": "Other facts I found",
  "ob.factsSelected": "{selected} of {total} selected",
  "ob.factsSub":
    "Untick anything that should not become company context — up to 100 facts can be selected.",
  "ob.nowUnderstands": "I now understand",
  "ob.contextReady":
    "I can use this context for drafts, search, agents and Voice DNA, with sources attached.",

  // No step number: the rail decides how many stops a reader gets, so the
  // count belongs to ob.conv.scene.step, which reads it off the rail. A total
  // written into a string here can only ever disagree with the real one.
  "ob.s1.kick": "Confirm",
  "ob.s1.title": "Review what I learned about your company",
  "ob.s1.sub":
    "I filled only what I could support from your website. Please correct anything that is wrong.",
  "ob.s1.urlPlaceholder": "yourcompany.com",
  "ob.s1.identityLabel": "Legal organization",
  "ob.s1.offerLabel": "Products and offer",
  "ob.s1.customerLabel": "Customer",
  "ob.s1.salesLabel": "Positioning and sales context",
  "ob.s1.fieldRequired": "Required.",
  "ob.s1.requiredMissing": "Fill these in before you continue: {fields}",
  "ob.s1.saving": "Saving…",
  "ob.s1.saveFailed": "Couldn't save your company",
  "ob.s1.savedNote":
    "Saved to your organization. Change anything here and continue to save it again.",
  "ob.readGo": "Read my website",
  "ob.urlWillRead": "I'll read {host}",
  "ob.readFromSite": "read from site",
  "ob.failTitle": "I couldn't read enough from this website",

  "ob.manualChapterLegal": "Your legal organization",
  "ob.manualChapterOffer": "Products and offer",
  "ob.manualChapterCustomer": "Ideal customer",
  "ob.manualChapterSales": "How you sell",
  "ob.manualNext": "Next question",
  "ob.manualLater": "Add later",
  "ob.manualReview": "Review my answers",
  "ob.manualRequired": "Required to create a usable company profile",
  "ob.manualOptional": "Optional — leave it empty to add later",
  "ob.manual.display_name": "What name do customers know your company by?",
  "ob.manual.display_nameHint":
    "Use the familiar company or trading name shown throughout Margince.",
  "ob.manual.legal_name": "What is the full registered legal name?",
  "ob.manual.legal_nameHint":
    "Include the legal form, such as GmbH, Ltd, Inc. or AG. Add it later if it does not apply.",
  "ob.manual.registered_address": "What is the registered address?",
  "ob.manual.registered_addressHint":
    "Use the official address from the commercial register or legal notice.",
  "ob.manual.register_vat": "What are the register and VAT/UID numbers?",
  "ob.manual.register_vatHint":
    "Enter the identifiers exactly as issued. Leave this empty when none applies.",
  "ob.manual.industry": "Which industry is the company in?",
  "ob.manual.industryHint":
    "Choose the description your customers would immediately recognize.",
  "ob.manual.history": "Is there useful company history Margince should know?",
  "ob.manual.historyHint":
    "For example founding year, origin or an important change in the business.",
  "ob.manual.offer_summary": "What products or services do you sell?",
  "ob.manual.offer_summaryHint":
    "One or two concrete sentences are enough. This is the commercial explanation Margince will use.",
  "ob.manual.value_proposition": "What outcome does the offer create?",
  "ob.manual.value_propositionHint":
    "Explain the value customers receive, not only the product features.",
  "ob.manual.usp": "What makes customers choose you?",
  "ob.manual.uspHint":
    "Name the strongest meaningful difference from the alternatives.",
  "ob.manual.icp": "Who is your ideal customer?",
  "ob.manual.icpHint":
    "Describe the companies or people that benefit most — size, industry, situation or geography.",
  "ob.manual.buying_center": "Who evaluates, buys or approves the purchase?",
  "ob.manual.buying_centerHint":
    "List the typical roles and who has the final say.",
  "ob.manual.customer_pains": "What problems bring those customers to you?",
  "ob.manual.customer_painsHint":
    "Use the problems customers would describe in their own words.",
  "ob.manual.desired_outcomes": "What are they trying to achieve?",
  "ob.manual.desired_outcomesHint":
    "Describe the practical or business outcomes they care about.",
  "ob.manual.buying_intents": "What usually signals buying interest?",
  "ob.manual.buying_intentsHint":
    "For example a new initiative, hiring pattern, deadline or operational problem.",
  "ob.manual.common_objections": "What objections do you hear most often?",
  "ob.manual.common_objectionsHint":
    "Include the concerns that regularly slow down or stop a purchase.",
  "ob.manual.sales_motion": "How does a typical sale happen?",
  "ob.manual.sales_motionHint":
    "Describe the path from first conversation to decision, including trials or procurement if relevant.",

  "ob.field.display_name": "Company name",
  "ob.field.offer_summary": "What do you sell?",
  "ob.field.icp": "Ideal customer",
  "ob.field.buying_center": "Who buys",
  "ob.field.value_proposition": "Value proposition",
  "ob.field.usp": "What sets you apart",
  "ob.field.customer_pains": "Customer pains",
  "ob.field.desired_outcomes": "Desired outcomes",
  "ob.field.buying_intents": "Buying intents",
  "ob.field.common_objections": "Common objections",
  "ob.field.sales_motion": "Sales motion",
  "ob.field.legal_name": "Registered legal name",
  "ob.field.registered_address": "Registered address",
  "ob.field.register_vat": "Register / VAT ID",
  "ob.field.industry": "Industry",
  "ob.field.history": "Company history",

  "ob.s3.title": "Look what you've built —",
  "ob.s3.titleEm": "with nothing connected.",
  "ob.s3.sub":
    "Your organization knows your business and your voice. Connect your inbox and it fills itself.",
  "ob.s3.subNoVoice":
    "Your organization knows your business. Connect your inbox and it fills itself.",
  "ob.s3.cardProfile": "Business profile",
  "ob.s3.cardProfileBody":
    "Confirmed and saved to your company page. Fields read from your site keep their source.",
  "ob.s3.cardProfileSkippedBody":
    "Read from your site but not saved — you skipped the confirm step. Go back to confirm it.",
  "ob.s3.cardVoice": "Your writing voice",
  "ob.s3.cardVoiceBody":
    "Built from the corpus you just gave us. Drafts will sound like you from day one.",
  "ob.s3.cardVoiceSkippedBody":
    "You skipped this step, so drafts use a neutral starter voice. Build yours anytime in Settings.",
  "ob.s3.cardPipeline": "Sales pipeline",
  "ob.s3.cardPipelineBody":
    "The standard 7-stage B2B template, tuned to your industry. Empty until you connect your inbox.",
  "ob.s3.cardDraft": "A sample draft, in your voice",
  "ob.s3.cardDraftExample": "A sample draft",
  "ob.s3.cardDraftBody": "See it below.",
  "ob.s3.originLabel": "Where this pipeline comes from",
  "ob.s3.originBody":
    "The standard B2B stage template, tuned to your industry from the read. Empty until you connect. You approve what becomes a deal.",
  "ob.s3.stillNothing":
    "Still nothing connected. You're in control of when that changes.",

  "ob.s4.provGoogle": "Google",
  "ob.s4.provMicrosoft": "Microsoft",
  "ob.s4.provImap": "Any inbox (IMAP)",
  "ob.s4.microsoftBtn": "Allow access to my Microsoft",
  "ob.s4.microsoftHint":
    "Read-only mail access. You can disconnect any time from Settings.",
  "ob.s4.microsoftUnverified":
    'You may see an "unverified app" notice — that\'s this self-hosted install, not a third party.',
  "ob.s4.microsoftFailed": "The Microsoft connection didn't complete.",
  "ob.s4.connectOkTitle": "You're connected",
  "ob.s4.connectOkBody":
    "Your mailbox is linked. Capture begins on the next sync.",
  "ob.s4.connectVerifying": "Confirming the connection…",
  "ob.s4.connectLive": "Live and capturing",
  "ob.s4.connectConfirmFailed": "We couldn't confirm the connection.",
  "ob.s4.connectRetry":
    "Head to Settings → Connections to try connecting again.",
  "ob.s4.connectDenied": "You declined access — nothing was connected.",
  "ob.s4.googleBtn": "Allow access to my Gmail",
  "ob.s4.googleHint":
    "Reads your mail, and sends only what you approve. You grant it on Google's own screen, and can disconnect any time.",
  "ob.s4.googleUnverified":
    "If Google warns about an “unverified app”, choose Advanced → Continue. Nothing sends without your approval.",
  "dedupe.intro":
    "Pairs the capture pipeline flagged as likely the same person or company. Merging keeps both records' history; dismissing tells the system to never ask about this pair again.",
  "dedupe.loading": "Loading the review queue…",
  "dedupe.empty": "No duplicates waiting — the queue is clear.",
  "dedupe.confidence": "Match confidence:",
  "dedupe.field": "Field",
  "dedupe.signal": "Signal",
  "dedupe.signalAgree": "agree",
  "dedupe.signalCollide": "conflict",
  "dedupe.signalOneSided": "one side only",
  "dedupe.left": "Keep left",
  "dedupe.right": "Keep right",
  "dedupe.kindPerson": "Person",
  "dedupe.kindOrganization": "Company",
  "dedupe.kindLead": "Lead",
  "dedupe.mergeCta": "Merge into selected",
  "dedupe.notDuplicateCta": "Not a duplicate",
  "dedupe.decided": "Decision saved.",
  "dedupe.undoCta": "Undo",
  "dedupe.undone": "Pair re-opened.",
  "dedupe.dismissNote": "Dismiss",
  "backfill.title": "Import your mail history",
  "backfill.intro":
    "Choose how far back to import. You'll see the scope and estimated cost before anything runs — and you can skip this entirely.",
  "backfill.windowLabel": "Import window",
  "backfill.window3m": "3 months",
  "backfill.window6m": "6 months",
  "backfill.window12m": "12 months",
  "backfill.window24m": "2 years",
  "backfill.window60m": "5 years",
  "backfill.previewLoading": "Counting your mailbox…",
  "backfill.estimateMessages": "Messages in this window:",
  "backfill.estimateCost": "Estimated AI cost:",
  "backfill.estimateNote":
    "An estimate, not a bill — actual usage is metered and visible as it happens.",
  "backfill.startCta": "Start the import",
  "backfill.starting": "Starting…",
  "backfill.skip": "Skip the history import",
  "backfill.skippedNote":
    "No history imported. New mail is still captured from now on — you can start an import later from Settings.",
  "backfill.loading": "Checking import status…",
  "backfill.statusUnavailable":
    "The import status can't be read right now — capture itself keeps running.",
  "backfill.queuedTitle": "Import queued",
  "backfill.runningTitle": "Importing your mail history",
  "backfill.doneTitle": "History import complete",
  "backfill.errorTitle": "The import hit a problem",
  "backfill.cancelledTitle": "Import cancelled",
  "backfill.progressLabel": "Import progress",
  "backfill.countScanned": "Messages scanned",
  "backfill.statEmails": "Emails captured",
  "backfill.statPeople": "People",
  // The count is domains this run raised a company question for, not
  // companies created — a domain becomes one only if its site says so.
  "backfill.statCompanies": "Companies to check",
  "backfill.errorNote":
    "It will retry on its own; everything captured so far is kept.",
  "backfill.cancel": "Stop the import",
  "backfill.cancelledNote": "Stopped. Everything captured so far is kept.",
  "backfill.unsupportedNote":
    "This mailbox type can't be backfilled — only new mail is captured from now on.",
  "backfill.narrowingNote":
    "A wider window already ran for this mailbox; the import window can only be widened, not narrowed.",
  "backfill.staleUpdated": "Last updated {duration} ago — no recent progress.",

  // The units an installation composed, offered on the settings page that
  // already holds the kind of credential each one is configured with. The two
  // headings differ because the two pages mean different things — one is your
  // own account somewhere, the other is the installation's.
  "extUnits.user.title": "Your other accounts",
  "extUnits.user.sub":
    "Accounts this installation can connect on your behalf. Each one is yours alone — nobody else sees it, and disconnecting it affects only you.",
  "extUnits.workspace.title": "Installation add-ons",
  "extUnits.workspace.sub":
    "Add-ons this installation runs with one shared credential. What you set here applies to everybody.",

  // Connected inboxes (Settings → Connections): the "manage in Settings"
  // surface the onboarding copy promises.
  "connectors.title": "Connected inboxes",
  "mailSharing.title": "Email sharing",
  "mailSharing.sub":
    "Captured mail is readable by every colleague who can see the contact. On by default — it is what makes the pipeline shared.",
  "mailSharing.label": "Share captured mail with the team",
  "mailSharing.help":
    "Individual messages can be limited afterwards, and addresses or domains excluded up front.",
  "mailSharing.danger":
    "DANGER: Switching off email sharing will make usage of the CRM difficult. New mail will be visible only to the people on each message.",
  "mailSharing.save": "Save",
  "connectors.sub":
    "Mailboxes capturing into your CRM. Disconnect any one when you need to — already-captured records stay.",
  "connectors.loading": "Loading your connections…",
  "connectors.loadFailed": "Couldn't load your connections.",
  "connectors.empty": "No inbox is connected yet.",
  "connectors.provGmail": "Gmail",
  "connectors.provGcal": "Google Calendar",
  "connectors.provGraph": "Microsoft",
  "connectors.provImap": "IMAP mailbox",
  "connectors.statusConnected": "Capturing",
  "connectors.statusPending": "Pending — not yet confirmed live",
  "connectors.statusReauth": "Needs reconnect",
  "connectors.statusError": "Sync error",
  "connectors.statusDisconnected": "Disconnected",
  "connectors.cannotSend": "Capturing only — cannot send",
  "connectors.reconnectToSend":
    "Reconnect this mailbox to send from it. A mailbox connected before sending existed cannot be upgraded in place — the provider only grants sending on a fresh connection.",
  "connectors.lastSynced": "Last synced {at}",
  "connectors.neverSynced": "Waiting for the first sync",
  "connectors.nextCheck": "Next check ~{at}",
  "connectors.polled": "Polled on a schedule (no push subscription)",
  "connectors.pushRenewal": "Push renewal by {at}",
  "connectors.notConfigured":
    "Mail capture isn't configured in this deployment.",
  "connectors.reconnect": "Reconnect",
  "connectors.disconnect": "Disconnect",
  "connectors.disconnectTitle": "Disconnect this inbox?",
  "connectors.disconnectBody":
    "This will delete the credential we stored for this mailbox. Capture stops immediately; everything already captured stays in your CRM, and reconnecting will ask for permission again.",
  "connectors.disconnectBodyGoogleNote":
    "Google may still list Margince under your account's third-party access — remove it there if you want to revoke it fully.",
  "connectors.disconnectBodyMicrosoftNote":
    "Microsoft may still list Margince among your account's connected apps — remove it there if you want to revoke it fully.",
  "connectors.errRateLimited":
    "The provider is throttling us. Capture is running slower than usual; nothing is lost.",
  "connectors.errUnreachable":
    "We couldn't reach the provider. We'll keep retrying.",
  "connectors.errAuth":
    "The provider rejected our credentials. Reconnect to resume.",
  "connectors.errHistoryGone":
    "The provider's change history expired. The next sync re-anchors from a fresh point.",
  "connectors.errInternal":
    "Something went wrong on our side. We stopped rather than capture partial data.",
  "connectors.errUnknown":
    "Capture hit a problem we can't classify yet. We'll keep retrying.",

  // The OAuth return outcome (Task 2): the callback lands back on
  // #/settings/connections/{outcome} — a dismissible inline note driven by
  // that route segment, never a claim the server hasn't confirmed.
  "connectors.oauthOk": "Connected. Your mailbox is now capturing.",
  "connectors.oauthDenied": "You declined access — nothing was connected.",
  "connectors.oauthError":
    "The connection couldn't be completed — please try again.",
  // Two failures that "try again" would be wrong about: the provider refused
  // the grant (retrying the same way repeats it), and the provider's API isn't
  // enabled for this deployment (no user action can clear it).
  "connectors.oauthRejected":
    "The provider declined the connection. Make sure you accept every permission it asks for, then try connecting again.",
  "connectors.oauthMisconfigured":
    "This deployment can't complete that connection yet — the provider's API isn't enabled for it. An administrator needs to enable it; the server log names which API.",
  "connectors.dismissOutcome": "Dismiss",

  // The always-present "Add a connection" affordance (Task 1): the empty
  // state and the roster footer share the same not-yet-connected provider
  // buttons, so the copy that names them lives once here.
  "connectors.addConnection": "Add a connection",
  "connectors.googleSeparateNote":
    "Gmail and Google Calendar connect separately.",
  "connectors.providerNotConfigured":
    "{provider} isn't configured in this deployment.",

  // The inline IMAP connect form (Task 6): first-connect and reconnect for
  // the one credential provider, done in Settings instead of bouncing to
  // onboarding.
  "connectors.imapModalTitle": "Connect an IMAP mailbox",
  "connectors.imapHost": "IMAP server",
  "connectors.imapPort": "Port",
  "connectors.imapUsername": "Email address",
  "connectors.imapSecret": "App password",
  "connectors.imapMailbox": "Mailbox",
  "connectors.imapMaxMessages": "Messages per sync",
  "connectors.imapSecretHint":
    "Use an app-specific password. We seal it in the credential vault and read your mail on a schedule until you disconnect — disconnecting deletes it.",
  "connectors.imapSubmitCta": "Connect",
  "connectors.imapLoginRejected":
    "The mailbox rejected these credentials. Check host, email and app password.",
  "connectors.imapUnreachable": "The mail server could not be reached.",

  // The Telegram connector panel (Task 17, design §9.1-§9.2): one bot
  // connects for the whole workspace — no OAuth handshake, a BotFather
  // token submitted through the same inline-form shape the IMAP connector
  // uses. Unlike the mail providers, the connection stays editable in
  // place: replacing the token goes through PATCH, never a disconnect.
  "connectors.provTelegram": "Telegram",
  "connectors.telegramTitle": "Telegram bot",
  "connectors.telegramSub":
    "One bot receives and sends messages for the whole organization.",
  "connectors.telegramNotConfigured":
    "Messaging channels aren't configured in this deployment.",
  "connectors.telegramConnectCta": "Connect a Telegram bot",
  "connectors.telegramEditToken": "Replace token",
  "connectors.telegramDisconnectTitle": "Disconnect this bot?",
  "connectors.telegramDisconnectBody":
    "This deletes the stored token and stops checking the bot for new messages. Capture and sending stop immediately; everything already captured stays in your CRM.",
  "connectors.telegramModalTitle": "Connect a Telegram bot",
  "connectors.telegramEditTitle": "Replace the bot token",
  "connectors.telegramBotToken": "Bot token",
  "connectors.telegramBotTokenHint":
    "Paste the token BotFather gave you when you created the bot. We seal it in the credential vault and never show it again.",
  "connectors.telegramSubmitCta": "Connect",
  "connectors.telegramReplaceCta": "Replace token",
  "connectors.telegramConnectedAs": "Connected as @{username}.",

  // The workspace's own consumer-mail list (CAP-PARAM-5): what the shipped
  // baseline missed, and what it got wrong. Admin-curated and shared, because
  // whether a domain can name a company is a fact about the domain.
  "consumerMail.title": "Consumer mail domains",
  "consumerMail.sub":
    "Mail from a consumer mailbox still creates the person — it just never creates a company. Margince ships a list of these providers; add what it missed, or take back a domain it wrongly claimed.",
  "consumerMail.domainLabel": "Domain",
  "consumerMail.domainPlaceholder": "provider.example",
  "consumerMail.kindLabel": "What this domain is",
  "consumerMail.kind.extra": "Consumer mail — never a company",
  "consumerMail.kind.never": "A real company — ignore the shipped list",
  "consumerMail.add": "Add",
  "consumerMail.remove": "Remove",
  "consumerMail.none": "Nothing added. The shipped list decides every domain.",
  "consumerMail.adminOnly": "You do not have permission to change this list.",
  "consumerMail.addOnly":
    "You can add consumer-mail domains. Overriding the shipped list and removing entries need an admin.",
  "consumerMail.baselineTitle": "Shipped list",
  "consumerMail.baselineCount":
    "Margince ships with {total} known consumer-mail domains.",
  "consumerMail.baselineSearchLabel": "Search the shipped list",
  "consumerMail.baselinePlaceholder": "gmail.com",
  "consumerMail.baselineNone": "No shipped domain matches.",
  "consumerMail.baselineMore":
    "Showing the first {shown} of {matched} matches.",

  "blockedDomains.title": "Refused domains",
  "blockedDomains.sub":
    "Which domains this installation refuses a company, and what decided each one — a model verdict, a heuristic, or a person. Letting a domain back in re-opens the company question rather than merely clearing a flag.",
  "blockedDomains.domainLabel": "Domain",
  "blockedDomains.domainPlaceholder": "vendor.example",
  "blockedDomains.admissionLabel": "Decision",
  "blockedDomains.admission.suppressed": "Never a company",
  "blockedDomains.admission.admitted": "Allowed, and kept",
  "blockedDomains.reasonLabel": "Why",
  "blockedDomains.reasonHint":
    "One sentence somebody reviewing this later can act on.",
  "blockedDomains.reasonPlaceholder": "a tool we use, not a customer",
  "blockedDomains.save": "Save decision",
  "blockedDomains.stored": "Stored: {domain} — {admission}.",
  "blockedDomains.adminOnly":
    "Only an admin or ops seat may change a domain decision. The list itself is yours to read.",
  "blockedDomains.none":
    "No domain has been refused a company yet. Bulk-sender verdicts land here on their own, and so does anything you refuse by hand.",
  "blockedDomains.unit": "domain decisions",
  "blockedDomains.openCompany": "the company",
  "blockedDomains.col.domain": "Domain",
  "blockedDomains.col.admission": "Decision",
  "blockedDomains.col.source": "Decided by",
  "blockedDomains.col.reason": "Why",
  "blockedDomains.col.decided": "When",
  "blockedDomains.col.revise": "Change",
  "blockedDomains.source.verdict": "A model verdict",
  "blockedDomains.source.heuristic": "A heuristic",
  "blockedDomains.source.human": "A person",
  "blockedDomains.rowAdmit": "Allow this one",
  "blockedDomains.rowRefuse": "Refuse this one",

  "ob.s4.googleFailed": "The Google connection didn't complete",
  "ob.s4.imapHost": "IMAP host",
  "ob.s4.imapHostPlaceholder": "imap.gmail.com",
  "ob.s4.imapPort": "Port",
  "ob.s4.imapEmail": "Email",
  "ob.s4.imapPassword": "App password",
  "ob.s4.imapMailbox": "Mailbox",
  "ob.s4.imapMax": "How many recent emails",
  "ob.s4.imapHint":
    "Use an app password. We store it encrypted, and disconnecting deletes it.",
  "ob.s4.imapConnect": "Test and connect",
  "ob.s4.connecting": "Connecting securely…",
  "ob.s4.accessToggle": "What access this gives",
  "ob.s4.scope1Lead": "We read — we don't clutter.",
  "ob.s4.scope1Rest":
    "Your mail becomes contacts, companies and activities, captured automatically.",
  "ob.s4.scope2Lead": "We never send anything without your approval.",
  "ob.s4.scope2Rest": "Drafts wait in your approval inbox.",
  "ob.s4.scope3Lead": "Your data stays in your organization.",
  "ob.s4.scope3Rest": "Own-your-data — export or delete everything anytime.",
  "ob.s4.scope4Lead": "Disconnect in one click.",
  "ob.s4.scope4Rest": "The CRM keeps working; it just stops capturing.",
  "ob.s4.capturedTitle": "Mailbox connected",
  "ob.s4.capturedBody":
    "Your CRM is building itself. New mail lands here as the first sweep runs, usually in minutes.",
  "ob.s4.enterCrm": "Enter your CRM",
  "ob.s4.connectFailed": "Couldn't connect that mailbox",
  "ob.s4.notNow": "Not now",

  "ob.conv.threadLabel": "Onboarding conversation",
  "ob.conv.welcome":
    "Hi, I am Margince. I set up your CRM from what already exists, and I show a source for everything.",
  "ob.conv.welcomeMember":
    "Hi, I am Margince. Your team is already set up. Two short steps and you are in.",
  "ob.conv.read.started": "Reading {host} now. I will tell you what I find.",
  "ob.conv.read.pages": "Pages read so far: {pages}.",
  "ob.conv.read.learnedField": "Learned {field}: {value}",
  "ob.conv.read.extracting":
    "Done crawling. Now extracting what the site says about your business.",
  "ob.conv.read.warning": "Heads up: {warning}",
  "ob.conv.read.failed":
    "I could not read that site. Try another URL, or tell me directly.",
  "ob.conv.read.deferred":
    "The read is paused for now. I will pick it up again automatically.",
  "ob.conv.read.pollFailed":
    "I lost the connection while reading. What I already found is kept.",
  "ob.conv.clarify.entity":
    "The site names more than one legal entity. Which one is this installation for?",
  "ob.conv.company.confirmed":
    "Company profile confirmed. Everything I stored carries its source.",
  "ob.conv.manual.chosen": "I will type it in myself.",
  "ob.conv.voice.skipped": "Skip voice for now.",
  "ob.conv.voice.uploadAdded": "Added {name}.",
  "ob.conv.voice.speakerQuestion":
    "This transcript has several speakers. Which one is you? Only your own words count.",
  "ob.conv.voice.speakerOptionDetail": "words: {words} · turns: {turns}",
  "ob.conv.voice.guideSpeaker":
    "A speaker choice is waiting on the right — pick which one is you.",
  "ob.conv.voice.speakerFoot": "Your choice applies to this file only.",
  "ob.conv.voice.speakerContinue": "Use this speaker",
  "ob.conv.voice.continueSkippedStatus":
    "Skipped for now — add it later in Settings.",
  "ob.conv.voice.continueFailedStatus":
    "Your material is safe — retry now, or continue and pick this up later.",
  "ob.conv.voice.continueDeferredStatus":
    "No action needed here — continue, and it finishes on its own.",
  "ob.conv.voice.collectAsk":
    "Send me things you wrote. Call transcripts work best; plain documents work too.",
  "ob.conv.voice.composer": "Paste the text you wrote here",
  "ob.conv.voice.dropHint":
    "You can also drop files anywhere in this conversation.",
  "ob.conv.voice.fileSkipped":
    "I cannot read {name}. I take .txt, .md, .vtt, .srt, or .json.",
  "ob.conv.voice.fileEmpty":
    "There are no words in {name}, so nothing was counted.",
  "ob.conv.voice.reactionTranscript":
    "Words kept: {kept} of {total}. Only your turns count; speech sharpens your voice most.",
  "ob.conv.voice.reactionDocument":
    "Words counted: {words}. Every word here is yours, so all of them count.",
  "ob.conv.voice.refusalUnattributed":
    "That looks like a conversation, but I cannot tell which words are yours, so I counted none.",
  "ob.conv.voice.refusalSpeaker":
    "I could not find that speaker in the transcript. Nothing was counted.",
  "ob.conv.voice.refusalUnsupported":
    "I could not parse that file as text or a transcript. Nothing was counted.",
  "ob.conv.voice.ingestFailed": "I could not add that source: {detail}",
  "ob.conv.voice.ingestUnexpected":
    "I could not add that source. Try again in a moment.",
  "ob.conv.voice.pasteAdd": "Yes, add it to my corpus.",
  "ob.conv.voice.pasteDiscard": "No, discard it.",
  "ob.conv.voice.pasteSource": "Pasted text",
  "ob.conv.voice.buildFloor":
    "Own words so far: {words}. I need at least {min} before I can build.",
  "ob.conv.voice.buildNudge":
    "I have enough to build. More helps: 4,000 words or more sharpen your voice noticeably.",
  "ob.conv.voice.buildChip": "Build my voice profile",
  "ob.conv.voice.retryBuild": "Try the build again",
  "ob.conv.voice.buildPollFailed":
    "I lost the connection during the build. Your texts are kept; try the build again.",
  "ob.conv.voice.statusBuilding": "Building your voice profile",
  "ob.conv.voice.resultTitle": "Here is your voice, in your own words.",
  "ob.conv.voice.resultLoading": "Loading what the build learned.",
  "ob.conv.voice.resultEmpty":
    "The build finished, but there is nothing to show yet. You can review it in Settings.",
  "ob.conv.voice.candidateNote":
    "This version needs your review before it goes live. You can approve it in Settings.",
  "ob.conv.voice.artifactTitle": "Voice corpus",
  "ob.conv.voice.artifactBody":
    "Only your own words count here. Every number comes from the server after speaker filtering.",
  "ob.conv.voice.artifactEmpty":
    "Nothing collected yet. Attach a transcript or a text you wrote.",
  "ob.conv.voice.meterWords": "Own words: {words} of {target}",
  "ob.conv.voice.meterBand": "Quality: {band}",
  "ob.conv.voice.manifestKept": "Kept {kept} of {total} words",
  "ob.conv.voice.manifestWords": "{words} words",
  "ob.conv.voice.registerMix": "Registers: {mix}",
  "ob.conv.voice.stageTitle": "Build progress",
  "ob.conv.corpus.words": "Own words in your corpus now: {words}.",
  "ob.conv.corpus.band": "Corpus quality moved to {band}.",
  "ob.conv.build.snapshot": "Locking in your corpus.",
  "ob.conv.build.extract": "Finding your signature moves.",
  "ob.conv.build.evaluate": "Testing drafts against held-out samples.",
  "ob.conv.build.activate": "Activating your voice profile.",
  "ob.conv.build.succeeded": "Your voice profile is ready.",
  "ob.conv.build.deferred":
    "The build is queued behind budget. It will run automatically.",
  "ob.conv.build.failed":
    "The build did not finish. Your texts are kept and you can retry anytime.",
  "ob.conv.recap":
    "Here is what your CRM knows now, with a source for every item.",
  "ob.conv.consent":
    "Last step: what may I capture, and for which purpose? Nothing is on by default.",
  "ob.conv.done": "Setup complete. Your CRM is ready.",
  "ob.conv.clarify.question": "{question}",
  "ob.conv.clarify.optionDetail": "{detail}",
  "ob.conv.clarify.dismiss": "Skip this - I will set it myself",
  "ob.conv.clarify.keepMine": "Keep my value",
  "ob.conv.review.skipped": "You skipped: {fields}. Edit them any time.",
  "ob.conv.clarify.applyFailed":
    "I could not record that choice: {detail} Pick it again.",
  "ob.conv.clarify.applyMissing":
    "The server did not confirm that choice. Pick it again.",
  "ob.conv.loadFailed": "I could not check your setup. Please try again.",
  "ob.conv.retry": "Try again",
  "ob.conv.connect.persistFailed": "I could not record the finish. Try again.",
  "ob.conv.review.title": "Here is everything I found. Correct me.",
  "ob.conv.review.showLess": "Show less",
  "ob.conv.review.continue": "Continue",
  "ob.conv.review.progressLabel": "Required fields completed",
  "ob.conv.review.requiredRemaining.one":
    "{count} field needed before you can continue",
  "ob.conv.review.requiredRemaining.other":
    "{count} fields needed before you can continue",
  "ob.conv.review.requiredDone": "Nothing more needed — you can continue.",
  "ob.conv.review.confirmQuestionOpen":
    "A decision is still open. Answer it to continue.",
  "ob.conv.triage.stateRequired": "required, still empty",
  "ob.conv.triage.stateEmpty": "empty",
  "ob.conv.triage.stateTyped": "typed by you",
  "ob.conv.triage.stateStored": "from your profile",
  // A value the entity census read off the legal notice — the one company the
  // site names, or the candidate the human picked from several. Nothing ever
  // scored it, so the word names WHERE it came from; "chosen by you" would be
  // false on the sole-candidate path, where nobody was asked anything.
  "ob.conv.triage.stateQuoted": "read from your legal notice",
  // Where a value would stand on an empty row. It says only that the row is
  // empty: the same line serves the manual path, where nothing ever read the
  // site, and a read-backed board, where the wire says nothing about why any
  // one field came back missing. Naming a cause here would invent one.
  "ob.conv.triage.emptyHint": "Nothing here yet. Yours to add.",
  "ob.conv.triage.legalNotPublished":
    "Not stated on your legal or imprint page. Yours to add.",
  "ob.conv.triage.legalNotChecked":
    "I did not find a legal or imprint page on your site to check. Yours to add.",
  // Several companies stand on the one imprint. The value IS on the page, so
  // saying it is not stated would be false; what is missing is the human's
  // decision about which company this installation belongs to.
  "ob.conv.triage.legalUnpicked":
    "Your imprint names more than one company. Choose which one is yours and I will fill this in.",
  // The omission notice on an empty row, once a read has actually run: the
  // field is named as withheld rather than left blank, and the reason is only
  // ever one the read can support for THAT field. The read's crawl-wide
  // warnings belong to the coverage card, which states each under its own
  // heading; beside a field they would name a cause the read never gave.
  "ob.conv.triage.omittedLabel": "Omitted, not guessed",
  "ob.conv.triage.omittedField": "{field}: {reason}",
  "ob.conv.triage.mapLabel": "Jump to a section",
  "ob.conv.triage.sectionBlocking": "{count} needed to continue",
  "ob.conv.triage.sectionAdvisory": "{count} worth a check",
  "ob.conv.triage.blockingHead": "Needed to continue",
  "ob.conv.triage.advisoryHead": "Worth a check",
  "ob.conv.triage.sectionSettled": "Nothing outstanding here",
  "ob.conv.triage.sectionMore": "+{count} more",
  "ob.conv.triage.restTitle": "Background, not work",
  "ob.conv.triage.looksSolid": "Looks solid · {count}",
  "ob.conv.triage.companyWebsite": "Website",
  "ob.conv.triage.sourceCount": "{count} src",
  "ob.conv.triage.peopleLabel": "People",
  "ob.conv.triage.peopleCount": "{count} found",
  "ob.conv.triage.peopleEmpty": "No people found on your site.",
  "ob.conv.triage.factsLabel": "Facts",
  "ob.conv.triage.factsCount": "{count} found",
  "ob.rail.spend": "Tokens this setup",
  "ob.rail.tokensUnit": "tok",
  "ob.conv.scene.step": "Step {n} of {m} · {label}",
  "ob.conv.scene.detour": "A quick detour",
  "ob.conv.scene.decisionSub":
    "Your site names several legal entities. The one you pick goes on every quote and invoice.",
  "ob.conv.scene.continue": "Continue",
  "ob.conv.scene.candidates": "{count} candidates",
  "ob.conv.connect.sceneTitle": "Connect your accounts.",
  "ob.conv.connect.sceneSub":
    "I build your contacts, companies and history from what is already in your inbox.",
  "ob.conv.connect.mailboxTitle": "Your mailbox",
  "ob.conv.connect.mailboxHint":
    "Pick one. This is where your contacts, companies and history come from.",
  "ob.conv.connect.networkTitle": "Your network",
  "ob.conv.connect.networkHint":
    "Optional but worth it. Turns who you know into accounts and watches them for triggers.",
  "ob.conv.connect.required": "required",
  "ob.conv.connect.recommended": "recommended",
  "ob.conv.connect.gmailBrings": "Mail, contacts and calendar from Google",
  "ob.conv.connect.microsoftBrings":
    "Mail, contacts and calendar over the Graph API",
  "ob.conv.connect.imapBrings": "Any other mail host, with an app password",
  "ob.conv.connect.linkedinAuth": "Profile link, read only",
  "ob.conv.connect.scopeGoogle": "OAuth, read and send scopes",
  "ob.conv.connect.scopeMicrosoft": "OAuth, Graph API",
  "ob.conv.connect.scopeImap": "Any other provider, app password",
  "ob.conv.connect.connectCta": "connect →",
  "ob.conv.connect.connectedCta": "connected",
  "ob.conv.connect.blockedCard":
    "You already picked a mailbox. Disconnect it in Settings to switch.",
  "ob.conv.connect.guaranteesToggle": "What connecting actually does",
  "ob.conv.connect.railPromise":
    "We only read, and nothing sends without your approval.",
  "ob.conv.connect.dialogHeadlineAccess": "{name} access needed",
  "ob.conv.connect.dialogHeadlineImap": "Connect your mail host",
  "ob.conv.connect.dialogIntro":
    "{brings}. I read it once to build your contacts and history, then keep it in sync.",
  "ob.conv.connect.dialogClose": "Close",
  "ob.conv.connect.linkedinName": "LinkedIn",
  "ob.conv.connect.linkedinConnected": "Connected",
  "ob.conv.connect.linkedinSkippedNote": "Skipped: add it later in Settings",
  "ob.conv.connect.rosterFailedTitle": "Could not check your mailboxes",
  "ob.conv.connect.rosterFailedBody":
    "Something went wrong loading your connection status. Try again before picking a provider.",
  "ob.conv.voice.sceneTitle": "Teach me how you write.",
  "ob.conv.voice.sceneSub":
    "This CRM drafts every email in your own words, and nothing sends until you approve it.",
  "ob.conv.voice.heroBody":
    "It learns your tone, rhythm and phrasing from your own writing, and from nobody else's.",
  "ob.conv.voice.whyToggle": "Why this matters",
  "ob.conv.voice.dropTitle": "Drop your writing here",
  "ob.conv.voice.dropSub":
    "Sent mail works best, because it shows how you write when you want something.",
  "ob.conv.voice.browse": "Browse files",
  "ob.conv.voice.pasteInstead": "Paste text instead",
  "ob.conv.voice.sourcesTitle": "Sources",
  "ob.conv.voice.meterLabel": "Progress toward the {min}-word minimum",
  "ob.conv.voice.meterProgress": "{words} of {min} words",
  "ob.conv.voice.meterReady":
    "{words} words — enough to build. More still sharpens it.",
  "ob.conv.voice.footReady":
    "Training takes about a minute. You will see a sample before anything is saved.",
  "ob.conv.voice.footFloor":
    "{min} words minimum. Below that the model just copies phrasing.",
  "ob.conv.voice.buildingTitle": "Learning your voice",
  "ob.conv.voice.buildingMeta": "{words} words, {sources} sources",
  "ob.conv.voice.resultSub":
    "Read the sample first. If it lands, confirm. If it is off, add more sources and I rebuild.",
  "ob.conv.voice.resultSubNoSample":
    "Your corpus is too small for a sample draft. Here is what the build learned. Add more sources.",
  "ob.conv.voice.resultContinue": "That is my voice",
  "ob.conv.voice.sampleEyebrow": "Sample, not sent",
  "ob.conv.voice.sampleAnother": "Another scenario",
  "ob.conv.voice.sampleSubjectLabel": "Subject",
  "ob.conv.voice.sampleWhyTag": "Why",
  "ob.conv.voice.dimensionsTitle": "Measured dimensions",
  "ob.conv.voice.dimensionsCount": "Measured: {count}",
  "ob.conv.voice.dimSentenceName": "Sentence length",
  "ob.conv.voice.dimSentencePoleLow": "Terse",
  "ob.conv.voice.dimSentencePoleHigh": "Elaborate",
  "ob.conv.voice.dimSentenceMeasured": "Measured",
  "ob.conv.voice.dimSentenceEvidence": "{count} words per sentence on average.",
  "ob.conv.scene.evidence": "evidence",
  "ob.conv.scene.hideEvidence": "hide evidence",
  "ob.conv.scene.whyThis": "What I read",
  "ob.conv.scene.foundOn": "Found on",
  "ob.conv.guide.decision":
    "I need one decision from you: {question} It is on the right, with the evidence for each option.",
  "ob.conv.guide.reviewBlocked.one":
    "Your review is ready on the right. {count} field blocks confirm.",
  "ob.conv.guide.reviewBlocked.other":
    "Your review is ready on the right. {count} fields block confirm.",
  "ob.conv.guide.reviewAdvisory.one":
    "Your review is ready on the right. Nothing blocks you; {count} thing is worth a look.",
  "ob.conv.guide.reviewAdvisory.other":
    "Your review is ready on the right. Nothing blocks you; {count} things are worth a look.",
  "ob.conv.guide.reviewClean":
    "Your review is ready on the right. It looks clean, check what you want and confirm when ready.",
  "ob.conv.guide.attentionHeading": "These need your input",
  "ob.conv.guide.attentionGroup.blocking": "Needed before you can continue",
  "ob.conv.guide.attentionGroup.decisions": "Needs a decision",
  "ob.conv.guide.attentionGroup.advisory": "Worth a look",
  "ob.conv.guide.attentionStatus.blocks": "needed to continue",
  "ob.conv.guide.attentionStatus.empty": "still empty",
  "ob.conv.guide.attentionStatus.decision": "needs a decision",
  "ob.conv.guide.attentionStatus.check": "worth a check",
  "ob.conv.activity.steps": "{count} steps",
  "ob.conv.showField": "Show me",
  "ob.conv.review.editDirectly": "Edit fields directly",
  "ob.conv.review.backToDossier": "Back to the dossier",
  "ob.conv.review.proposalFallback":
    "I could not load the prepared mapping. Review what I read directly; every field keeps its source.",
  "ob.conv.review.confirmFailed":
    "I could not save that yet: {detail} Fix it and accept again.",
  "ob.conv.review.confirmVersionSkew":
    "Your review just picked up newer information. Have a look, then press Continue again.",
  "ob.conv.review.confirmVersionSkewStuck":
    "Nothing has changed yet, so Continue would fail again. Have another look, or try in a moment.",
  "ob.conv.review.confirmNotReady":
    "This read has no draft to confirm yet. Check again when it finishes, or start a fresh read.",
  "ob.conv.review.confirmCheckFailed":
    "This read is confirmed, but I could not load the company it created. Try again shortly.",
  "ob.conv.artifact.empty":
    "Nothing read yet. Give me a website and this panel fills with sourced findings.",
  "ob.conv.results.continue": "Continue",
  "ob.conv.results.artifactTitle": "Setup recap",
  "ob.conv.results.artifactBody":
    "What your CRM starts with. Nothing here claims more than what actually happened.",
  "ob.conv.results.company":
    "Company profile confirmed for {name}. Everything stored carries its source.",
  "ob.conv.results.companyUnsaved":
    "Your company details are not saved yet. You can complete them later in Settings.",
  "ob.conv.results.voiceBuilt":
    "Your voice profile is built. Drafts will sound like you.",
  "ob.conv.results.voiceSkipped":
    "No voice profile yet. Drafts use a neutral starter voice, and you can build yours later in Settings.",
  "ob.conv.recap.back": "Welcome back. Here is where we stand.",
  "ob.conv.recap.company": "Your company profile for {name} is confirmed.",
  "ob.conv.recap.companyUnsaved":
    "Your company details are not saved yet. You can complete them in Settings.",
  "ob.conv.recap.voiceBuilt":
    "Your voice profile is built. Drafts can sound like you.",
  "ob.conv.recap.voiceSkipped":
    "You skipped the voice profile. Drafts use a neutral starter voice.",
  "ob.conv.recap.corpus":
    "Your corpus already holds {words} of your own words.",
  "ob.conv.recap.readTerminal":
    "Welcome back. I finished reading {host}: {count} findings with sources, ready below.",
  "ob.conv.recap.readReading":
    "Welcome back. I am still reading {host}. Pages so far: {pages}.",
  "ob.conv.recap.readFailed":
    "Welcome back. My earlier read of {host} did not finish. Give me a website again, or tell me directly.",
  "ob.conv.recap.readDeferred":
    "Welcome back. My read of {host} is paused for now. Give me a website again, or tell me directly.",
  "ob.conv.connect.pick":
    "Pick a provider to see exactly what connecting does, or skip and connect later in Settings.",
  "ob.conv.connect.skip": "Skip connecting for now",
  "ob.conv.linkedin.cardBody":
    "Turns your network into accounts and contacts, and flags it when a connection changes jobs.",
  "ob.conv.linkedin.limitsToggle": "What Margince can and cannot see",
  "ob.conv.linkedin.scope1Lead": "Your connection list \u2014",
  "ob.conv.linkedin.scope1Rest":
    "name, position, company and the date you connected.",
  "ob.conv.linkedin.scope2Lead": "Nothing else.",
  "ob.conv.linkedin.scope2Rest":
    "No messages, no posts, no who-viewed-you, no activity.",
  "ob.conv.linkedin.scope3Lead": "Your network stays yours.",
  "ob.conv.linkedin.scope3Rest":
    "It is attributed to you, never to the company, and disconnecting removes it.",
  "ob.conv.linkedin.scope4Lead": "Nobody is contacted.",
  "ob.conv.linkedin.scope4Rest":
    "Connecting sends no invitations and no messages, ever.",
  "ob.conv.linkedin.neverContacts":
    "Your connections never become contacts. They answer one question: who here already knows them?",
  "ob.conv.linkedin.profileLabel": "Your LinkedIn profile URL",
  "ob.conv.linkedin.profilePlaceholder": "https://www.linkedin.com/in/…",
  "ob.conv.linkedin.profileWhy":
    "So the network is attributed to you: “Anna knows them”, never “the company knows them”.",
  "ob.conv.linkedin.authorize": "Authorize with LinkedIn",
  "ob.conv.linkedin.appPending":
    "Our LinkedIn app is still awaiting approval, so nothing syncs yet. Upload Connections.csv in Settings.",
  "ob.conv.linkedin.skip": "Skip LinkedIn for now",
  "ob.conv.linkedin.connected":
    "LinkedIn authorized. Your connections will sync as soon as the app is approved.",
  "ob.conv.linkedin.skipped":
    "Skipped LinkedIn. You can connect it any time in Settings.",

  // The setup rail: five stops, one word each. Long enough to name the step,
  // short enough that five of them fit a column at 10px.
  "ob.rail.read": "Read",
  "ob.rail.confirm": "Confirm",
  "ob.rail.voice": "Voice",
  "ob.rail.ready": "Ready",
  "ob.rail.connect": "Connect",

  // --- the gate: the first screen after sign-in -------------------------
  // One question and nothing else. Nobody should meet the whole tool on their
  // first screen, so the gate names what it will do, what it costs the reader
  // (two minutes), and who decides (they do) — then asks once.
  "ob.gate.title": "Hi {name}, I am the Margince AI.",
  "ob.gate.titleAnonymous": "I am the Margince AI.",
  "ob.gate.sub":
    "I read your site and draft your company profile. You approve before anything is saved. About two minutes.",
  "ob.gate.trustToggle": "How this works",
  "ob.gate.trustBody":
    "I read only public pages. Nothing is saved until you confirm it, and nothing is ever sent without your approval.",
  "ob.gate.field": "Your website address",
  "ob.gate.placeholder": "yourcompany.com",
  "ob.gate.submit": "Read my site",
  "ob.gate.altPrompt": "No website handy?",
  "ob.gate.altAction": "Enter the details yourself",
  "ob.gate.invalidUrl":
    "That does not look like a web address. Try it as yourcompany.com.",
  // One string for two failures that look identical to the reader: the request
  // to start never landed, or the read started and did not finish. {detail} is
  // the server's own guidance and may be empty, so the sentence has to stand
  // without it.
  "ob.gate.startFailed":
    "I could not read that site. {detail} Try another address, or enter the details yourself.",
  // A deferred read is shelved, not broken: the server will come back to it. So
  // this says what is true and names both doors, without asking the reader to
  // fix anything.
  "ob.gate.readPaused":
    "That read is paused. {detail} It resumes on its own — or give me another address, or type it in.",

  // --- the read theatre -------------------------------------------------
  // Volume made visible. The wire gives no page-count denominator, so every
  // number here is an open count — never "14 of 18", never a bar with a known
  // end, because inventing the total would be inventing data.
  "ob.scan.title": "Reading {host}",
  "ob.scan.sub":
    "Every fact keeps the page it came from, so you can check anything I claim.",
  "ob.scan.doneTitle": "Read {host}",
  "ob.scan.doneSub":
    "{facts} facts and {fields} profile fields, each with the page it came from. Opening your review.",
  "ob.scan.phaseCrawling": "Fetching pages",
  "ob.scan.phaseExtracting": "Working out what you sell",
  "ob.scan.phaseQueued": "Queued, starting shortly",
  "ob.scan.phaseDeferred": "Paused for now",
  "ob.scan.pagesRead": "{pages} pages read",
  "ob.scan.pagesSkipped": "{count} skipped",
  "ob.scan.factsSoFar": "{count} facts so far",
  "ob.scan.stillReading": "still reading",
  "ob.scan.pageStripLabel": "Pages read so far",
  "ob.scan.logLabel": "The pages I am walking, newest first",
  "ob.scan.pageFetched": "{url} — read",
  "ob.scan.pageSkipped": "{url} — skipped: {reason}",
  "ob.scan.pageFailed": "{url} — could not be read: {reason}",
  "ob.scan.pageNoReason": "no reason recorded",
  "ob.scan.pageStatusFetched": "read",
  "ob.scan.pageStatusSkipped": "skipped: {reason}",
  "ob.scan.pageStatusFailed": "could not be read: {reason}",
  "ob.scan.skipReason.robots": "the site asked me not to read it",
  "ob.scan.skipReason.offDomain": "it lives on another domain",
  "ob.scan.skipReason.pageCap":
    "I had already read as many pages as one read allows",
  "ob.scan.skipReason.byteCap":
    "this read had already taken in as much text as it allows",
  "ob.scan.skipReason.unreadable": "I could not read the page",
  "ob.scan.transparency": "Transparency",
  "ob.scan.costLine": "{calls} calls · {tokens} tokens · {cost}",
  "ob.scan.costPending": "no model calls billed yet",
  "ob.scan.costUnpriced": " · unpriced usage exists",

  // --- the live panel: what the read covered, and what it left ----------
  "ob.live.stateDone": "done",
  "ob.live.stateNow": "in progress",
  "ob.live.stateWaiting": "waiting",
  "ob.live.review": "Review",
  "ob.live.hide": "Hide",
  "ob.live.countPages": "{read} read · {skipped} skipped",
  "ob.live.cardCoverage": "What I read, and what I skipped",
  "ob.live.coverageWarning": "Warning",
  // A bounded read has to say it was bounded: the page counts beside it
  // otherwise read as the whole site.
  "ob.live.coverageStopped": "Stopped early",
  "ob.live.stoppedPageCap":
    "I reached the page limit for one read, so there is more of your site I did not open.",
  "ob.live.stoppedByteCap":
    "I reached the size limit for one read, so there is more of your site I did not open.",
  "ob.live.stoppedBudget":
    "I reached the budget for one read, so there is more of your site I did not open.",
  "ob.live.stoppedDeadline":
    "I ran out of time for one read, so there is more of your site I did not open.",
  "ob.live.coverageSkipped": "Skipped",
  "ob.live.coverageFailed": "Could not read",
  "ob.live.coverageClean":
    "Every page I tried came back. Nothing was skipped and nothing failed.",

  // --- facts: saving one, and the ceiling on how many -------------------
  "ob.facts.rowSave": "Save the fact: {fact}",
  "ob.facts.capReached":
    "You can save up to {max} facts. Clear one to make room for another.",

  // --- the payoff: what two minutes actually bought ----------------------
  // Counts, not congratulation. Every cell is a real number off the wire, and
  // a cell with no number says so rather than showing a zero that looks earned.
  // Two leads for one moment. The first is only true when the install really was
  // empty minutes ago; a setup picked up across days is the supported path, and
  // the payoff above all else may not overstate.
  "ob.payoff.lead": "Minutes ago this was an empty install.",
  "ob.payoff.leadResumed": "This started as an empty install.",
  "ob.payoff.factsRead": "facts read",
  "ob.payoff.factsConfirmed": "facts you confirmed",
  "ob.payoff.peopleFound": "people found",
  "ob.payoff.profileFields": "profile fields",
  "ob.payoff.voiceWords": "words in your voice",
  "ob.payoff.pagesRead": "pages read",
  "ob.payoff.voiceNotTrained": "voice not trained yet",
  "ob.payoff.body":
    "Everything in there is yours to correct, and every value still points at the page it came from.",
  "ob.payoff.defaults":
    "I wait for your yes, and never overwrite what you typed. Both change in Settings → Autonomy.",
  "ob.payoff.seats":
    "The one thing left is your colleagues. Seats are billed, so you create them in Settings → People.",
  "ob.payoff.understood": "Understood",

  // --- the handoff into the app -----------------------------------------
  "ob.enter.cta": "Enter Margince",
  "ob.enter.assembling": "Assembling your organization",

  // --- the mailbox backread ---------------------------------------------
  // A separate operation from connecting, and the copy has to keep them
  // separate: connecting grants access, the backread spends budget reading
  // history. Read-only, and it writes nothing until the reader approves.
  "ob.backread.heading": "How far back should I read?",
  "ob.backread.window3m": "3 months — recent context",
  "ob.backread.window6m": "6 months — recommended",
  "ob.backread.window12m": "12 months — full sales cycle",
  "ob.backread.window24m": "2 years — the relationship, not just the deal",
  "ob.backread.window60m": "5 years — everything the mailbox still holds",
  "ob.backread.estimate": "About {messages} messages in that window.",
  "ob.backread.estimateHeuristic":
    "Estimated from the mailbox, not counted yet.",
  "ob.backread.estimateCost": "Roughly {cost} in model calls.",
  "ob.backread.estimateFailed":
    "I could not estimate that window: {detail} You can still start, or pick another.",
  "ob.backread.note":
    "The backread only reads. You see every person and company I found before anything is written.",
  "ob.backread.start": "Connect and read",
  "ob.backread.startFailed":
    "I could not start the backread: {detail} Try again, or continue and start it later in Settings.",
  "ob.backread.running": "Reading your mailbox",
  "ob.backread.runningNote":
    "You can leave this running and keep working. I pick it up where I left off.",
  "ob.backread.queued": "Queued. It starts in a moment.",
  "ob.backread.progress": "{scanned} of about {total} messages",
  "ob.backread.progressNoTotal": "{scanned} messages so far",
  "ob.backread.tallyMessages": "messages read",
  "ob.backread.tallyCaptured": "kept",
  "ob.backread.tallySkipped": "ignored",
  "ob.backread.tallyPeople": "people found",
  "ob.backread.tallyCompanies": "companies found",
  "ob.backread.doneHeading": "Here is what is in there.",
  "ob.backread.doneNote":
    "Nothing is written yet. Everything I found waits for your review in the inbox.",
  "ob.backread.failed":
    "The backread stopped: {detail} Your connection is fine — you can start it again in Settings.",
  "ob.backread.cancelled": "I stopped reading. Nothing was written.",
  "ob.backread.cancelledPartial":
    "I stopped reading. What was already captured stays — it is waiting for you in the inbox.",
  "ob.backread.cancelFailed":
    "I could not stop the read: {detail} Try again — it keeps running meanwhile.",
  "ob.backread.detailUnavailable": "Something unexpected went wrong.",
  "ob.backread.cancel": "Stop reading",
  "ob.backread.explore": "Explore Margince meanwhile",
  "ob.backread.skip": "Do not read history now",

  "auth.title": "Margince",
  "auth.checking": "Checking your session…",
  "auth.pageTitle": "Sign in · Margince",
  "auth.loginTitle": "Sign in to Margince",
  // Short declaratives rather than one comma-joined sentence (VOICE-RULE-1):
  // each clause is a separate fact about the installation, and a reader who
  // stops after the first has still read a true sentence. Two of them, not
  // three — "Margince runs on your own server" is a claim about the product,
  // and someone at a login screen is here to get in. What is left is the
  // provisioning fact (A97, invite-only): what to do when there is no sign-up
  // link.
  "auth.loginSub":
    "Accounts come from your administrator. There is no self-signup.",
  "auth.coreDisclosure": "Margince · AI system",
  "auth.coreBoundary":
    "I can only use your context after Margince verifies that it's you.",
  // The scope of the context the statement above is about. Bounded on purpose:
  // "nothing else" is what keeps it a limit rather than a list of capabilities,
  // which is what the artifact's version of this line was.
  "auth.coreScope":
    "That context is your mail, your calendar, and what I can read on the open web. Nothing else, and nothing without your permission.",
  "auth.corePermission": "I use your permissions.",
  "auth.coreCites": "I cite what I find.",
  "auth.coreWaits": "I wait before taking external action.",
  // The fourth limit. The mockup's five became four, and one that did not
  // travel says why: "enriches records from sources it names" is a capability
  // claim, and ADR-0076 Decision 2 admits only limits. This one is a limit.
  "auth.coreMarks": "I mark every value I wrote.",
  "auth.coreConfigured": "Configured",
  "auth.coreUnconfigured": "AI not configured",
  "auth.coreStillWorks": "The CRM still works.",
  "auth.coreDevelopment": "Development AI",
  "auth.coreModeCloud": "cloud routing",
  "auth.coreModeLocal": "local routing",
  "auth.coreModeHybrid": "hybrid routing",
  "auth.coreModeNone": "no model routing",
  "auth.coreModeDevelopment": "offline development path",
  "auth.coreProviderAnthropic": "Anthropic",
  "auth.coreProviderGemini": "Gemini",
  "auth.coreProviderOllama": "Ollama",
  "auth.coreProviderOpenAI": "OpenAI",
  "auth.coreProviderCompatible": "compatible provider",
  "auth.coreProviderVllm": "vLLM",
  // The shortest label that still names the field (VOICE-RULE-1), pinned by the
  // login spec §7.1/§7.2 (Amendment 4) and reconciling
  // single-organization-auth-concept.md §12, which already drew "Email".
  "auth.email": "Email",
  // A placeholder is an EXAMPLE, never an instruction and never the label
  // again. "Enter your email" in a placeholder is a label that disappears.
  // The address is the login spec §7.2's, and the reserved example domain
  // rather than a plausible one: `company.com` belongs to somebody.
  "auth.emailPlaceholder": "name@example.com",
  "auth.password": "Password",
  "auth.passwordPlaceholder": "Password",
  "auth.passwordHint": "at least 12 characters",
  "auth.showPassword": "Show password",
  "auth.hidePassword": "Hide password",
  "auth.capsLock": "Caps Lock is on",
  // NOT the label of a served provider button. A real installation's button text
  // is `oidc_providers[].label` off the wire, server-owned, and the client never
  // composes it. This is what the ui-preview fixture uses to stand in for that
  // server in the reader's own language — see app/ui-preview.ts.
  "auth.continueWith": "Continue with {brand}",
  // Labels the password path, not the provider buttons above it: where the
  // installation runs SSO, the form beneath this divider is the fallback door.
  "auth.orDivider": "or",
  // §7.1 verbatim. The noun is "organization", not "workspace": ADR-0061/A107
  // keeps `workspace` internal and §7.3 removed it from authentication. And the
  // line states that ACCESS is restricted, never that data is safe, encrypted or
  // compliant — VOICE-RULE-7 rules those out here, because they are outcome
  // claims the installation's own configuration can contradict, on the screen a
  // CISO reads on the way in.
  "auth.legalProtected": "Access to this organization is restricted.",
  "auth.legalTerms": "Terms",
  "auth.legalPrivacy": "Privacy",
  "auth.signingIn": "Signing in…",
  "auth.signIn": "Sign in",
  "auth.failed": "That didn't work",
  "auth.errCredentials":
    "We couldn't sign you in. Check your email and password and try again.",
  "auth.errRateLimited":
    "Too many sign-in attempts. Wait a moment and try again.",
  "auth.errUnreachable":
    "Margince couldn't be reached. Check your connection and try again.",
  "auth.retry": "Try again",
  "auth.noticeSignedOut": "You have been signed out.",
  "auth.noticeSessionExpired":
    "Your session expired. Sign in again to continue.",
  "auth.connectionTitle": "Margince couldn't be reached",
  "auth.connectionBody":
    "Check your connection and try again. If the problem persists, the server may be restarting.",
  "auth.unavailableTitle": "Installation not ready",
  "auth.unavailableBody":
    "This Margince installation isn't ready to sign you in. An operator needs to complete or repair the setup.",
  // The first-run claim (ADR-0105). An installation with no configured admin
  // waits to be claimed; this is the only screen that creates an account
  // without one, so it names what it is creating.
  // Changing your own password from account settings. The current password is
  // the authority, not the session.
  // The account whose password an operator chose. Authenticated, and
  // able to do exactly one thing until it has a password of its own.
  "forcedPassword.pageTitle": "Choose your password",
  "forcedPassword.title": "Choose your own password",
  "forcedPassword.body":
    "This account is still using the password your operator set up. Choose one only you know before you continue.",
  "password.title": "Password",
  "password.body": "Change the password you sign in with.",
  "password.current": "Current password",
  "password.next": "New password",
  "password.confirm": "Confirm new password",
  "password.hint": "At least 12 characters.",
  "password.tooShort": "Too short. Use at least 12 characters.",
  "password.mismatch": "These two don't match.",
  "password.signsYouOut":
    "Changing it signs you out everywhere, including here. Sign in again with the new password.",
  "password.changing": "Changing your password…",
  "password.submit": "Change password",
  "password.done": "Password changed. Sign in again with the new one.",
  // Deliberately says nothing about WHICH field: this is the fallback for a
  // refusal the server did not explain, and naming the current password would
  // send someone hunting a mistake that may not be theirs.
  "password.errorGeneric": "The password couldn't be changed. Try again.",
  "setup.pageTitle": "Set up Margince",
  "setup.title": "Claim this installation",
  "setup.body":
    "This Margince installation has no organization yet. Your operator has a one-time setup token from the token file the server wrote at first start.",
  "setup.token": "Setup token",
  "setup.tokenHint":
    "From the token file the server wrote at first start — the server log names its path, and carries the token itself if that file could not be written.",
  "setup.organization": "Organization name",
  "setup.adminName": "Your name",
  "setup.adminEmail": "Your email",
  "setup.adminPassword": "Choose a password",
  "setup.passwordHint": "At least 12 characters.",
  "setup.passwordShort": "Too short. Use at least 12 characters.",
  "setup.rootWarning":
    "This creates the administrator account for the whole installation. It has every permission, including managing everyone else.",
  "setup.claim": "Create the organization",
  "setup.claiming": "Creating…",
  "setup.errorToken":
    "That setup token isn't valid for this installation. Check the token file the server named in its log at first start.",
  "setup.errorAlready":
    "This installation already has an organization. Sign in instead, or ask your operator to reset it.",
  "setup.errorFields":
    "Something in the form needs fixing. Check the fields and try again.",
  "setup.errorServer":
    "Margince couldn't complete the setup. Nothing was created. Try again in a moment; if it keeps failing, check the server log.",
  "setup.errorNetwork":
    "Margince couldn't be reached. Check your connection and try again.",
  "auth.forgotLink": "Forgot password?",
  "auth.forgotTitle": "Reset your password",
  // Two sentences, sentence-cased, with no dash. VOICE-RULE-5 forbids an em or
  // en dash anywhere in user-facing copy, and a lowercase opening mid-surface
  // reads as a fragment rather than as a sentence. Same for auth.resetSub.
  "auth.forgotSub":
    "Enter your email. If it has an account, a reset link is on its way.",
  "auth.sendResetLink": "Send reset link",
  "auth.forgotSentTitle": "Check your inbox",
  "auth.forgotSentBody":
    "If that address has an account, a reset link is on its way. It expires in one hour.",
  "auth.resetTitle": "Choose a new password",
  "auth.resetSub": "Your link is valid. Choose a new password.",
  "auth.newPassword": "New password",
  "auth.setNewPassword": "Set new password",
  "auth.resetFailed": "That reset link is invalid, used, or expired.",
  // The password was refused, not the link — so the link is still good and the
  // user must not be sent to replace it.
  "auth.resetRejectedPassword":
    "That password was refused. Choose a different one and try again.",
  // Neither the link's fault nor the user's: the token is untouched, so retrying
  // the same one is the right advice. Two sentences, no dash (VOICE-RULE-5).
  "auth.resetServerFailed":
    "We couldn't set your password just now. Your link is still valid, so try again in a moment.",
  // Its own key rather than auth.errRateLimited, which says "sign-in attempts":
  // this user is setting a password, not signing in, and copy that names the
  // wrong action reads as the wrong error.
  "auth.resetRateLimited":
    "Too many attempts. Wait a moment, then set your password again.",
  "auth.requestNewLink": "Request a new link",
  "auth.askAdminForNewLink":
    "Ask your administrator for a new set-password link.",
  "auth.resetDoneTitle": "Password updated",
  "auth.resetDoneBody":
    "Your password is changed and every other session is signed out. Sign in with the new password.",
  "auth.backToLogin": "Back to sign in",
  "auth.signOut": "Sign out",

  "client.back": "Back to Margince",
  "client.title": "Margince alongside your inbox",
  "client.sub": "the extension surface — shell-free, record-aware",
  "client.sender": "Sender",
  "client.lookup": "Look up",
  "client.open360": "Open the 360",
  "client.unknown": "Not in your organization yet.",
  "client.unknownDetail":
    "This sender matches no contact you can see. Nothing was fetched from anywhere else.",
  "client.createLead": "Capture as lead",
  "client.isolation": "talks only to YOUR organization",
  "client.attribution": "Every capture is attributed and auditable.",

  "book.title": "Book a meeting",
  "book.sub": "live availability from the connected calendar",
  "book.min15": "15 min",
  "book.min30": "30 min",
  "book.min60": "60 min",
  "book.attendee": "Attendee email",
  "book.welcomeBack": "Recognized: {name}",
  "book.subject": "Meeting via Margince",
  "book.confirmed": "Booked. The invite is on its way.",
  "book.failed": "Booking didn't go through — nothing was scheduled.",
  "book.publicSub": "pick a slot — no account needed",
  "book.name": "Your name",
  "book.email": "Your email",
  "book.consentWording":
    "I agree that my name and email are stored to arrange and follow up on this meeting.",

  "prefs.title": "Choose what you hear from us",
  "prefs.sub":
    "Each purpose is separate — this isn't all-or-nothing. Transactional messages can't be switched off here, because you need them; everything else is yours to control.",
  "prefs.invalidLink":
    "This link is no longer valid. Preference links expire and can be withdrawn — ask for a fresh one from any recent email.",
  "prefs.rateLimited":
    "Too many attempts from here just now. Wait a minute and reload.",
  "prefs.subscribed": "Subscribed",
  "prefs.notSubscribed":
    "Not subscribed — you receive nothing for this purpose",
  "prefs.alwaysOn": "always on",
  "prefs.lockedWhy": "Transactional — exempt from opt-out.",
  "prefs.notSaved": "Not saved yet.",
  "prefs.savePending": "Pending: {changes}.",
  "prefs.saveProof":
    "We record the exact wording you saw and a timestamp as proof — then it applies to every future send.",
  "prefs.save": "Save preferences",
  "prefs.discard": "Discard",
  "prefs.partialSave":
    "Something went wrong part-way. Some of your choices may have been saved — we've reloaded your current settings so you can see exactly where you stand.",
  "prefs.wordingGeneric": '"Send me {label}."',
  "prefs.wording.marketing_email":
    '"Send me product updates & occasional marketing email."',
  "prefs.wording.events": '"Send me event & webinar invitations."',
  "prefs.unsubscribeAll": "Unsubscribe from all marketing",
  "prefs.unsubscribeAllHint":
    "Prefer to stop all non-essential mail at once? You'll still get transactional messages.",
  "prefs.oneClickDone":
    "Done — you're off our marketing email. It takes effect immediately across every campaign.",
  "prefs.oneClickAlreadyOff": "Nothing to do — these were already off.",
  "prefs.undo": "Undo — keep receiving marketing",
  "prefs.undoExplicit":
    "Re-subscribing is an explicit opt-in — we won't silently turn it back on. Save below to record your consent, or discard.",

  "auto.sub": "a closed catalog — pick a type, set its parameters, enable it",
  "auto.readOnly":
    "Read-only view — you do not have permission to change automations.",
  "auto.catalog": "Starter library",
  "auto.catalogSub": "the closed set of automation types",
  "auto.instances": "Configured automations",
  "auto.use": "Use template",
  "auto.name": "Name",
  "auto.create": "Create",
  "auto.createdPaused": "Created paused — nothing runs until you enable it.",
  "auto.delete": "Delete",
  "auto.statusEnabled": "enabled",
  "auto.statusPaused": "paused",
  "auto.dateField.placeholder": "Select date field",
  "auto.dateField.needsObject":
    "Choose an object first to list its date fields.",
  "auto.dateField.empty": "This object has no active date fields yet.",
  "auto.dateField.loadError":
    "Couldn't load this object's date fields. Try again.",
  "auto.enabledFor": "{name} is enabled",
  "auto.rowActions": "Actions for {name}",
  "auto.withheld":
    "The configured automations are hidden — your role cannot read them.",
  "auto.deleteTitle": "Delete this automation?",
  "auto.deleteBody":
    "“{name}” and its settings are removed for good. To stop it firing without losing the rule, turn it off instead.",

  "auto.runs.open": "Runs",
  "auto.runs.title": "Run history",
  "auto.runs.filterAll": "All",
  "auto.runs.filterFired": "Fired",
  "auto.runs.filterFailed": "Failed",
  "auto.runs.filterBlocked": "Blocked",
  "auto.runs.filterSkipped": "Skipped",
  "auto.runs.filterQueued": "Queued for approval",
  "auto.runs.empty": "This automation hasn't fired yet.",
  "auto.runs.emptyFiltered": "No runs with this outcome.",
  "auto.runs.needsApproval": "needs approval",
  "auto.runs.why": "Why",
  "auto.runs.target": "Target",
  "auto.runs.result": "Result",
  "auto.runs.reason": "Reason",
  "auto.runs.outcomeFired": "fired",
  "auto.runs.outcomeFailed": "failed",
  "auto.runs.outcomeBlocked": "blocked",
  "auto.runs.outcomeSkipped": "skipped",
  "auto.runs.outcomeQueued": "queued",

  "auto.preview.open": "Preview",
  "auto.preview.title": "Dry-run blast radius",
  "auto.preview.window": "Window",
  "auto.preview.window7": "7d",
  "auto.preview.window30": "30d",
  "auto.preview.window90": "90d",
  "auto.preview.matchesNow": "Matches now: {n}",
  "auto.preview.wouldFire": "Would fire: ~{n} / {days}d",
  "auto.preview.notComputable": "Trailing estimate not computable",
  "auto.preview.hidden": "{n} hidden — no access",
  "auto.preview.explainer":
    "A read-only dry run — no records are changed and nothing is sent.",

  "strength.title": "Relationship strength",
  "strength.score": "Score {score}/100",
  "strength.bucket.dormant": "Dormant",
  "strength.bucket.weak": "Weak",
  "strength.bucket.warm": "Warm",
  "strength.bucket.strong": "Strong",
  "strength.factor.recency": "Recency",
  "strength.factor.frequency": "Frequency",
  "strength.factor.reciprocity": "Reciprocity",
  "strength.factor.direction": "Direction",
  "strength.lastInteraction": "Last interaction: {when}",
  "strength.none": "No interactions yet",
  "strength.inout": "{in} in · {out} out (90d)",
  "strength.computedFrom": "Computed from {count} activities",

  // The relationship-graph cards (ADR-0078). The colleague bands are PO-F-3b's
  // own vocabulary and deliberately differ from the workspace-wide card's:
  // the two measure different things and must not read as comparable.
  "network.title": "Who here knows them",
  "network.empty": "Nobody here has recorded contact with this person yet.",
  "network.interactions": "{count} interactions (90 days)",
  "network.neverSpoken": "No recorded contact",
  "network.bucket.none": "No contact",
  "network.bucket.weak": "Weak",
  "network.bucket.moderate": "Moderate",
  "network.bucket.strong": "Strong",
  "coverage.title": "Coverage",
  "coverage.clear": "Nothing flagged — this deal passes every coverage check.",
  "coverage.withheld":
    "Coverage was withheld — you cannot read this deal’s relationships, so no check was run.",
  "coverage.daysSinceTouch": "{days} days",
  "coverage.risk.single_threaded_theirs": "Single-threaded",
  "coverage.risk.single_threaded_ours": "Carried by one colleague",
  "coverage.risk.coverage_gap": "No engaged champion",
  "coverage.risk.champion_left": "Champion has left",
  "coverage.risk.stakeholder_left": "Stakeholder has left",
  "coverage.risk.going_cold": "Going cold",

  "cf.title": "Custom fields",
  "cf.formSection": "Custom fields",
  "cf.subtitle":
    "Add a simple typed field to an object you already have — at runtime, no developer, no deploy. New objects and relationships still go through code.",
  "cf.object": "Object",
  "cf.obj.deal": "Deal",
  "cf.obj.organization": "Company",
  "cf.obj.person": "Contact",
  "cf.obj.lead": "Lead",
  "cf.col.field": "Field",
  "cf.col.type": "Type",
  "cf.col.addedBy": "Added by",
  "cf.addedByYou": "You",
  "cf.addedByAdmin": "Admin",
  "cf.empty.deal":
    "No custom fields on Deal yet. Add one below if you track something we didn't ship.",
  "cf.empty.organization":
    "No custom fields on Company yet. Add one below if you track something we didn't ship.",
  "cf.empty.person":
    "No custom fields on Contact yet. Core fields cover the contact record; add one below if you track more.",
  "cf.empty.lead":
    "No custom fields on Lead yet. A field you add here also appears once a lead is promoted to a contact.",
  "cf.type.text": "Text",
  "cf.type.number": "Number",
  "cf.type.date": "Date",
  "cf.type.currency": "Currency",
  "cf.type.picklist": "Picklist",
  "cf.type.boolean": "Yes / No",
  "cf.builder.addTo": "Add a field to {object}",
  "cf.builder.noCode": "no code",
  "cf.builder.intro":
    "A new field is a real column on the existing table — it filters, reports, exports, and is in the API like any core field. It is not a new object.",
  "cf.label": "Label",
  "cf.apiKey": "API key",
  "cf.apiKeyHint":
    "Auto-derived, immutable once live. Prefixed cf_ so it never collides with a core field.",
  "cf.typeLabel": "Type",
  "cf.currencyCode": "Currency code",
  "cf.currencyHint":
    "Three-letter ISO-4217 code (e.g. EUR, USD). Money is stored to the cent.",
  "cf.options": "Options",
  "cf.addOption": "Add option",
  "cf.removeOption": "Remove option",
  "cf.optionPlaceholder": "Option label",
  "cf.lastOptionBlocked": "A picklist needs at least one option",
  "cf.gate.title": "Adding a field is gated.",
  "cf.gate.body":
    "On confirm it becomes a live column on every {object} — on the 360, in search & filters, lists, export, and the API. The add is written to the audit trail.",
  "cf.refuse.title":
    "That looks like a new object or relationship, not a field.",
  "cf.refuse.body":
    "This builder adds simple fields to existing records only. A new object, a link between objects, or a calculated roll-up is a structural change — it ships as a reviewed change to Margince in a new version, done by people, not by the product editing its own code.",
  "cf.refuse.route":
    "Route it through the development path — your own engineers, an implementation partner, or Gradion services.",
  "cf.confirm": "Confirm & add field",
  "cf.reset": "Reset",
  "cf.writing": "writing…",
  "cf.added": 'Field "{label}" added — live on 360, filters, export & API',
  "cf.edit": "Edit label",
  "cf.archive": "Archive field",
  "cf.archived":
    '"{label}" archived — hidden from new records, retained in audit & history (reversible)',
  "cf.renamePrompt": "New label",
  "cf.renamed": 'Renamed to "{label}"',
  "cf.audit.title": "Recent field changes",
  "cf.audit.empty": "No custom-field changes yet.",
  "cf.audit.footer":
    "Every add / edit / archive is recorded permanently in the audit log.",
  "cf.noPermission":
    "You have read-only access to custom fields — adding, editing and archiving are not yours to do here.",
  "cf.retired": "Retired",
  // The settings level, in the order the sidebar prints it. "General" rather
  // than "Organization" for the first org entry: the group heading above it
  // already says that word, and a row repeating its own heading names nothing.
  // The same reason keeps the possessive off "Agents" — the group is "You".
  //
  // "Connections" and "Integrations" are the same distinction the two groups
  // are: the mailbox and the network a PERSON connected, against the outside
  // systems the INSTALLATION is wired to. One row carried both before, which
  // is why it had to be ungated to keep a rep's own mailbox reachable.
  "settings.tab.account": "Account",
  "settings.tab.voice": "Writing voice",
  "settings.tab.agents": "Agents",
  "settings.tab.connections": "Connections",
  "settings.tab.general": "General",
  "settings.tab.people": "People & access",
  "settings.tab.integrations": "Integrations",
  "settings.tab.capture": "Capture",
  "settings.tab.data-model": "Data model",
  "settings.tab.ai": "AI",
  "settings.tab.privacy": "Privacy & audit",
  "settings.tab.capture-activity": "Capture activity",
  "captureActivity.title": "Capture activity",
  "captureActivity.sub": "What happened to your messages in the last 24 hours.",
  "captureActivity.scope.label": "Whose activity",
  "captureActivity.scope.mine": "Mine",
  "captureActivity.scope.workspace": "Shared channels",
  "captureActivity.scopeNote":
    "Counted from the point a connector hands a message to this CRM. What a connector filtered on its own side — a chat reaction, a mail rule — is not included. Covers messages; lead capture is not shown here.",
  "captureActivity.filtered":
    "Showing {shown} of {total} {outcome} in this window.",
  "captureActivity.openTrace": "See every step this message went through",
  "captureActivity.emptyFiltered":
    "none of the loaded rows match — load more to reach the rest of the window",
  "captureActivity.loadMore": "Load more",
  "captureActivity.empty": "no capture activity in the last 24 hours",
  "captureActivity.contentNotStored": "content not stored",
  "captureActivity.contentNone": "no sender recorded",
  "captureActivity.outcome.captured": "Captured",
  "captureActivity.outcome.internal": "Dropped as internal",
  "captureActivity.outcome.suppressed": "No contact created",
  "captureActivity.outcome.deferred": "Waiting on a verdict",
  "captureActivity.outcome.fault": "Derivation failed",
  "captureActivity.reason.internal_only": "every party was on your own domains",
  "captureActivity.reason.deferral_capped":
    "the open-question limit was reached, so no verdict is coming",
  "captureActivity.reason.noise_prior":
    "a previous verdict judged this sender noise, so it will be archived",
  "captureActivity.reason.decided_prior":
    "this sender was already decided, so no contact will be created",
  "captureActivity.reason.no_granting_human":
    "the connection named no member to act for",
  "captureActivity.reason.invisible_incumbent":
    "it matched a record outside what you can see",
  "captureActivity.reason.derivation_failed":
    "the contact step failed; the message itself is unaffected",
  "captureActivity.reason.no_counterparty": "no sender this CRM could record",
  "captureActivity.reason.transactional_infra":
    "the sender is mail infrastructure, not a company you work with",
  "captureActivity.reason.transactional_prefix":
    "the sender looks like an automated mailer, not a person",
  "captureActivity.outcome.deferred_capped": "Not queued",
  "captureActivity.outcome.deferred_sent": "Sent for a verdict",
  "captureActivity.resolution.pending": "still waiting",
  "captureActivity.resolution.unsure": "sent to the review queue",
  "captureActivity.resolution.real": "judged a real contact",
  "captureActivity.resolution.noise": "judged noise",
  "captureActivity.resolution.rejected": "declined by a human",
  "captureActivity.resolution.suppressed": "suppressed",
  "pipeline.title": "How this message was handled",
  "pipeline.sub":
    "Every step of the capture pipeline, in the order this message met them.",
  "pipeline.payloadsOff":
    "No sender or subject is stored for any step: this deployment did not turn payload capture on.",
  "pipeline.transport": "Carried by",
  "pipeline.unavailable": "this message's pipeline steps could not be read",
  "pipeline.status.done": "Done",
  "pipeline.status.skipped": "Skipped",
  "pipeline.status.pending": "Waiting",
  "pipeline.status.failed": "Failed",
  "pipeline.status.not_applicable": "Did not apply",
  "pipeline.status.unknown": "Cannot tell",
  "pipeline.reason.record_not_available":
    "this step's record is no longer kept, or is not yours to read — once the record is gone the two cannot be told apart",
  "pipeline.status.not_reported": "Not reported here",
  "pipeline.subject.message": "about this message",
  "pipeline.subject.sender": "about the sender, not this message alone",
  "pipeline.subject.domain": "about the sender's domain",
  "pipeline.subject.thread": "about the whole conversation",
  "pipeline.stage.connector_filter": "Connector filtering",
  "pipeline.stage.ingress_gate": "Admission check",
  "pipeline.stage.erasure_check": "Erasure check",
  "pipeline.stage.internal_drop": "Internal-only check",
  "pipeline.stage.activity_write": "Saved to the timeline",
  "pipeline.stage.tier_ladder": "Contact decision",
  "pipeline.stage.person_create": "Contact created",
  "pipeline.stage.verdict": "Sender verdict",
  "pipeline.stage.company_triage": "Company check",
  "pipeline.stage.attention_label": "Attention label",
  "pipeline.stage.material_events": "Conversation reading",
  "pipeline.stage.claim_extraction": "Commitments and open loops",
  "pipeline.reason.internal_only": "every party was on your own domains",
  "pipeline.reason.invisible_incumbent":
    "it matched a record outside what you can see",
  "pipeline.reason.transactional_infra":
    "the sender is mail infrastructure, not a company you work with",
  "pipeline.reason.transactional_prefix":
    "the sender looks like an automated mailer, not a person",
  "pipeline.reason.deferral_capped":
    "the open-question limit was reached, so no verdict is coming",
  "pipeline.reason.noise_prior": "a previous verdict judged this sender noise",
  "pipeline.reason.decided_prior": "this sender was already decided",
  "pipeline.reason.no_counterparty": "no sender this CRM could record",
  "pipeline.reason.no_granting_human":
    "the connection named no member to act for",
  "pipeline.reason.derivation_failed":
    "the contact step failed; the message itself is unaffected",
  "pipeline.reason.not_linked_yet": "no contact is linked to this message yet",
  "pipeline.reason.no_contact_intended":
    "the contact decision concluded that none was to be made",
  "pipeline.reason.awaiting_verdict":
    "the sender is still waiting on a verdict",
  "pipeline.reason.verdict_reached":
    "a verdict has been reached for this sender",
  "pipeline.reason.no_open_question":
    "there was no open question about this sender",
  "pipeline.reason.transport_not_read":
    "this step reads email only, and the message arrived over another transport",
  "pipeline.reason.sender_undecided":
    "the sender is still waiting on a verdict, so the message is held back",
  "pipeline.reason.archived": "the message is archived",
  "pipeline.reason.not_connector_captured":
    "the message was not captured by a connector",
  "pipeline.reason.awaiting_batch":
    "it is eligible and waiting for the next batch",
  "pipeline.reason.labelled": "the message was labelled",
  "pipeline.reason.not_comparable":
    "what a connector filters on its own side is not counted here — the numbers mean different things per connector",
  "pipeline.reason.connector_side_defect":
    "admission failures are a fault of the connection, not of one message",
  "pipeline.reason.would_restore_erased":
    "reporting this would restore data an erasure removed",
  "pipeline.reason.no_writer_yet": "this step does not exist yet",
  "pipeline.reason.not_reported_yet":
    "this step runs, but is not reported here yet",
  "settings.tab.maintenance": "Maintenance",
  "settings.tab.license": "License",
  "license.card.title": "License and seats",
  "license.state.licensed": "Licensed",
  "license.state.uncapped": "Licensed, no seat limit",
  "license.state.unlicensed": "No license configured",
  "license.seats.used": "Seats in use",
  "license.seats.granted": "Seats granted",
  "license.seats.uncapped": "No limit",
  "license.meter.label": "{used} of {granted} seats in use",
  "license.over.title": "You are over your seat entitlement",
  "license.over.body":
    "{used} seats are in use and the license grants {granted}. Nobody loses access and no seat is taken away — but no new member can be invited until you are back inside the entitlement. Deactivate a member, or raise the entitlement.",
  "license.holder.title": "Licensed to",
  "license.holder.org": "Organization",
  "license.holder.contact": "Contact",
  "license.holder.installation": "Installation",
  "license.holder.validUntil": "Valid until",
  "license.holder.expiredOn": "Expired on",
  "license.holder.id": "License id",
  "license.grace.title": "This license expired",
  "license.grace.body":
    "The license expired on {expiry}. It still works, for a limited period. Renew it to keep the installation in service.",
  "license.renewal.title": "This license needs a renewal",
  "license.renewal.body":
    "The license expires on {expiry}. Nothing changes before that date.",
  "license.counting":
    "Full seats that are neither deactivated nor suspended, agents included. Read-only seats are unlimited and never counted. This is the count a new member is admitted against.",
  "settings.group.you": "You",
  "settings.group.org": "Organization",
  "settings.rates.fxTitle": "Currency rates",
  "settings.rates.fxIntro":
    "Exchange rates that convert foreign-currency amounts to your base currency. New rates take effect today or later; past rates are never changed.",
  "settings.rates.fxWithheld":
    "Only an admin or ops can see the currency rates. They are the conversion every roll-up in the installation is built on, so they are not shown more widely.",
  "settings.rates.modelWithheld":
    "Only an admin or ops can see what each model costs. The prices are operator information, so they are not shown more widely.",
  "settings.rates.readOnly":
    "Read-only view — you do not have permission to change rates.",
  "settings.rates.fxAdd": "Set rate",
  "settings.rates.fxEmpty": "No currency rates yet.",
  "settings.rates.fxModalTitle": "Set a currency rate",
  "settings.rates.rateToBase": "Rate (to base currency)",
  "settings.rates.modelTitle": "AI model costs",
  "settings.rates.modelIntro":
    "Per-model prices in USD per 1M tokens, used to estimate AI spend. Transparency only — prices never change how models are routed.",
  "settings.rates.modelAdd": "Add model rate",
  "settings.rates.modelEmpty": "No model rates yet.",
  "settings.rates.modelModalTitle": "Set a model price",
  "settings.rates.setRate": "Save",
  "settings.rates.refresh": "Refresh from sources",
  "settings.rates.refreshEnqueued":
    "Refresh requested — any proposed changes will appear in the inbox.",
  "settings.rates.colFrom": "From",
  "settings.rates.colRate": "Rate (→{base})",
  "settings.rates.colEffective": "Effective",
  "settings.rates.colProvider": "Provider",
  "settings.rates.colModel": "Model",
  "settings.rates.colInput": "Input $/M",
  "settings.rates.colOutput": "Output $/M",
  "settings.rates.colCacheRead": "Cache read $/M",
  "settings.rates.colCacheWrite": "Cache write $/M",
  "settings.voice.title": "Voice DNA",
  "settings.voice.intro":
    "Your personal writing voice. It shapes drafts made for you, stays private to you, and only learns from sources you add.",
  "settings.voice.readOnly":
    "Read-only view — you do not have permission to change your Voice DNA.",
  "settings.voice.emptyTitle": "No Voice DNA yet",
  "settings.voice.emptyBody":
    "Add a few writing samples below and build your Voice DNA — or do it during onboarding.",
  "settings.voice.status.collecting": "Collecting",
  "settings.voice.status.ready": "Ready",
  "settings.voice.status.stale": "Needs a rebuild",
  "settings.voice.bandThin": "thin",
  "settings.voice.bandGood": "good",
  "settings.voice.bandRich": "rich",
  "settings.voice.bandSharp": "sharp",
  "settings.voice.version": "version {n}",
  "settings.voice.derivedLabel": "Your derived voice",
  "settings.voice.derivedEmpty":
    "Not built yet — add samples and build to see your derived voice.",
  "settings.voice.personalityLabel": "Your preferences",
  "settings.voice.personalityPlaceholder":
    "Notes on how you want to sound — kept exactly as you write them; the model never overwrites this.",
  "settings.voice.savePreferences": "Save preferences",
  "settings.voice.corpusLabel": "Writing samples",
  "settings.voice.meter": "{count} of {target} words",
  "settings.voice.register.email": "email",
  "settings.voice.register.social": "social",
  "settings.voice.register.long_form": "long-form",
  "settings.voice.register.spoken": "spoken",
  "settings.voice.register.general": "general",
  "settings.voice.bandDrop":
    "Removing this drops your voice from {from} to {to}. Confirm by activating remove again.",
  "voice.insights.avoidLabel": "What your voice avoids",
  "voice.insights.voiceScore": "voice match {pct}%",
  "voice.insights.next.addTranscript":
    "Add a call or meeting transcript \u2014 spoken words are your highest-signal source.",
  "voice.insights.next.addEmail":
    "Add sent emails \u2014 they are the primary source for how you write at work.",
  "voice.insights.next.addWords":
    "Add roughly {count} more words to reach the sharp band.",
  "voice.insights.next.atTarget":
    "Your corpus is at target; keep it fresh by adding recent writing occasionally.",
  "voice.status.active": "active",
  "voice.status.candidate": "awaiting review",
  "voice.status.superseded": "superseded",
  "voice.status.rejected": "rejected",
  "voice.classification.routine": "routine change",
  "voice.classification.material": "material change",
  "voice.outcome.autoActivated": "activated automatically",
  "voice.outcome.reviewRequired": "review required",
  "voice.outcome.manuallyActivated": "activated by you",
  "voice.outcome.rejected": "rejected",
  "voice.outcome.rollback": "restored",
  "voice.history.versionRow": "v{n} \u00b7",
  "voice.history.loadMore": "Show older entries",
  "voice.insights.provenance": "Built from your corpus \u00b7 v{n}",
  "voice.insights.statWords": "Words: {count}",
  "voice.insights.statSources": "Sources: {count}",
  "voice.insights.statSentence": "\u2248{count} words per sentence",
  "voice.insights.thinkingLabel": "How you think",
  "voice.insights.movesLabel": "Your signature moves \u2014 in your own words",
  "voice.insights.samplesLabel": "Sample drafts in your voice",
  "voice.insights.draftOnly": "draft only \u2014 never sent",
  "voice.insights.disclosure":
    "AI-assisted drafts; every send stays a human decision.",
  "voice.insights.nextBestLabel": "To make it better:",
  "voice.candidate.title":
    "A new voice version (v{n}) is waiting for your review.",
  "voice.candidate.apply": "Use this version",
  "voice.candidate.reject": "Keep my current voice",
  "voice.history.label": "Versions and learning",
  "voice.history.empty": "No versions yet \u2014 build your voice first.",
  "voice.history.deltasLabel": "What changed",
  "voice.history.deltaRow": "v{from} \u2192 v{to}",
  "voice.history.learning":
    "Learning continuously \u2014 drafts served: {drafted} \u00b7 edited before sending: {edited} \u00b7 rejected: {rejected}.",
  "voice.history.rollback": "Restore version {n}",
  "settings.voice.corpusEmpty": "No samples yet.",
  "settings.voice.excluded": "excluded",
  "settings.voice.removeSource": "Remove sample",
  "settings.voice.pastedLabel": "Pasted writing",
  "settings.voice.addPlaceholder":
    "Paste an email, post, or anything you've written…",
  "settings.voice.addSource": "Add sample",
  "settings.voice.addFirstLabel": "Your first writing sample",
  "settings.voice.addFirstCta": "Add it and start my Voice DNA",
  "settings.voice.browseFiles": "Choose files",
  "settings.voice.dropHint":
    "Or drop .txt, .md, .vtt, .srt or .json files here.",
  "settings.voice.floorLabel": "Progress towards the first build ({min} words)",
  "settings.voice.floorProgress": "{words} of {min} words to a first build",
  "settings.voice.speakerQuestion": "Which speaker are you in “{name}”?",
  "settings.voice.speakerDetail": "{words} words, {turns} turns",
  "settings.voice.speakerConfirm": "That one is me",
  "settings.voice.speakerDismiss": "Skip this file",
  "settings.voice.noticeKept": "{name}: kept {kept} of {total} words.",
  "settings.voice.noticeSkippedType":
    "{name} was skipped — only text files can be read.",
  "settings.voice.noticeSkippedEmpty": "{name} was skipped — it has no text.",
  "settings.voice.noticeDismissed":
    "{name} was skipped — nothing in it could be attributed to you.",
  "settings.voice.noticeAskQueueFull":
    "{name} was not added — answer the speaker questions above first, then add it again.",
  "settings.voice.noticeFailed": "{name} could not be added: {detail}",
  "settings.voice.noticeUnexpected": "{name} could not be added.",
  "settings.voice.refusalUnattributed":
    "{name} is a conversation, and none of it could be attributed to you — so none of it was added.",
  "settings.voice.refusalSpeaker":
    "That speaker was not found in {name}, so nothing was added.",
  "settings.voice.refusalUnsupported":
    "{name} is not a format the corpus can read.",
  "settings.voice.buildsTitle": "Builds",
  "settings.voice.building": "Building…",
  "settings.voice.rebuild": "Rebuild Voice DNA",
  "settings.voice.buildNeedsWords":
    "About {n} more words and I can build your first voice. Below that there is not enough of your writing to learn anything honest from.",
  "settings.voice.buildProvisional":
    "Enough to build from. About {n} more words gives the build a fuller picture of how you write.",
  "settings.voice.buildStatus.succeeded": "Voice DNA updated.",
  "settings.voice.buildStatus.failed": "The build didn't finish — try again.",
  "settings.voice.buildStatus.deferred":
    "Queued — it'll finish shortly and update automatically.",
  "settings.voice.buildStatus.pending":
    "Still building — this can take a moment; it'll update here when it's done.",
  "extAccess.title": "Extensions & access",
  "extAccess.sub":
    "What each composed extension unit brought into this installation, and which role may use it. Admin-only.",
  "extAccess.adminOnly": "Extension access is available to admins only.",
  "extAccess.readOnly":
    "Your seat reads this page. Changing a grant needs a full seat.",
  "extAccess.empty": "No extension units are composed into this installation.",
  "extAccess.version": "Version {version}",
  "extAccess.openUnit": "Open the {name} page",
  "extAccess.noPage":
    "{name} is composed into the API, but this build of the app has no page for it — the app is probably older than the server.",
  "extAccess.brings.heading": "What this unit brings",
  "extAccess.brings.objects": "Permission objects",
  "extAccess.brings.routes": "Routes",
  "extAccess.brings.jobs": "Background jobs",
  "extAccess.brings.none": "None",
  "extAccess.grants.heading": "Who may use it",
  "extAccess.noObjects":
    "This unit registers no permission objects, so there is nothing to grant.",
  "extAccess.roleColumn": "Role",
  "extAccess.action.read": "Read",
  "extAccess.action.create": "Create",
  "extAccess.action.update": "Update",
  "extAccess.action.delete": "Delete",
  "extAccess.matrixCaption": "Who may do what with {object}",
  "extAccess.cell": "Allow {role} to {action} {object}",
  "extAccess.versionSkew":
    "Someone else changed this role while you were looking at it, so your change was not applied. The grants above are now the current ones — make the change again if you still want it.",
  "extAccess.systemRole": "Built-in role",
  "extAccess.nobodyReads":
    "No role holds read on {object}, so every member sees an empty screen where this extension should be. Grant read to at least one role below.",
  "users.empty": "No members yet.",
  "users.adminOnly": "Managing members is available to admins only.",
  "users.inviteTitle": "Invite a member",
  "users.teamsLabel": "Teams",
  "users.noTeamsYet": "No teams yet.",
  "users.teamsTitle": "Teams",
  "users.teamsSub":
    "Who may edit whose records: members of a team edit every teammate's records. Customers, contacts, leads and deals are readable by everyone.",
  "users.teamMembers": "{count} members",
  "users.archiveTeam": "Archive team {name}",
  "users.newTeamLabel": "New team",
  "users.newTeamPlaceholder": "e.g. DACH Sales",
  "users.createTeam": "Create team",
  "users.access.title": "What this member sees",
  "users.access.identity":
    "Reads every contact, company, lead and deal in the organization.",
  "users.access.writesAll": "Edits every record.",
  "users.access.writesTeam":
    "Edits their own records and those of the teams {teams}.",
  "users.access.writesTeamNone":
    "Edits only their own records — not on any team yet.",
  "users.access.writesOwn": "Edits only their own records.",
  "users.access.none": "no access",
  "users.access.read": "read",
  "users.access.write": "write",
  "users.access.delete": "delete",
  "users.access.object.person": "Contacts",
  "users.access.object.organization": "Companies",
  "users.access.object.lead": "Leads",
  "users.access.object.deal": "Deals",
  "users.access.object.project": "Projects",
  "users.access.mask": "{field} is withheld {when}.",
  "users.access.maskAlways": "always",
  "users.access.maskOutside": "on records they may not edit",
  "users.inviteSub":
    "Add someone to this installation and pick the role they start with.",
  "users.membersTitle": "Members",
  "users.membersSub":
    "Everyone who holds a seat here, deactivated accounts included.",
  "users.memberCount.one": "{count} member",
  "users.memberCount.other": "{count} members",
  "users.emailLabel": "New member's email",
  "users.nameLabel": "New member's full name",
  "users.emailPlaceholder": "name@company.com",
  "users.namePlaceholder": "Full name",
  "users.deactivateConfirmTitle": "Deactivate {name}?",
  "users.deactivateConfirmBody":
    "They'll be signed out everywhere and their agent passports revoked immediately. You can reactivate them later, but they'll need to sign in again.",
  "users.deactivateAgentConfirmBody":
    "This is the organization's agent identity. Deactivating it stops every job that runs with nobody behind it, extensions included, until you reactivate it. No person loses access — it signs in nowhere.",
  "users.agentSeat": "Agent",
  "users.agentSeatRole": "Acts under a passport, not a role",
  "users.roleLabel": "Role for the new member",
  "users.invite": "Invite",
  "users.setRole": "Set role…",
  "users.setRoleFor": "Set role for {name}",
  "users.rolesHeld": "Holds {roles}. Choosing one replaces them all",
  "users.deactivate": "Deactivate",
  "users.reactivate": "Reactivate",
  "users.status.active": "Active",
  "users.status.deactivated": "Deactivated",
  "users.status.suspended": "Suspended",
  "users.role.admin": "Admin",
  "users.role.management": "Management",
  "users.role.manager": "Team Lead",
  "users.role.rep": "Member",
  "users.role.read_only": "Read-only",
  "users.role.ops": "Ops",
  "users.link.action": "Get set-password link",
  "users.link.title": "Set-password link for {name}",
  "users.link.pending": "Creating the link…",
  // Two sentences, no dash (VOICE-RULE-5).
  "users.link.body":
    "Send this link to the member over a channel you trust. It works once and is shown only now. Close this and you can create a new one from their row.",
  "users.link.urlLabel": "Set-password link",
  "users.link.copy": "Copy link",
  "users.link.copied": "Copied",
  "users.link.copyFailed":
    "Could not copy automatically. Select the link and copy it.",
  "users.link.expires": "Expires {when}.",
  "users.link.failed":
    "The member was created, but the link could not be. They cannot sign in until you send them one.",
  "users.link.offline":
    "Could not reach the server. Check your connection and try again.",
  "users.link.retry": "Try again",
  "users.link.done": "Done",
  "settings.companyKicker": "Company intelligence",
  "settings.companyTitle": "What Margince knows about your company",
  "settings.companyReadOnly":
    "Read-only view — changing the company profile needs an organization write.",
  "settings.companySub":
    "Keep the shared business context behind drafting, offers, search, and governed agents accurate. Every statement stays tied to who supplied it and where it came from.",
  "settings.companyTrust":
    "Confirmed knowledge only — website text never becomes instructions.",
  "settings.companyConfirmed": "confirmed statements",
  "settings.companyWebsite": "Public company website",
  "settings.companyWebsiteRequired": "Add a company website before refreshing.",
  "settings.companyRefresh": "Refresh from website",
  "settings.companyEssentials": "The three essentials",
  "settings.companyPositioning": "Positioning, buyers, and sales motion",
  "settings.companyIdentity": "Identity and legal details",
  "settings.companyViewSource": "View source",
  "settings.companySave": "Save company context",
  "settings.companySaved": "Saved",
  "settings.companyRefreshUnreadable":
    "We lost track of this website read. Start the refresh again.",
  "settings.companyRefreshStale":
    "The website proposal changed. Review the refreshed comparison before applying it.",
  "settings.companyRefreshReview": "Website comparison",
  "settings.companyRefreshReady": "Review what changed",
  "settings.companyRefreshReading": "Reading and grounding your site…",
  "settings.companyCoverage": "page coverage",
  "settings.companyResolveAll":
    "Choose an outcome for every human-held conflict.",
  "settings.companyApplyRefresh": "Apply selected changes",
  "settings.companySelectChange": "Select the {field} change",
  "settings.companyClass.new": "New",
  "settings.companyClass.machine_change": "Website changed",
  "settings.companyClass.human_conflict": "Needs your decision",
  "settings.companyClass.unchanged": "Unchanged",
  "settings.companyResolution.keep_current": "Keep current",
  "settings.companyResolution.accept_proposal": "Accept website",
  "settings.companyResolution.useValueFor": "Value to keep for {field}",
  "settings.companyResolution.use_value": "Use my edited value",
  "settings.companyManualKicker": "Private, manual setup",
  "settings.companyManualTitle": "Tell Margince the essentials",
  "settings.companyManualSub":
    "Website reading is not enabled for this rollout. These three answers are enough to create useful company context, with no model call and no external request.",
  "settings.companyCreateWorkspace": "Create company context",
  "product.title": "Products",
  "product.settingsSub": "Rate-card entries that offer lines snapshot from.",
  "product.readOnly": "Read-only view — you may not change products.",
  "product.new": "New product",
  "product.edit": "Edit product",
  "product.archive": "Archive product",
  "product.archiveConfirm":
    "Archive this product? Existing offer lines keep their snapshot.",
  "product.name": "Name",
  "product.sku": "SKU",
  "product.description": "Description",
  "product.unit": "Unit",
  "product.unitPrice": "Unit price",
  "product.currency": "Currency",
  "product.taxRate": "Default tax rate %",
  "product.active": "Active",
  "product.activeFilter": "Active only",
  "product.activeFilterAll": "All",
  "product.inactive": "Inactive",
  "product.archived": "Archived",

  "template.title": "Offer templates",
  "template.settingsSub": "Branded DE/EN PDF layouts for offers.",
  "template.readOnly": "Read-only view — you may not change offer templates.",
  "template.new": "New template",
  "template.edit": "Edit template",
  "template.archive": "Archive template",
  "template.archiveConfirm":
    "Archive this template? Offers that reference it fall back to the locale default.",
  "template.name": "Name",
  "template.locale": "Locale",
  "template.isDefault": "Default for locale",
  "template.header": "Header text",
  "template.footer": "Footer text",
  "template.localeFilter": "Locale",
  "template.localeFilterAll": "All locales",
  "template.localeDE": "German (DE)",
  "template.localeEN": "English (US)",

  "tools.title": "Agent tools",
  "tools.sub":
    "The governed surface a passport can call — same inventory an MCP client sees.",
  "tools.egress": "reaches out",
  "tools.scopeAll": "All passports",
  "tools.scopedTo": "Reachable by {label}",
  "tools.unreachable": "scope not granted",

  "aiusage.title": "AI usage & budget",
  "aiusage.withheld":
    "Only an operator can see what the AI runtime spent. The figures cover the whole installation, so they are not shown more widely.",
  "aiusage.sub":
    "Your own bill, made visible — per task and tier, token-denominated.",
  "aiusage.budget": "{spent} of {budget} tokens · {pct}%",
  "aiusage.budgetMeter": "Monthly token budget used",
  "aiusage.band.normal": "normal",
  "aiusage.band.degraded": "economy mode",
  "aiusage.band.queued": "budget reached — background AI queued",
  "aiusage.band.unknown": "unknown budget state",
  "aiusage.col.task": "Task",
  "aiusage.col.tier": "Tier",
  "aiusage.col.calls": "Calls",
  "aiusage.col.cached": "Cached",
  "aiusage.col.tokensIn": "Tokens in",
  "aiusage.col.tokensOut": "Tokens out",
  "aiusage.col.cost": "Est. cost",
  "aiusage.costNote": "Costs are estimates at configured rates.",
  "aiusage.days.show": "Show days",
  "aiusage.days.hide": "Hide days",
  "aiusage.empty": "No AI calls in this window.",
  "aiusage.prevMonth": "Previous month",
  "aiusage.nextMonth": "Next month",

  "aibanner.degraded": "AI running in economy mode.",
  "aibanner.queued": "AI budget reached — background AI is queued.",
  "aibanner.unknown": "AI budget status is not recognized.",
  "aibanner.link": "View usage",
  "aibanner.dismiss": "Dismiss",

  "aicalls.title": "AI call trace",
  "aicalls.withheld":
    "Only an operator can read the per-call trace. It records every model call the installation made, so it is not shown more widely.",
  "aicalls.sub":
    "Every model call — routing identity, tokens, retries, captured payload.",
  "aicalls.col.detail": "Detail",
  "aicalls.expandCall": "Show the attempt trail for {task} at {when}",
  "aicalls.col.when": "When",
  "aicalls.col.task": "Task",
  "aicalls.col.model": "Model",
  "aicalls.col.tokens": "Tokens",
  "aicalls.col.latency": "Latency",
  "aicalls.ms": "{value} ms",
  "aicalls.badge.cacheHit": "cache hit",
  "aicalls.badge.degraded": "degraded",
  "aicalls.badge.retries": "retry ×{count}",
  "aicalls.filter.all": "All tasks",
  "aicalls.loadMore": "Load more",
  "aicalls.empty": "No AI calls recorded yet.",
  "aicalls.detail.identity":
    "Served {served} via {provider} (configured: {configured})",
  "aicalls.detail.source": "Served identity source: {source}",
  "aicalls.detail.context": "Injected context: {scopes}",
  "aicalls.detail.contextNone": "No company context injected",
  "aicalls.detail.attempts": "Attempts",
  "aicalls.detail.request": "Request payload",
  "aicalls.detail.response": "Response payload",
  "aicalls.payload.off":
    "Payload capture is off — set ai.capture_payloads: true in margince.yaml to record request/response content.",
  "aicalls.payload.none": "No payload captured for this call.",

  "aiexport.button": "Export as cert scenario",
  "aiexport.title": "Export run as certification scenario",
  "aiexport.nameLabel": "Scenario name",
  "aiexport.checklist":
    "Secrets were stripped at capture. Personal data was NOT — review and remove PII, then replace sanitized_by before committing this file to the corpus.",
  "aiexport.copy": "Copy YAML",
  "aiexport.copied": "Copied",
  "aiexport.download": "Download .yaml",
  "aiexport.copyFailed": "Copy failed — use the preview or download instead.",
  "aiexport.close": "Close",
  "aiexport.previewLabel": "Scenario preview",
  "aiexport.responseLabel": "Model response",

  "countdown.daysHours": "{days}d {hours}h",
  "countdown.hoursMinutes": "{hours}h {minutes}m",
  "countdown.minutesSeconds": "{minutes}m {seconds}s",
  "countdown.expired": "Expired",

  // Quotas & attainment (RD-T06): human-set revenue targets with
  // server-computed attainment, surfaced under the Reports "Quotas" segment.
  "quotas.tab": "Quotas",
  // The selector panel's own title. The Reports segment picker directly above
  // it already reads "Quotas", so this names what the LIST holds — one row per
  // owner or team carrying a target — rather than repeating the page.
  "quotas.selector.title": "Who has a quota",
  "quotas.sub": "revenue targets — human-set, attainment computed",
  "quotas.role.owner": "Individual quota",
  "quotas.role.team": "Team quota",
  "quotas.periodRange": "{start} – {end}",
  "quotas.empty.title": "No quota set",
  "quotas.empty.body":
    "A quota is a target a human sets — owner or team, period, amount. We don't guess one for you. Set a target to start tracking attainment from closed-won deals.",
  "quotas.empty.cta": "Set a target",
  "quotas.attained": "attained",
  "quotas.closedWon": "Closed-won this period",
  "quotas.target": "Target",
  "quotas.gap": "Gap to target",
  "quotas.baseCurrencyNote":
    "Figures in the organization's base currency ({currency}).",
  "quotas.pace.ahead":
    "Ahead of pace — {pct}% attained vs {pace}% of period elapsed.",
  "quotas.pace.behind":
    "Behind pace — {pct}% attained vs {pace}% of period elapsed.",
  "quotas.pace.met": "Target met — {pct}% attained.",
  "quotas.computed": "computed server-side",
  "quotas.contributing.title": "What counts toward attainment",
  "quotas.contributing.subtitle": "closed-won deals · base value in the period",
  "quotas.contributing.deal": "Deal",
  "quotas.contributing.amount": "Counted amount",
  "quotas.contributing.total": "Counted total",
  "quotas.contributing.caption":
    "Base currency · open / lost / omitted deals excluded",
  "quotas.explain.formula":
    "attainment = Σ(closed-won base value) ÷ target, to the cent",
  "quotas.explain.closedWon":
    "closed-won = {sum} ({count} deals in the period)",
  "quotas.explain.target": "target = {target} (human-set)",
  "quotas.explain.result": "attainment = {sum} ÷ {target} = {pct}%",
  "quotas.explain.exclusions":
    "open / lost / omitted deals are excluded; clean-core only",
  "quotas.scopeNote.title": "What this quota deliberately is",
  "quotas.scopeNote.flag": "flagged, not hidden",
  "quotas.scopeNote.body":
    "The target is human-set — the AI never invents a quota number. Attainment is computed from closed-won base value and is fully auditable. There is no AI-set goal, no forecast-to-quota auto-fill, and no comp/commission engine yet.",
  "quotas.target.title": "Period target",
  "quotas.target.new": "Set a target",
  "quotas.target.edit": "Edit target",
  "quotas.target.save": "Save target",
  "quotas.target.note":
    "Editing writes a human-typed value and logs the change. Attainment recomputes against it.",
  "quotas.target.sideFixed":
    "A quota's owner/team side is fixed — switch it by archiving and recreating.",
  "quotas.side.label": "Assigned to",
  "quotas.side.owner": "Owner",
  "quotas.side.team": "Team",
  "quotas.owner": "Owner",
  "quotas.team": "Team",
  "quotas.pickOwner": "Select an owner…",
  "quotas.pickTeam": "Select a team…",
  "quotas.amountHint": "Whole euros — no decimals",
  "quotas.periodStart": "Period start",
  "quotas.periodEnd": "Period end",
  "quotas.amount": "Target amount",
  "quotas.currency": "Currency",
  "quotas.err.targetZero": "This quota has no target yet",
  "quotas.err.computeFailed": "Attainment couldn't be computed",
  "quotas.err.ownerXorTeam": "Choose exactly one of owner or team.",
  "quotas.archive.title": "Archive quota",
  "quotas.archive.confirm":
    "Archiving drops this quota from the list and stops tracking its attainment. Archived quotas can't be edited.",

  "installationSettings.orgTitle": "Installation",
  "installationSettings.orgSub":
    "What this installation is called, and the zone every reporting period is computed in.",
  "installationSettings.currencyTitle": "Currency",
  "installationSettings.currencySub":
    "The one currency every roll-up converts amounts to.",
  "installationSettings.name": "Organization name",
  "installationSettings.nameHint":
    "Shown wherever the product names your organization.",
  "installationSettings.timezone": "Reporting timezone",
  "installationSettings.timezoneHint":
    "IANA zone name (for example Europe/Berlin). Period boundaries in every report are computed in it, separate from your own display timezone.",
  "installationSettings.baseCurrency": "Base currency",
  "installationSettings.baseCurrencyHint":
    "ISO-4217 code every amount converts to for roll-ups. Changeable until the first deal converts against it.",
  "installationSettings.baseCurrencyLocked":
    "Locked: deals have already converted against this currency, so changing it would re-mean every roll-up built on them.",
  "installationSettings.readOnly":
    "Only an admin or ops can change these settings.",
  "installationSettings.save": "Save",
  // Which vendor this installation's text is sent to. Admin/ops only, on both
  // verbs â see the ai_routing RBAC object.
  "aiRouting.title": "Model routing",
  "aiRouting.sub":
    "Which model serves each tier. Changes take effect without a restart, and every process picks them up within a minute.",
  "aiRouting.unbound":
    "This installation has no models bound, so its AI features are off. A deployment declares its first binding under seeds.ai_routing in margince.yaml.",
  "aiRouting.profile.label": "Location",
  "aiRouting.profile.help":
    "Where inference runs. Sovereign means zero egress: only models on your own hosts, refused at save time rather than at the first call.",
  "aiRouting.profile.eu_hosted": "EU-hosted",
  "aiRouting.profile.sovereign": "Sovereign (no egress)",
  "aiRouting.profile.cloud_frontier": "Cloud frontier",
  "aiRouting.model.label": "Model",
  "aiRouting.save": "Save routing",
  "aiRouting.saving": "Saving the binding…",
  "aiRouting.saved": "Routing saved. Every process is now serving it.",
  "aiRouting.adminOnly": "Only an admin or ops can change model routing.",
  "captureSettings.title": "Enrichment",
  "captureSettings.sub":
    "How captured companies and contacts are enriched after they are created.",
  "captureSettings.autoEnrich.label": "Auto-enrich captured companies",
  "captureSettings.autoEnrich.help":
    "When on, each new company created from captured mail gets an automatic web dossier — its site is read and its profile filled in. Runs under a daily limit.",
  "captureSettings.adminOnly": "Only an admin or ops can change this.",

  "ownDomains.companyTitle": "Company domains",
  "captureExclusions.title": "Keep out of capture",
  "captureExclusions.sub":
    "Addresses and domains whose messages never enter the CRM. Your own rules bind only the mailboxes you connected; the organization's rules bind everyone.",
  "captureExclusions.notRetroactive":
    "Takes effect from the next message. Messages already captured stay.",
  "captureExclusions.empty": "No exclusions.",
  "captureExclusions.scope.user": "Only me",
  "captureExclusions.scope.workspace": "Whole organization",
  "captureExclusions.kind.address": "Address",
  "captureExclusions.kind.domain": "Domain",
  "captureExclusions.scopeLabel": "Applies to",
  "captureExclusions.kindLabel": "Kind",
  "captureExclusions.addLabel": "Exclude an address or a domain",
  "captureExclusions.placeholder.address": "name@example.com",
  "captureExclusions.placeholder.domain": "example.com",
  "captureExclusions.add": "Exclude",
  "captureExclusions.remove": "Capture {value} again",
  "ownDomains.title": "Own email domains",
  "ownDomains.sub":
    "The domains that belong to this company. When colleagues write to each other, that message is not stored. Not even for you.",
  "ownDomains.irreversible":
    "Adding a domain takes effect from the next message. Removing it later resumes capture from that point on. Mail skipped while it was registered is never offered again by any mailbox. Mail already captured stays.",
  "ownDomains.fromCompany": "From the company profile. Change them there:",
  "ownDomains.openCompany": "Open the company profile",
  "ownDomains.empty":
    "No further domains registered. Add one if your company also writes from another domain.",
  "ownDomains.confirmed": "confirmed",
  "ownDomains.candidate": "seen on a connected mailbox, not confirmed yet",
  "ownDomains.add": "Add",
  "ownDomains.addLabel": "Add an own domain",
  "ownDomains.placeholder": "example.com",
  "ownDomains.remove": "Remove {domain}",

  "webhooks.title": "Webhooks",
  "webhooks.readOnly":
    "Read-only view — only an admin or ops can change subscriptions.",
  "webhooks.sub":
    "Outbound subscriptions that receive signed HTTP POSTs for chosen events.",
  "webhooks.new": "New subscription",
  "webhooks.notConfigured":
    "Outbound webhooks are not enabled on this deployment — a signing key must be configured first.",
  "webhooks.state.active": "Active",
  "webhooks.state.paused": "Paused",
  "webhooks.updated": "Updated {date}",
  "webhooks.field.targetUrl": "Target URL",
  "webhooks.field.eventTypes": "Event types",
  "webhooks.field.state": "State",
  "webhooks.edit": "Edit",
  "webhooks.archive": "Archive",
  "webhooks.archiveConfirm":
    "Archiving stops all delivery for this subscription. This can't be undone.",
  "webhooks.rotate": "Rotate secret",
  "webhooks.rotateConfirm.title": "Rotate signing secret?",
  "webhooks.rotateConfirm.body":
    "Confirming invalidates the current secret immediately and then reveals the new secret once. Copy it and update your receiver as soon as rotation completes.",
  "webhooks.secret.title": "Signing secret",
  "webhooks.secret.warning":
    "This secret is shown once and can't be retrieved again. Store it now — deliveries are signed with it.",
  "webhooks.secret.copy": "Copy",
  "webhooks.secret.copied": "Copied",
  "webhooks.secret.copyFailed":
    "Couldn't copy automatically — select and copy the secret manually.",
  "webhooks.secret.done": "Done",
  "webhooks.secret.leaveWarning":
    "Leaving destroys the only copy of this secret. Copy it first.",

  "webhooks.deliveries.show": "View deliveries",
  "webhooks.deliveries.hide": "Hide deliveries",
  "webhooks.deliveries.empty": "No delivery attempts yet.",
  "webhooks.deliveries.deadLetterGroup": "Dead-lettered ({count})",
  "webhooks.deliveries.allGroup": "Other attempts",
  "webhooks.deliveries.column.status": "Status",
  "webhooks.deliveries.column.event": "Event",
  "webhooks.deliveries.column.attempts": "Attempts",
  "webhooks.deliveries.column.lastStatusCode": "Last status",
  "webhooks.deliveries.column.lastError": "Last error",
  "webhooks.deliveries.column.created": "Created",
  "webhooks.deliveries.column.resolved": "Resolved / next retry",
  "webhooks.deliveries.status.pending": "Pending",
  "webhooks.deliveries.status.delivered": "Delivered",
  "webhooks.deliveries.status.retrying": "Retrying",
  "webhooks.deliveries.status.dead_lettered": "Dead-lettered",
  "webhooks.deliveries.replay": "Replay",
  "webhooks.deliveries.replayConfirm.title": "Replay this delivery?",
  "webhooks.deliveries.replayConfirm.body":
    "Re-attempts delivery now, signed with the current secret and a fresh timestamp. It doesn't wait for the next scheduled retry.",
  "reindexbanner.needed": "Reindex needed",
  "reindexbanner.link": "Review in settings",

  "embedreindex.title": "Search index",
  "embedreindex.sub":
    "The embedding store's reindex status — admin/ops only, including viewing it.",
  "embedreindex.withheld":
    "Only an admin or ops can see the search index. Rebuilding it spends tokens for the whole installation, so its status is not shown more widely.",
  "embedreindex.statusIdle": "Up to date",
  "embedreindex.statusNeeded": "Reindex needed",
  "embedreindex.statusReembedding": "Reindexing…",
  "embedreindex.lastProgress": "Last progress {duration} ago",
  "embedreindex.entitiesPending": "{count} entities pending",
  "embedreindex.workspacePending": "{count} pending",
  "embedreindex.reviewCta": "Review & reindex",
  "embedreindex.rebuildCta": "Rebuild index",
  "embedreindex.confirmTitle": "Start the reindex",
  "embedreindex.rebuildTitle": "Rebuild the search index",
  "embedreindex.confirmCta": "Start reindex",
  "embedreindex.rebuildConfirmCta": "Rebuild now",
  "embedreindex.previewLoading": "Estimating scope…",
  "embedreindex.estimateEntities": "Entities to (re)embed:",
  "embedreindex.estimateTokens": "Estimated AI tokens:",
  "embedreindex.estimateCost": "Estimated cost:",
  "embedreindex.estimateQualityHeuristic":
    "Heuristic estimate — a cold work-shape floor, not observed spend.",
  "embedreindex.utilizationTitle": "Budget impact",
  "embedreindex.impact.normal": "normal",
  "embedreindex.impact.degraded": "would enter economy mode",
  "embedreindex.impact.queued": "would be queued",

  "consent.title": "Authorize access",
  "consent.asks": "{client} wants to act in Margince as you.",
  "consent.redirectsTo": "Margince will send the authorization back to {host}.",
  "consent.redirectsToLoopback":
    "That is an address on this computer, and this connection cannot prove which program is listening on it.",
  "consent.lend": "Lend it one of your agent passports",
  "consent.grantedNote":
    "This connection gets exactly the scopes shown — the ones this passport carries.",
  "consent.offline":
    "It will stay connected without asking again, renewing access until you revoke it.",
  "consent.approve": "Authorize",
  "consent.deny": "Deny access",
  "consent.emptyTitle": "You need an agent passport first",
  "consent.emptyBody":
    "A passport is the authority you lend an agent — it never exceeds your own permissions, and you can revoke it at any time. Mint one and we'll bring you back here to finish connecting {client}.",
  "consent.emptyCta": "Mint a passport",
  "consent.expires": "expires {date}",
  "consent.resumeTitle": "Finish connecting {client}",
  "consent.resumeBody":
    "You came here to mint a passport for {client}. Once you have one, pick up where you left off.",
  "consent.resume": "Continue connecting",
  "consent.resumeDismiss": "Cancel this connection",
  "consent.reentering": "Reconnecting…",
  "consent.backToApp": "Back to Margince",
  "consent.staleTitle": "This request has expired",
  // No {client}: this card renders without the consent-request fetch, so the
  // client's name is not available to name here.
  "consent.staleBody":
    "The connection request is no longer valid. Go back to the app you were connecting and start again — reloading this page will not help.",
  "consent.unlendableTitle": "That passport can no longer be lent",
  "consent.unlendableBody":
    "The passport you chose for {client} was revoked, expired, or is already bound to another connection. Choose a different one below.",
  "consent.invalidTitle": "This connection request could not be completed",
  "consent.invalidBody":
    "This installation will not authorize the request as it stands — the app may no longer be registered here. Go back to the app you were connecting and start again.",
  "consent.unnamedPassport": "Unnamed passport ({id})",
  "person.thin.title": "What we know so far",
  "person.thin.known":
    "We have {what} for {name}, but nobody here has a recorded exchange with them yet.",
  "person.thin.remediation.capture":
    "Connect the mailbox that writes to them, and this page fills itself in — every field with the source it came from.",
  "person.thin.remediation.employer":
    "Add their employer and Margince can read that company's site for their role.",
  "person.thin.logFirst": "Log the first interaction",
  "person.timeline.all": "All",
  "person.timeline.messages": "Messages",
  "person.timeline.meetings": "Meetings",
  "person.timeline.tasks": "Tasks",
  "person.enriched.title": "What Margince read",
  "person.enriched.sub":
    "Each value with the text it was read from. Correct one and the correction stands.",
  "person.enriched.field.title": "Title",
  "person.enriched.field.phone": "Phone",
  "person.enriched.field.role": "Role",
  "person.enriched.field.linkedin": "LinkedIn",
  "person.enriched.field.org_name": "Company",
  "person.enriched.readFrom": "Read from {source} on {when}",
  "person.enriched.correctedByYou": "Corrected by you",
  "person.enriched.confirmed": "Confirmed",
  "person.enriched.correct": "Correct",
  "person.enriched.confirm": "That is right",
  "person.enriched.save": "Save the correction",
  "person.enriched.cancel": "Cancel",
  "person.graph.loading": "Reading the network around this contact…",
  "person.graph.routeTitle": "The warmest way in",
  "person.graph.routeDirect": "{name} already corresponds with them.",
  "person.graph.routeVia":
    "{name} corresponds with {through} at the same company.",
  "person.graph.noRoute":
    "Nobody here corresponds with them or with anyone at their company yet.",
  "person.graph.direct": "Who knows them",
  "person.graph.directSub":
    "Colleagues who have corresponded with this contact themselves.",
  "person.graph.noDirect": "Nobody here has corresponded with them.",
  "person.graph.account": "At the same company",
  "person.graph.accountSub":
    "Their colleagues, and who here is warmest with each.",
  "person.graph.noAccount": "No other contacts on record at their company.",
  "person.graph.omitted":
    "Part of this is hidden because you do not have the grant for it.",
  "person.graph.noEdge": "No recorded correspondence with {name}.",
  "person.graph.withColleague": "with {name}",
  "person.graph.withContact": "with this contact",
  "person.graph.counts":
    "{total} interactions in 90 days · {inbound} in, {outbound} out",
  "person.graph.countsOnly":
    "Counts only — the messages themselves stay on the timeline.",
  "person.graph.untitledMessage": "Message with no subject",
  "person.graph.dropped": "{count} more not shown.",
  "person.change.repliedAfterGap": "They replied after {days} quiet days.",
  "person.change.wentQuiet": "Nothing has happened for {days} days.",
  "person.change.warmed": "The relationship moved from {from} to {to}.",
  "person.change.cooled": "The relationship fell from {from} to {to}.",
  "person.band.none": "no contact",
  "person.band.weak": "weak",
  "person.band.moderate": "moderate",
  "person.band.strong": "strong",
  "person.pulse.title": "Relationship",
  "person.pulse.warmestIs": "{name} has the warmest relationship here.",
  "person.pulse.nobodyYet":
    "Nobody here has a recorded exchange with them yet.",
  "person.pulse.lastInbound": "They last wrote",
  "person.pulse.lastOutbound": "We last wrote",
  "person.pulse.neverInbound": "never",
  "person.pulse.neverOutbound": "never",
  "person.pulse.why": "How this is computed",
  "person.pulse.arithmetic":
    "Score {score}/100 = 100 x recency {recency} x frequency {frequency} x reciprocity {reciprocity}. Computed at read from captured cadence, never stored.",
  "person.identity.title": "Identity",
  "person.identity.email": "Email",
  "person.identity.phone": "Phone",
  "person.identity.currentRole": "Current role",
  "person.identity.buyingRole": "Buying role",
  "person.career.title": "Former roles",
  "person.consent.title": "Outbound guard",
  "person.consent.allowed": "Allowed: {purposes}",
  "person.consent.noneGranted":
    "No purpose is granted, so outbound stays blocked.",
  "person.consent.blocked": "Blocked: {purposes}",
  "person.network.title": "Who here knows them",
  "person.network.twoWay": "{count} two-way exchanges in 90 days",
  "person.network.oneSided": "{count} interactions in 90 days, one-sided",
  "person.network.replied": "replied {when}",

  // The person record page V2 (ADR-0096). The strip, rail and card words are
  // split by SLOT rather than by sentence, because the same word means
  // different things in different slots: "Never" under a direction is an
  // absence of correspondence, "None" under a meeting is an absence of a
  // booking, and German renders them differently.
  "person.page.loading": "Loading…",
  "person.page.notOpened": "This contact could not be opened.",
  "person.page.asideLabel": "Relationship context",
  "person.page.buyingRole": "Buying role",
  "person.page.owner": "Owner",
  "person.page.ownerAssigned": "Assigned",
  "person.page.ownerUnassigned": "Unassigned",
  "person.page.linkedin": "LinkedIn",
  // The rail's own details grid: the contact's own fields, at a glance above
  // the six relationship sections below it.
  "person.rail.detailsTitle": "Details",
  "person.rail.contactMethodImmutable":
    "Set when this contact was added. Email and phone cannot be changed here.",
  "person.rail.archivedReadOnly":
    "This contact is archived. Restore them to change anything here.",
  // Fired when an employment row's version could not be read back before a
  // write — the row is not saved unpinned, so the reader is told to reload
  // rather than left to think the edit landed.
  "person.rail.employmentVersionUnresolved":
    "This row's current version could not be read back to save against. Reload and try again.",
  // The employers section: every employment edge this person holds, current
  // one first — a person can work at more than one company at once.
  "person.rail.employmentTitle": "Companies",
  "person.rail.noEmployment": "No employment on record.",
  "person.rail.addEmployment": "Add company",
  "person.rail.employer": "Employer",
  "person.rail.allOrgsConnected":
    "Every match is already connected to this person.",
  "person.rail.isCurrentEmployer": "This is their current employer",
  "person.rail.markEnded": "Mark as ended",
  "person.rail.removeEmploymentTitle": "Remove this company connection?",
  "person.rail.removeEmploymentBody":
    "The link to {org} and the history hanging off it disappear, and this cannot be undone. {org} itself stays. If they simply left, mark it ended instead.",
  "person.timeline.empty": "Nothing has been logged with them yet.",
  "person.deals.empty": "They are not recorded on any deal.",
  "person.deals.untitled": "Untitled deal",
  "person.deals.noStage": "No stage yet",
  "person.meetings.next": "Next meeting",
  "person.meetings.past": "Meetings so far",
  "person.meetings.noneBooked": "Nothing is booked with them.",
  "person.meetings.noneLogged": "No meeting with them has been logged.",
  "person.meetings.untitled": "Untitled meeting",
  "person.meetings.participants": "In the room",
  "person.documents.empty": "No file has been filed against this contact.",
  "person.research.empty": "Nothing has been researched about them yet.",
  "person.research.fields": "Enrichment evidence",
  "person.research.fieldsEmpty": "No enriched field carries evidence yet.",
  "person.research.capturedBy": "Captured by",
  "person.action.email": "Email",
  // The lead verb when the record leaves the transport open: either the
  // composer will ask which way to send, or there is no way to send at all.
  "person.action.write": "Write",
  // The lead verb when a chat channel is the ONLY way to reach them. The
  // provider is named by the transport directory, so an extension unit this
  // build has never heard of still reads as itself.
  "person.action.messageOn": "Message on {transport}",
  // Why the lead verb is refused, in two sentences that are never merged: no
  // way to reach them, and consent that says not to.
  "person.action.noTransport": "No address, and no conversation to reply to.",
  "person.action.consentRefused":
    "No purpose currently permits writing to them.",
  "person.action.call": "Call",
  "person.action.meetings": "See meetings",
  "person.action.addTask": "Add task",
  "person.action.research": "Research",
  "person.action.more": "More actions",

  "person.strip.lastInbound": "Last inbound",
  "person.strip.lastOutbound": "Last outbound",
  "person.strip.reciprocity": "Reciprocity",
  "person.strip.openDeal": "Open deal",
  "person.strip.nextMeeting": "Next meeting",
  "person.strip.consent": "Consent",
  "person.strip.never": "Never",
  "person.strip.today": "Today",
  "person.strip.yesterday": "Yesterday",
  "person.strip.days": "{count} days",
  "person.strip.inOut": "{inbound} in · {outbound} out",
  "person.strip.noOpenDeal": "No open deal",
  "person.strip.noMeeting": "None",
  "person.consent.allowedWord": "Allowed",
  "person.consent.blockedWord": "Blocked",
  "person.consent.unknownWord": "Unknown",

  "person.today.heading": "Today with {name}",
  "person.today.source": "{count} source",
  "person.today.sources": "{count} sources",
  "person.today.updated": "Updated {when}",
  "person.today.freshToday": "today",
  "person.today.freshYesterday": "yesterday",
  "person.today.freshDaysAgo": "{count} days ago",

  "person.brief.title": "Relationship brief",
  "person.brief.reading": "Reading the relationship…",
  "person.brief.empty":
    "Nothing has been captured yet that this brief could be written from.",
  "person.brief.sourceActivity": "Conversation",
  "person.brief.sourceDeal": "Deal notes",

  "person.matters.title": "What matters to {name}",
  "person.matters.priorities": "Priorities",
  "person.matters.objections": "Objections",
  "person.matters.successCriteria": "Success criteria",
  "person.matters.absent": "Nothing captured yet",

  "person.commercial.title": "Open deal & buying role",
  "person.commercial.withheld":
    "You do not have access to this person's deals.",
  "person.commercial.noDeal": "No open deal.",
  "person.commercial.closes": "closes {date}",
  "person.commercial.committee": "Buying committee",
  "person.commercial.openDeal": "Open deal",

  "person.loops.title": "Commitments & open loops",
  "person.loops.empty":
    "Nothing has been promised or asked in the captured conversations.",
  "person.loops.ours": "You",
  "person.loops.question": "Open question",
  "person.loops.overdue": "overdue {count} days",
  "person.loops.due": "due {when}",
  "person.loops.dueToday": "today",
  "person.loops.dueTomorrow": "tomorrow",
  "person.loops.dueInDays": "in {count} days",
  "person.loops.waiting": "waiting",
  "person.loops.open": "open",

  "person.memory.title": "Conversation memory",
  "person.memory.empty": "Nothing captured on this channel yet.",
  "person.memory.all": "All",
  "person.memory.email": "Email",
  "person.memory.meetings": "Meetings",
  "person.memory.calls": "Calls",
  "person.memory.notes": "Notes",
  "person.memory.channelEmail": "Email",
  "person.memory.channelMeeting": "Meeting",
  "person.memory.channelCall": "Call",
  "person.memory.channelNote": "Note",
  "person.memory.channelMessage": "Message",
  "person.memory.channelTask": "Task",
  "person.memory.replied": "Replied",
  "person.memory.unanswered": "Unanswered",

  "person.rail.reviewFirst": "Review first",
  "person.rail.blocked": "Blocked",
  "person.rail.ready": "Ready",
  "person.rail.pulseTitle": "Relationship pulse",
  "person.rail.explain": "Explain",
  "person.rail.direction": "Direction",
  "person.rail.twoWay": "Two-way",
  "person.rail.oneSided": "One-sided",
  "person.rail.lastReply": "Last reply",
  "person.rail.coverage": "Coverage",
  "person.rail.colleagueOne": "1 colleague",
  "person.rail.colleagues": "{count} colleagues",
  "person.rail.trend": "Trend",
  "person.rail.noInbound": "No inbound",
  "person.rail.cooling": "Cooling",
  "person.rail.warming": "Warming",
  "person.rail.overall": "Overall",
  "person.rail.thin": "Thin",
  "person.rail.atRisk": "At risk",
  "person.rail.strong": "Strong",
  "person.rail.whoKnows": "Who knows {name}",
  "person.rail.nobodyYet": "Nobody here has corresponded with them yet.",
  "person.rail.exchanges": "{count} exchanges",
  "person.rail.signals": "Signals & risks",
  "person.rail.noSignals": "Nothing stands out on this relationship.",
  "person.rail.noReplyDays": "No reply for {count} days",
  "person.rail.repliedDaysAgo": "Replied {count} days ago",
  "person.rail.singleThreaded": "Single-threaded on this deal",
  "person.rail.noMeetingBooked": "No next meeting booked",
  "person.rail.consentTitle": "Consent & channels",
  "person.rail.email": "Email",
  "person.rail.phone": "Phone",
  "person.rail.noEmailAddress": "No address on file",
  "person.rail.noPhoneNumber": "No number on file",
  "person.rail.channelNotDeliverable": "Not deliverable",
  "person.rail.recentActivity": "Recent activity",
  "person.rail.nothingCaptured": "Nothing captured yet.",
  "person.rail.viewAllActivity": "View all activity",
  "person.drawer.close": "Close",
  "person.composer.title": "Draft follow-up · {name}",
  "person.composer.to": "To",
  "person.composer.transport": "How to send",
  "person.composer.transportEmail": "Email",
  "person.composer.toConversation": "Continues your {transport} conversation",
  "person.composer.subject": "Subject",
  "person.composer.bcc": "Bcc",
  "person.composer.bccPlaceholder":
    "One address per line — they receive the message and no other recipient sees them",
  "person.composer.body": "Message",
  "richtext.bold": "Bold",
  "richtext.italic": "Italic",
  "richtext.bulletList": "Bulleted list",
  "richtext.numberList": "Numbered list",
  "richtext.link": "Link",
  "richtext.linkPrompt": "Web address for this link (leave empty to remove it)",
  "person.composer.drafting": "Writing a draft…",
  "person.composer.why": "Why this draft",
  "person.composer.consentUnknown":
    "No consent decision is recorded for this channel.",
  "person.composer.sendNote":
    "Pressing send delivers this message from your own mailbox.",
  "person.composer.purpose": "Consent purpose",
  "person.composer.blockedLead":
    "This message cannot go out under this purpose.",
  "person.composer.blockedRewrite":
    "A message sent under another purpose has to BE that kind of message — relabelling this one does not make it so.",
  "person.composer.blockedRecordConsent":
    "If you have a basis for writing, record the consent decision on their contact record.",
  "person.composer.consentPickPurpose":
    "Choose what this message is for — consent is decided per purpose.",
  "person.composer.intent": "What should it be about?",
  "person.composer.intentHint":
    "Optional — e.g. ask for a date in the first week of September",
  "person.composer.draftWithAi": "Draft with AI",
  "person.composer.intentAgenda": "propose an agenda for the upcoming meeting",
  "person.composer.intentReply": "reply to their last message",
  "person.composer.intentCommitment": "deliver what we promised them",
  "person.composer.intentFollowUp": "follow up — it has gone quiet",
  "person.composer.send": "Send",
  "person.composer.sending": "Sending…",
  "person.composer.sent": "Sent",
  "person.composer.aiDisclosure": "AI-assisted draft · review every word",
  "person.research.title": "Deep research · {name}",
  "person.research.publicOnly": "Public sources only",
  "person.research.running": "Reading public sources…",
  // Deep research and bought contact data are two capabilities, and this
  // sentence sits directly under the bought data on the same drawer. It names
  // the RESEARCH provider for that reason: "data provider" is the licensed
  // contact-data vocabulary (provider.profile.*), and using it here told a
  // reader nothing was connected while eight purchased claims sat above it.
  "person.research.notConnected":
    "No research provider is connected, so no public source has been read for them. That is separate from any bought contact data above — Margince never researches a person on its own authority, and deep research needs a licensed provider that carries the lawful basis for it.",
  "person.research.staged":
    "Research is staged. Nothing changes {name}'s record until you review and save.",
  "person.research.stats": "{sources} sources read · {claims} cited claims",
  "person.research.dismiss": "Dismiss",
  "person.research.discard": "Discard",
  "person.research.save": "Review & save {count} claims",
  "person.research.evidenceOrOmit":
    "AI-assisted · evidence-or-omit · public information only",
  "person.meeting.title": "Meeting brief",
  "person.meeting.loading": "Assembling the brief…",
  "person.meeting.assembledNow": "Assembled just now, from the latest data",
  "person.meeting.header": "At a glance",
  "person.meeting.goal": "Goal for this meeting",
  "person.meeting.attendees": "Attendees",
  "person.meeting.commitments": "Open commitments",
  "person.meeting.deal_state": "Where the deal stands",
  "person.meeting.risks": "Risks and watch-outs",
  "person.meeting.talking_points": "Suggested talking points",
  "person.meeting.company_context": "Company context",

  "co.strip.healthSummary": "Health",
  "co.strip.healthSummary.failingOf": "{failing} of {rated} at risk",
  "co.strip.healthSummary.of": "{rated} of 3 rated",
  "today.source.suggestions": "the advice",

  // The licensed data provider (ADR-0101). Two surfaces share this
  // vocabulary — the Settings card and the person page — so a state reads
  // the same wherever it appears.
  "provider.title": "Contact data",
  "provider.readOnly":
    "Read-only view — connecting a provider spends money, so it is an admin or ops action.",
  "provider.sub":
    "Buy verified contact details for the people in your CRM. You pay the provider in credits; what you spend here is shown below.",
  "provider.notConfigured":
    "No data provider is available in this installation. Nothing is being bought and nothing can be.",
  "provider.status.connected": "Connected",
  "provider.status.disconnected": "Not connected",
  "provider.status.validating": "Checking the key…",
  "provider.status.invalidCredentials": "The key was refused",
  "provider.status.insufficientCredits": "Out of credits",
  "provider.status.rateLimited": "Rate limited",
  "provider.status.providerError": "The provider is having trouble",
  "provider.connect": "Connect",
  "provider.reconnect": "Replace the key",
  "provider.apiKey": "API key",
  "provider.apiKeyHint":
    "Sealed as soon as it is verified. It is never shown again, and never leaves this installation except to the provider.",
  "provider.apiKeyStored": "Replace the API key",
  "provider.apiKeyReplaceHint":
    "A key is stored and in use. It cannot be shown again, so this box stays empty — paste a new one only if you are replacing it.",
  "provider.apiKeyReplacePlaceholder":
    "Paste a new key to replace the stored one",
  "provider.connectConfirm.title": "Connect this data provider?",
  "provider.connectConfirm.body":
    "The key is checked against the provider before anything is saved. Once connected, enriching a contact spends your credits.",
  "provider.disconnect": "Disconnect",
  "provider.disconnectConfirm.title": "Disconnect the provider?",
  "provider.disconnectConfirm.body":
    "New lookups stop immediately and the key is destroyed. Data already bought stays on your records — disconnecting is not deleting.",
  "provider.deleteData": "Delete bought data",
  "provider.deleteDataConfirm.title":
    "Delete everything bought from this provider?",
  "provider.deleteDataConfirm.body":
    "Every value this provider supplied is removed from every contact. What you spent stays in your records; the data does not. This cannot be undone.",
  "provider.deleteDataConfirm.typed": "Type the provider's name to confirm",
  "provider.autoEnrich": "Enrich new contacts automatically",
  "provider.autoEnrichHint":
    "When somebody adds a contact by hand, buy their details straight away.",
  "provider.credits": "Credits left with the provider",
  "provider.credits.none": "The provider has not told us a balance yet.",
  "provider.credits.notConnected":
    "Connect a key to see what credit you have with the provider.",
  "provider.constraints": "Limits in force",
  "provider.spend": "What we have used",
  "provider.spend.hint":
    "Our own record of what enrichment consumed. Not the provider's invoice — the same credits can be spent through their app, so the two figures differ legitimately.",
  "provider.spend.thisMonth": "This month",
  "provider.spend.month": "Month",
  "provider.spend.pool": "Pool",
  "provider.spend.chargedHead": "Credits",
  // "Held" rather than "unknown": the column holds credits whose outcome the
  // provider never reported, which is what a human reconciles against the
  // invoice. Kept out of the Credits column on purpose.
  "provider.spend.heldHead": "Held",
  "provider.spend.runsHead": "Lookups",
  "provider.spend.none": "Nothing has been bought yet.",
  "provider.mode": "When to enrich",

  // The person page's section. The three "nothing here" states are three
  // different sentences on purpose: only one of them is something the
  // reader can act on.
  "provider.profile.title": "Bought contact data",
  "provider.profile.notConnected":
    "No data provider is connected, so nothing has been bought.",
  "provider.profile.notEligible":
    "This contact is not eligible — they have objected, or the record is archived.",
  "provider.profile.neverRun": "Nobody has looked this contact up yet.",
  "provider.profile.queued": "Queued",
  "provider.profile.inProgress": "Looking them up…",
  "provider.profile.completed": "Found",
  "provider.profile.noMatch": "The provider had nothing for this contact.",
  "provider.profile.stale":
    "Bought earlier. The provider is no longer connected, so this cannot be refreshed.",
  "provider.profile.invalidCredentials":
    "The provider refused our key, so this lookup could not run.",
  "provider.profile.insufficientCredits":
    "Not bought: the credit budget for this month is spent.",
  "provider.profile.rateLimited":
    "Not bought: the provider asked us to slow down.",
  "provider.profile.providerError": "The provider could not answer.",
  "provider.profile.submissionUnknown":
    "We never learned how this lookup ended. It may have been charged for.",
  "provider.profile.claimsUnwritten":
    "Paid for, but the details never reached this record. Nobody has to hunt for them — this is the gap.",
  "provider.profile.enrichNow": "Look this contact up",
  "provider.profile.emails": "Email addresses",
  "provider.profile.emailType.provider": "{type}, as the provider labelled it",
  "provider.profile.emailType.requested":
    "{type}, because that is what we asked for",
  "provider.profile.mobiles": "Mobile numbers",
  "provider.profile.confidence": "{percent}% confidence",
  "provider.profile.linkedin": "LinkedIn",
  "provider.profile.employment": "Current role",
  "provider.profile.jobHistory": "Earlier roles",
  "provider.profile.location": "Location",
  "provider.profile.departments": "Departments",
  "provider.profile.seniorities": "Seniority",
  "provider.profile.notRequested":
    "Not asked for: {categories}. A blank here means nobody bought it, not that the provider had nothing.",

  // The predicate builder (AC-filters-and-views-3/4). Operator labels are keyed
  // per reading rather than per symbol: the same `gte` is "on or after" a date
  // and "at least" a quantity, and one label for both would send a reader
  // looking for a calendar on a score.
  "filters.joinAll": "ALL \u00b7 AND",
  "filters.joinAny": "ANY \u00b7 OR",
  "filters.joinLabel": "How this group joins its clauses",
  "filters.removeGroup": "Remove group",
  "filters.addGroup": "Add group",
  "filters.addClause": "Add clause",
  "filters.emptyGroup":
    "No clauses yet \u2014 an empty group matches nothing, so add one.",
  "filters.field": "Field",
  "filters.choosePlaceholder": "Choose a field",
  "filters.customBadge": "custom field",
  "filters.operator": "Operator",
  "filters.value": "Value",
  "filters.values": "Values",
  "filters.addValue": "Add a value",
  "filters.removeClause": "Remove the {field} clause",
  "filters.existsLabel": "Whether the field has a value",
  "filters.hasValue": "has a value",
  "filters.isEmpty": "is empty",
  "filters.yes": "yes",
  "filters.no": "no",
  "filters.op.eq": "is",
  "filters.op.neq": "is not",
  "filters.op.in": "is any of",
  "filters.op.contains": "contains",
  "filters.op.exists": "has a value",
  "filters.op.afterDate": "is after",
  "filters.op.onOrAfterDate": "is on or after",
  "filters.op.beforeDate": "is before",
  "filters.op.onOrBeforeDate": "is on or before",
  "filters.op.moreThan": "is more than",
  "filters.op.atLeast": "is at least",
  "filters.op.lessThan": "is less than",
  "filters.op.atMost": "is at most",

  // The Filters & views screen's own chrome. The match line is keyed per object
  // because "3 contacts match" and "3 companies match" are different sentences in
  // every language, and a shared "{count} match" would make the object a
  // placeholder that some grammars cannot place.
  "filters.title": "Filters & views",
  "filters.subtitle":
    "Build a filter, watch what it selects, and save it as a view.",
  "filters.objectLabel": "Which records to filter",
  "filters.tab.contacts": "Contacts",
  "filters.tab.companies": "Companies",
  "filters.tab.deals": "Deals",
  "filters.builderTitle": "Filter",
  "filters.dynamic": "Dynamic \u2014 recomputes on every event",
  "filters.matchContacts": "{count} contacts match",
  "filters.matchCompanies": "{count} companies match",
  "filters.matchDeals": "{count} deals match",
  "filters.noFilterYet": "Add a clause to see what it selects",
  "filters.loadingVocabulary": "Loading the fields you can filter on\u2026",
  "filters.noFields": "No filterable fields for this record type.",
  "filters.resultsTitle": "Matching records",
  "filters.resultsCaption":
    "The first page of matches — enough to check the filter, not the whole selection.",
  "filters.noMatches": "No records match this filter.",
  "filters.loadView": "Load a saved filter",
  "filters.pickRecord": "Choose one",
  "filters.loadingRecords": "Loading choices…",
  "filters.saveList": "Save as list",
  "filters.saveListTitle": "Save this filter as a dynamic list",
  "filters.listName": "List name",
  "filters.saveListConfirm": "Create list",
  "filters.exportCsv": "Export CSV",
  "filters.exportJson": "Export JSON",

  // The release gate (src/screens/releaseskew.tsx). It renders instead of the
  // app when this bundle and the api come from different releases, so the copy
  // has two readers at once: the person who just wants in, and the operator who
  // has to fix it. The first sentence is for the first, the last for the second.
  "release.skewTitle": "This installation is part-way through an update",
  "release.skewBody":
    "The app in your browser and the server behind it come from different releases, so nothing here works reliably. Reload to get the current version. If this message stays, tell whoever operates this installation: every part of it has to run the same release.",
  "release.skewVersions": "app {app} · server {server}",
  "release.skewReload": "Reload",

  // The queue behind "send later". Every sentence here is addressed to ONE
  // person: the list is the sender's own, so there is no "a teammate scheduled
  // this" reading to write for.
  //
  // The verb is "withdraw", not "delete" or "cancel". Nothing was transmitted
  // and nothing reaches the timeline, so there is no send to cancel and no
  // record to delete — the rep is taking a message back before it goes, and
  // "withdraw" is the only one of the three that says so.
  "nav.scheduled": "Scheduled messages",
  "sched.sub":
    "Messages you have written that have not gone out yet. Only you can see them.",
  "sched.empty": "You have not scheduled a message yet.",
  "sched.group.held": "Stopped, waiting on you",
  "sched.group.heldEmpty": "Nothing has been stopped.",
  "sched.group.waiting": "Waiting to send",
  "sched.group.waitingEmpty": "Nothing is waiting to send.",
  "sched.group.closed": "No longer waiting",
  "sched.group.closedEmpty": "Nothing has gone out or been withdrawn yet.",
  "sched.status.scheduled": "Waiting",
  // "Released" is the wire's word for the step between waiting and sent: the
  // activity and the delivery exist and the provider has not answered yet. To a
  // rep that is a message on its way out, and there is nothing left to do to it.
  "sched.status.released": "Going out",
  "sched.status.sent": "Sent",
  "sched.status.cancelled": "Withdrawn",
  "sched.status.held": "Stopped",
  "sched.held.consentWithdrawn":
    "A recipient withdrew their consent after you scheduled this. It will not send until you write to them under a purpose they have agreed to.",
  "sched.held.senderInactive":
    "Your seat or your mailbox changed after you scheduled this, so it cannot be sent as you.",
  "sched.held.missedWindow":
    "Its moment passed while nothing was running, and it is now too late to be the message you wrote. Move it or withdraw it.",
  "sched.held.timerExhausted":
    "The job that wakes this message ran out of attempts. Move it to a new moment to try again.",
  "sched.held.sendRefused":
    "A check refused this message when it came due. Nothing was sent.",
  "sched.inZone": "in {zone}",
  "sched.recipientsUnknown": "No recipient on this message",
  "sched.recipientsMore": "{first} and {count} more",
  "sched.move": "Change moment",
  "sched.moveTo": "New moment for “{subject}”",
  "sched.moveSave": "Move it",
  "sched.moveCancel": "Leave it",
  "sched.withdraw": "Withdraw",
  "sched.withdrawTitle": "Withdraw this message?",
  "sched.withdrawBody":
    "“{subject}” will not be sent, and nothing will reach the timeline. Writing it again means composing it from scratch.",
  "sched.withdrawConfirm": "Withdraw it",
  "sched.skew":
    "This list is out of date: the message you acted on had already gone, been withdrawn, or been moved somewhere else. Read the list again.",
  "sched.reload": "Read it again",
} as const;

export type MessageKey = keyof typeof en;
