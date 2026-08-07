import type { MessageKey } from "./en";

// German catalog — the A24 default locale. `satisfies` forces exact key
// parity with en at compile time; i18n.test.ts re-checks it at runtime so a
// build without typechecking still fails loudly. Register: informal "du",
// natural spoken-adjacent German, no translationese, no corporate filler.
export const de = {
  "app.title": "Margince Design-Tokens",
  "app.subtitle":
    "Ledger Green (ADR-0040) — die kanonischen Werte spiegeln die Spec-Mockups; Tests pinnen sie.",
  "theme.toDark": "Dunkles Design",
  "theme.toLight": "Helles Design",
  // Die Design-Zeile im Kontomenü: worauf es JETZT steht, und die Aktion, die die
  // Zeile für einen Screenreader benennt. Die beiden Labels darüber bleiben die
  // Namen des reinen Icon-Schalters auf Anmeldung und Onboarding, wo kein Satz
  // in die Zeile passt.
  "theme.light": "Hell",
  "theme.dark": "Dunkel",
  "theme.switchToDark": "Auf dunkles Design umschalten",
  "theme.switchToLight": "Auf helles Design umschalten",

  "section.surfaces": "Flächen",
  "section.accentAi": "Akzent & KI",
  "section.text": "Text",
  "section.status": "Status",
  "section.typeRamp": "Schriftgrößen",
  "section.trust": "Vertrauens-Primitive (B-EP09.3a)",

  "type.display": "Display — Outfit 600",
  "type.body": "Fließtext — DM Sans 400, die Standard-Leseschrift.",
  "type.mono": "Mono — JetBrains Mono, Evidenz-Snippets und IDs.",

  "trust.accept": "Übernehmen",
  "trust.edit": "Bearbeiten",
  "trust.dismiss": "Verwerfen",
  "trust.save": "Speichern",
  "trust.typedByYou": "von dir eingetragen",
  "trust.typedByHuman": "von einer Person eingetragen",
  "trust.typedByPrefix": "eingetragen von",
  "trust.sourceUnknown": "Herkunft nicht erfasst",
  "trust.agentTag": "Agent: {agent}",
  "trust.connectorTag": "über {connector}",
  "trust.dismissed": "Vorschlag verworfen.",
  "trust.stagedProposal": "vorgemerkter Vorschlag",
  "trust.resolvedValue": "übernommener Wert",
  "trust.editValue": "{description} bearbeiten",
  "trust.evidenceFrom": "Beleg von {source}",

  "history.created": "— angelegt —",
  "history.oldValue": "Vorheriger Wert",
  "history.newValue": "Neuer Wert",
  "history.cleared": "— geleert —",
  "history.passport": "Agent-Passport",
  "history.empty": "Keine Änderungen erfasst",
  "history.onBehalfOf": "im Auftrag von {name}",
  "history.fieldEmpty":
    "Beim Anlegen gesetzt und nie geändert — der Audit-Log verzeichnet keine Bearbeitungen. Ein leerer Verlauf ist ehrlich, keine Lücke.",
  "history.filterEmpty": "Keine Änderungen entsprechen diesem Filter.",
  "history.clearFilter": "Filter zurücksetzen",
  "history.allFields": "Alle Felder",
  "history.actorAll": "Alle",
  "history.actorHuman": "Mensch",
  "history.actorAgent": "Agent",
  "history.tabChanges": "Änderungen",
  "history.tabFields": "Feldverlauf",

  "confidence.high": "hoch",
  "confidence.med": "mittel",
  "confidence.low": "niedrig",

  "autonomy.auto": "automatisch",
  "autonomy.confirm": "erst bestätigen",

  "nav.home": "Start",
  "nav.contacts": "Kontakte",
  "nav.companies": "Firmen",
  "nav.leads": "Leads",
  "nav.deals": "Pipeline",
  "nav.tasks": "Aufgaben",
  "nav.inbox": "Freigaben",
  "nav.reports": "Berichte",
  "nav.ai": "Margince fragen",
  "nav.settings": "Einstellungen",
  "nav.design": "Designsystem",
  "nav.automations": "Automatisierungen",
  "nav.group.records": "Datensätze",
  "nav.group.work": "Arbeit",
  "nav.group.intelligence": "Auswertung",
  "nav.dedupe": "Duplikate",
  "nav.products": "Produkte",
  "nav.offerTemplates": "Angebotsvorlagen",
  "nav.offers": "Angebot",
  "nav.share": "Freigabe",
  "nav.search": "Suchergebnisse",

  "shell.railAria": "Hauptnavigation",
  "shell.logoAria": "Margince",
  "shell.search": "Suche",
  "shell.searchHint": "Suchen oder Befehl ausführen",
  "shell.signOutAria": "Abmelden",
  "shell.collapse": "Seitenleiste einklappen",
  "shell.expand": "Seitenleiste ausklappen",
  "shell.accountAria": "Konto",
  "shell.theme": "Design",
  "shell.views": "Ansichten",
  "shell.more": "Mehr",
  "agent.title": "Margince KI",
  "agent.configured": "Konfiguriert",
  "agent.exampleActivity": "4 neue Kontakte werden angereichert",
  "agent.exampleRouting": "Lokal + Cloud",
  "agent.exampleCost": "€2,41 heute",
  "agent.fixture": "Beispieldaten",
  "locale.name.en": "English",
  "locale.name.de": "Deutsch",
  "locale.name.vi": "Tiếng Việt",
  "locale.switchLabel": "Sprache",

  "screen.pending":
    "Noch nicht gebaut — diese Oberfläche kommt mit ihrem Build-Ticket.",

  "search.title": "Suche",
  "search.placeholder":
    "Personen, Firmen, Deals, Aktivitäten, Leads durchsuchen…",
  "search.empty": "Keine Treffer für „{q}“.",
  "search.group.person": "Personen",
  "search.group.organization": "Organisationen",
  "search.group.deal": "Deals",
  "search.group.activity": "Aktivitäten",
  "search.group.lead": "Leads",
  "search.why": "Warum dieses Ergebnis",
  "search.relevance": "Relevanz {pct}%",
  "search.tier.authoritative": "verifiziert",
  "search.tier.mirrored": "aus HubSpot",

  "context.title": "Verwandte Belege",
  "context.empty": "Noch nichts Verwandtes.",

  "palette.aria": "Befehlspalette",
  "palette.placeholder": "Wohin? Oder frag einfach etwas…",
  "palette.empty": "Keine Treffer.",
  "palette.askAi": "KI fragen: „{query}“",
  "palette.typeScreen": "Ansicht",
  "palette.typeAction": "Aktion",
  "palette.typeRecord": "Datensatz",
  "palette.seeAll": "Alle Ergebnisse für „{query}“ anzeigen",
  "action.newDeal": "Neuer Deal",
  "action.readCompany": "Firma einlesen",
  "action.booking": "Buchungsseite",

  "fab.open": "Dazu fragen",
  "fab.close": "Schließen",
  "fab.panelAria": "Zu diesem Datensatz fragen",
  "fab.context": "Frage zu {context}",
  "fab.scope": "Dein Agent liest nur, was du sehen kannst.",
  "fab.inputAria": "Deine Frage",
  "fab.placeholder": "Frag zu dem, was du gerade offen hast…",
  "fab.send": "Fragen",

  "explain.open": "Diese Zahl erklären",
  "explain.title": "So setzt sich die Zahl zusammen",
  "explain.rate": "Kurs {rate} am {date}",

  "brief.nothingSent": "Noch nichts versendet",
  "board.count": "{count} Deals",
  "board.weighted": "gewichtet {value}",
  "deal.stalled": "stockt",
  "deal.archived": "archiviert",
  "deal.singleThreaded": "nur ein Kontakt",
  "deal.staged": "vorgemerkt",
  "record.timeline": "Verlauf",
  "record.edit": "Bearbeiten",
  "record.save": "Speichern",
  "record.archive": "Archivieren",
  "record.disqualify": "Disqualifizieren",
  "record.archiveConfirm":
    "Bist du sicher? Das archiviert den Datensatz — es gibt keine Rückgängig-Funktion.",
  "record.disqualifyConfirm":
    "Bist du sicher? Das disqualifiziert und archiviert den Lead — es gibt keine Rückgängig-Funktion.",
  "record.archived": "Archiviert",
  "record.share": "Teilen",
  "record.moreActions": "Weitere Aktionen",
  "record.fullHistory": "Vollständiger Verlauf",

  "share.title": "Diesen Datensatz teilen",
  "share.ceiling.pre": "Eine Freigabe ändert, wer ",
  "share.ceiling.recordEmphasis": "genau diesen einen Datensatz",
  "share.ceiling.mid":
    " sehen kann — sonst nichts am Zugriffsbereich einer Person. Eine Freigabe ist auf deinen eigenen Zugriff begrenzt, ",
  "share.ceiling.noWider": "nicht weiter",
  "share.ceiling.post": ".",
  "share.unknownRecord": "Dieser Datensatztyp kann nicht geteilt werden.",
  "share.grantAccess": "Zugriff gewähren",
  "share.subject": "Person oder Team",
  "share.alreadyGranted": "hat bereits eine Freigabe",
  "share.kindPerson": "Person",
  "share.kindTeam": "Team",
  "share.access": "Zugriffsstufe",
  "share.access.read": "Lesen",
  "share.access.write": "Schreiben",
  "share.access.readNote":
    "Kann diesen Datensatz öffnen und lesen — nicht bearbeiten oder senden.",
  "share.access.writeNote":
    "Kann diesen Datensatz öffnen, bearbeiten und ergänzen — nicht Eigentümerschaft oder Freigaben ändern.",
  "share.expiry": "Ablauf",
  "share.expiry.none": "Kein Ablauf (bis zum Widerruf)",
  "share.expiry.day": "Läuft in 24 Stunden ab",
  "share.expiry.week": "Läuft in 7 Tagen ab",
  "share.expiry.month": "Läuft in 30 Tagen ab",
  "share.reason": "Grund",
  "share.grant": "Zugriff gewähren",
  "share.whoHasAccess": "Wer hat Zugriff",
  "share.grantedBy": "gewährt von",
  "share.revoke": "Widerrufen",
  "share.revokeConfirm":
    "Diese Freigabe widerrufen? Der Zugriff auf diesen Datensatz entfällt beim nächsten Request — es gibt keine Rückgängig-Funktion.",
  "share.approvalRequired":
    "Diese Freigabe braucht erst eine Genehmigung — sie wurde in den Genehmigungs-Posteingang eingereiht, aber noch nicht angewendet.",
  "share.empty": "Noch keine manuellen Freigaben für diesen Datensatz.",
  "share.teamMembers.one": "Team · {count} Mitglied",
  "share.teamMembers.other": "Team · {count} Mitglieder",
  "share.rosterLoading": "Personen und Teams werden geladen…",
  "share.rosterErrorUsers":
    "Personenliste konnte nicht geladen werden — Teams werden unten angezeigt.",
  "share.rosterErrorTeams":
    "Teamliste konnte nicht geladen werden — Personen werden unten angezeigt.",
  "share.rosterErrorBoth": "Personen und Teams konnten nicht geladen werden.",
  "share.rosterEmpty": "Keine freigebbaren Personen oder Teams gefunden.",

  "edit.versionSkew":
    "Dieser Datensatz hat sich geändert, seit du ihn geöffnet hast — neu laden und erneut versuchen.",

  "merge.person": "Kontakt zusammenführen",
  "merge.org": "Firma zusammenführen",
  "merge.searchPlaceholder": "Suchen…",
  "merge.pickTarget": "Überlebenden Datensatz auswählen",
  "merge.confirm":
    "{source} in {target} zusammenführen? {source} wird archiviert.",
  "merge.submit": "Zusammenführen",

  "tab.overview": "Übersicht",
  "tab.relationships": "Personen & Firmen",
  "tab.partner": "Partner",
  "tab.rollup": "Roll-up",
  "tab.history": "Verlauf",

  "rollup.weightedPipeline": "Gewichtete Pipeline",
  "rollup.closedWon": "Abgeschlossen (aktuelles Quartal)",
  "rollup.activity30d": "Aktivität (30 Tage)",
  "rollup.accounts": "Zusammengefasste Accounts",
  "rollup.excluded":
    "{count} für dich nicht sichtbare Account(s) wurden ausgeschlossen",
  "rollup.fxUnavailable":
    "Ein Wechselkurs fehlt — das Roll-up kann nicht berechnet werden.",
  "rollup.computedAt": "Berechnet am {when}",

  "nav.partners": "Partner",
  "partner.setup": "Zum Partner machen",
  "partner.edit": "Partner bearbeiten",
  "partner.none": "Noch kein Partner",
  "partner.organization": "Organisation",
  "partner.role": "Partnerrolle",
  "partner.certStatus": "Zertifizierungsstatus",
  "partner.marginTier": "Margen-Stufe",
  "partner.stage": "Beziehungsphase",
  "partner.nextStep": "Nächster Schritt",
  "partner.nextStepDue": "Nächster Schritt fällig",
  "partner.servedSegments": "Betreute Segmente",
  "partner.servedSegmentsHint": "kommagetrennt",
  "partner.role.hosting": "Hosting",
  "partner.role.consulting": "Beratung",
  "partner.role.strategic": "Strategisch",
  "partner.cert.applied": "Beantragt",
  "partner.cert.certified": "Zertifiziert",
  "partner.cert.suspended": "Ausgesetzt",
  "partner.marginTier.tier1": "Stufe 1 (15 %)",
  "partner.marginTier.tier2": "Stufe 2 (20 %)",
  "partner.marginTier.tier3": "Stufe 3 (25 %)",
  "partner.stage.research": "Recherche",
  "partner.stage.identified": "Identifiziert",
  "partner.stage.contacted": "Kontaktiert",
  "partner.stage.inConversation": "Im Gespräch",
  "partner.stage.fitConfirmed": "Passung bestätigt",
  "partner.stage.agreementPending": "Vereinbarung ausstehend",
  "partner.stage.active": "Aktiv",
  "partner.stage.activeReferring": "Aktiv — empfiehlt",
  "partner.stage.dormant": "Ruhend",
  "partner.stage.noFit": "Keine Passung",

  "rel.add": "Beziehung hinzufügen",
  "rel.kind": "Art",
  "rel.role": "Rolle",
  "rel.startedAt": "Beginn",
  "rel.endedAt": "Ende",
  "rel.current": "aktuell",
  "rel.remove": "Entfernen",
  "rel.removeConfirm":
    "Bist du sicher? Das entfernt die Beziehung — es gibt keine Rückgängig-Funktion.",
  "rel.empty": "Noch keine Beziehungen",
  "rel.counterparty": "Verknüpft mit",
  "rel.dates": "Zeitraum",
  "rel.pickCounterparty": "Die andere Seite auswählen",
  "rel.addConfirm": "{kind}-Verknüpfung zu {target} hinzufügen.",
  "rel.kind.employment": "Anstellung",
  "rel.kind.dealStakeholder": "Deal-Beteiligter",
  "rel.kind.projectStakeholder": "Projekt-Beteiligter",
  "rel.kind.partnerOf": "Partner von",
  "rel.kind.referredBy": "Empfohlen von",
  "rel.kind.coSellWith": "Co-Sell mit",

  "common.error": "Konnten diese Ansicht nicht laden.",
  "common.errorNoCause":
    "Die Anfrage ist fehlgeschlagen. Keine Ursache gemeldet.",
  "common.retry": "Erneut versuchen",
  "common.empty": "Hier ist noch nichts.",
  "common.saving": "Wird gespeichert…",

  // "Funktioniert nicht mehr", nicht "Fehler aufgetreten": die Ansicht ist
  // stehengeblieben, und das ist die Beobachtung, die der Lesende selbst
  // machen kann. Kein Wort über den Fehler.
  "app.errorTitle": "Diese Ansicht funktioniert nicht mehr.",
  "app.errorBody":
    "Versuch es noch einmal. Wenn es weiter fehlschlägt, lade die Seite neu.",
  "app.errorRetry": "Erneut versuchen",

  "list.search": "Suchen",
  "list.sort": "Sortieren",
  "list.showArchived": "Archivierte anzeigen",
  "list.loadMore": "Mehr laden",
  "list.sortNewest": "Neueste",
  "list.sortScore": "Score",
  "list.overlayReadOnly":
    "Sortierung und Filter laufen über HubSpot — dort öffnen",
  "overlay.unavailable":
    "In der HubSpot-Ansicht nicht verfügbar — in HubSpot öffnen",
  "overlay.chipLabel": "Liest aus HubSpot",
  "overlay.chipAria":
    "Diese Installation liest Datensätze aus einem HubSpot-Spiegel statt aus nativen Tabellen. Öffne Einstellungen → Overlay, um die Verbindung zu verwalten.",
  "overlay.refused":
    "Beim Lesen aus HubSpot nicht verfügbar — der Spiegel kann diesen Schreibvorgang nicht ausführen.",
  "overlay.filterUnsupported":
    "Dieser Filter oder diese Sortierung ist beim Lesen aus HubSpot nicht verfügbar — bitte entfernen und erneut versuchen.",
  "overlay.emptyOwnerHint":
    "Eine leere Liste bedeutet hier meist, dass die HubSpot-E-Mail des Owners keinem Workspace-Benutzer entspricht — nicht, dass das HubSpot-Portal leer ist.",
  "overlay.partialWriteBack":
    "Nur die Felder, die HubSpot akzeptiert, werden zurückgeschrieben — alles andere hier, einschließlich Custom Fields und Owner, wird überhaupt nicht angewendet; der aktuelle Wert in HubSpot bleibt bestehen.",

  "overlay.title": "HubSpot-Spiegel",
  "overlay.sub":
    "Verbindet das führende CRM des Workspaces, damit Datensätze aus dessen Spiegel statt aus nativen Tabellen gelesen werden.",
  "overlay.loading": "Lade die Anbieter-Verbindung…",
  "overlay.notConfigured":
    "Overlay-Modus ist in diesem Deployment nicht konfiguriert.",
  "overlay.loadFailed": "Die Anbieter-Verbindung konnte nicht geladen werden.",
  "overlay.empty":
    "Kein führendes System verbunden. Verbinde HubSpot, um Datensätze aus dessen Spiegel zu lesen.",
  "overlay.adminOnly": "Du hast keine Berechtigung, HubSpot zu verbinden.",
  "overlay.region": "Region",
  "overlay.regionEu1": "EU",
  "overlay.regionUs": "USA",
  "overlay.token": "Private-App-Token",
  "overlay.tokenHint": "Wird im Vault versiegelt; wird nie wieder angezeigt.",
  "overlay.connect": "HubSpot verbinden",
  "overlay.reconnect": "Erneut verbinden",
  "overlay.connectConfirmTitle": "HubSpot für den ganzen Workspace verbinden?",
  "overlay.reconnectConfirmTitle":
    "HubSpot für den ganzen Workspace erneut verbinden?",
  "overlay.connectConfirmBody":
    "Dies schaltet die Lesezugriffe aller Sitze sofort auf den HubSpot-Spiegel um, und Datensätze werden schreibgeschützt, wo immer der Spiegel kein Schreiben unterstützt. Dies betrifft die gesamte Installation, nicht nur die eigene Sitzung.",
  "overlay.statusActive": "Verbunden",
  "overlay.statusRevoked": "Widerrufen",
  "overlay.statusError": "Sync-Fehler",
  "overlay.connectedAt": "Verbunden {at}",
  "overlay.syncTitle": "Spiegel-Synchronisierung",
  "overlay.syncLoading": "Lade Sync-Status…",
  "overlay.syncLoadFailed": "Sync-Status konnte nicht geladen werden.",
  "overlay.syncEmpty": "Noch nichts synchronisiert.",
  "overlay.syncStateFresh": "Aktuell",
  "overlay.syncStatePending": "Sync ausstehend",
  "overlay.syncStateStale": "Veraltet",
  "overlay.backfillDone": "Backfill abgeschlossen",
  "overlay.backfillPending": "Backfill läuft",
  "overlay.lastSynced": "Zuletzt synchronisiert {at}",
  "overlay.neverSynced": "Noch nie synchronisiert",
  "overlay.budgetTitle": "API-Budget",
  "overlay.budgetLoading": "Lade das Budget-Fenster…",
  "overlay.budgetLoadFailed": "Das Budget-Fenster konnte nicht geladen werden.",
  "overlay.budgetHeadroom": "Spielraum: {headroom}",
  "overlay.budgetSources":
    "Force-Fresh {forceFresh} · Poller {poller} · Capture {capture}",
  "overlay.budgetSearch": "Such-API: {consumed} / {limit} pro Sekunde",
  "overlay.bandOk": "Gesund",
  "overlay.bandWarn": "Nähert sich dem Limit",
  "overlay.bandShed": "Drosselt Last",
  "overlay.reconcile": "Jetzt synchronisieren",
  "overlay.reconcileQueued":
    "Abgleich eingereiht — der Worker holt ihn beim nächsten Poll ab (etwa alle 2 Minuten).",
  "overlay.disconnect": "Trennen",
  "overlay.disconnectTitle": "HubSpot trennen?",
  "overlay.disconnectBody":
    "Dies löscht die gespiegelten Daten und schaltet den Workspace zurück auf native Datensätze. Das Audit-Protokoll bleibt erhalten.",

  "overlay.userMap.title": "Nutzerzuordnung des Spiegels",
  "overlay.userMap.sub":
    "Wer jede Person im Workspace als {principal}-Nutzer ist. Diese Zuordnung entscheidet allein darüber, was sie im Spiegel sieht.",
  "overlay.userMap.cost":
    "Wer nicht zugeordnet ist, sieht überhaupt keine gespiegelten Datensätze — alle Listen bleiben leer.",
  "overlay.userMap.loading": "Lade die Nutzerzuordnung…",
  "overlay.userMap.loadFailed":
    "Die Nutzerzuordnung konnte nicht geladen werden.",
  "overlay.userMap.adminOnly":
    "Du hast keine Berechtigung, die Zuordnung zu prüfen.",
  "overlay.userMap.notOverlay":
    "Dieser Workspace liest aus nativen Tabellen, es gibt also nichts zuzuordnen.",
  "overlay.userMap.notConfigured":
    "Overlay-Modus ist in diesem Deployment nicht konfiguriert.",
  "overlay.userMap.empty": "Dieser Workspace hat keine Nutzer zum Zuordnen.",
  "overlay.userMap.view": "Gruppierung",
  "overlay.userMap.viewByUser": "Nach Nutzer",
  "overlay.userMap.viewByOwner": "Nach {principal}-Nutzer",
  "overlay.userMap.principal.hubspot": "HubSpot",
  "overlay.userMap.principal.generic": "verbundenes CRM",
  "overlay.userMap.you": "Du",
  "overlay.userMap.matchEmail": "Über E-Mail zugeordnet",
  "overlay.userMap.matchManual": "Manuell gesetzt",
  "overlay.userMap.map": "Zuordnen…",
  "overlay.userMap.change": "Ändern…",
  "overlay.userMap.unmap": "Zuordnung aufheben",
  "overlay.userMap.cancel": "Abbrechen",
  "overlay.userMap.pickerLabel": "{principal}-Nutzer suchen",
  "overlay.userMap.truncated":
    "Das {principal}-Verzeichnis ist länger als diese Liste — wen du hier nicht findest, liegt vielleicht hinter der Grenze.",
  "overlay.userMap.directoryFailed":
    "Das {principal}-Verzeichnis konnte nicht gelesen werden, deshalb lässt sich gerade niemand auswählen.",
  "overlay.userMap.notMapped": "Nicht zugeordnet",
  "overlay.userMap.chip.noEmailMatch": "Keine E-Mail-Übereinstimmung",
  "overlay.userMap.chip.ambiguousEmail": "Mehrdeutige E-Mail",
  "overlay.userMap.chip.blockedByAdmin": "Von Admin aufgehoben",
  "overlay.userMap.chip.notYetSynced": "Noch nicht synchronisiert",
  "overlay.userMap.chip.directoryUnavailable": "Grund unbekannt",
  "overlay.userMap.reason.noEmailMatch":
    "Kein {principal}-Nutzer hat diese E-Mail-Adresse.",
  "overlay.userMap.reason.ambiguousEmail":
    "Zwei oder mehr {principal}-Nutzer teilen sich diese E-Mail-Adresse, eine automatische Zuordnung wäre also nicht sicher.",
  "overlay.userMap.reason.blockedByAdmin":
    "Eine Admin-Person hat die Zuordnung aufgehoben; die automatische Zuordnung setzt sie nicht erneut.",
  "overlay.userMap.reason.notYetSynced":
    "Das {principal}-Verzeichnis führt diese Person noch nicht.",
  "overlay.userMap.reason.directoryUnavailable":
    "Das {principal}-Verzeichnis konnte nicht vollständig gelesen werden, deshalb lässt sich kein Grund ableiten.",
  "overlay.userMap.staleChip": "Nicht mehr im {principal}-Verzeichnis",
  "overlay.userMap.staleNote":
    "Diese manuelle Zuordnung gewährt keine Sichtbarkeit. Sie wird gemeldet, aber nie automatisch zurückgenommen — die Entscheidung bleibt bei dir.",
  "overlay.userMap.unmapTitle": "Zuordnung dieser Person aufheben?",
  "overlay.userMap.unmapSelfTitle": "Deine eigene Zuordnung aufheben?",
  "overlay.userMap.unmapBody":
    "{user} sieht dann keine gespiegelten Datensätze mehr, bis die Zuordnung wieder gesetzt ist.",
  "overlay.userMap.unmapSelfBody":
    "Du siehst dann keine gespiegelten Datensätze mehr, bis du wieder zugeordnet bist. Dieser Tab bleibt erreichbar, du kannst es hier rückgängig machen.",
  "overlay.userMap.sharedSeat": "Geteilter Sitz — {count} Nutzer",
  "overlay.userMap.ownerEmpty":
    "Bisher ist niemand einem {principal}-Nutzer zugeordnet.",
  "overlay.userMap.unmappedCountOne":
    "1 Person ist nicht zugeordnet und fehlt hier — wechsle zu Nach Nutzer, um das zu beheben.",
  "overlay.userMap.unmappedCount":
    "{count} Personen sind nicht zugeordnet und fehlen hier — wechsle zu Nach Nutzer, um das zu beheben.",
  "overlay.userMap.partialView":
    "Diese Gruppierung und die Zählung umfassen nur die bisher geladenen Nutzer. Lade mehr, um den Rest zu sehen.",

  "people.name": "Name",
  "people.email": "E-Mail",
  "people.capturedBy": "Erfasst von",
  "person.consent": "Einwilligung",
  "consent.grant": "Erteilen",
  "consent.withdraw": "Widerrufen",
  "consent.doubleOptIn": "Double-Opt-in anfordern",
  "consent.doiIssued": "Einmal-Token (nur einmal sichtbar):",
  "consent.doiExpires": "Läuft ab",
  "consent.noRecord": "kein Eintrag",
  "consent.noPurposes":
    "Dieser Workspace erfasst noch keine Einwilligungszwecke.",
  "consent.defaultDeny":
    "Ausgehende Kommunikation ist pro Zweck standardmäßig gesperrt: ein Versand wird blockiert, sofern keine aktive, nachgewiesene Einwilligung für diesen Zweck vorliegt. Eine Einwilligung für einen Zweck berechtigt niemals einen anderen.",
  "consent.proofLog": "Nachweisprotokoll",
  "consent.proofEmpty":
    "Für diesen Zweck ist keine Einwilligungsentscheidung erfasst. Ein leeres Protokoll ist ehrlich, keine Lücke.",
  "consent.sourceUnknown": "Quelle nicht erfasst",
  "consent.tokenLabel": "Bestätigungs-Token",
  "consent.tokenHint":
    "Dieser Zweck benötigt ein Double-Opt-in: Einmal-Token hier einfügen, damit die Einwilligung wirksam wird.",
  "consent.actorHuman": "Mensch",
  "consent.actorAgent": "Agent",
  "consent.actorSystem": "System",
  "consent.actorConnector": "Connector",
  "consent.actorUnknown": "Akteur nicht erfasst",
  "consent.purposesUnavailable":
    "Der Einwilligungszweck-Katalog konnte nicht geladen werden — welche Zwecke ein Double-Opt-in brauchen, lässt sich gerade nicht anzeigen.",

  "org.name": "Firma",
  "org.industry": "Branche",
  "org.size": "Größe",
  "org.classification": "Typ",
  // Nur dort angeboten, wo es noch kein Partnerprogramm gibt: der Tab mit dem
  // Formular erscheint erst, wenn eines besteht — so entsteht das erste.
  // Wo der Account bei uns steht, und was er für uns ist — die zwei Fragen,
  // die die abgelöste Einstufung mit einem Wert beantworten wollte.
  "org.lifecycle": "Phase",
  "org.relationshipTypes": "Beziehung zu uns",
  "org.lifecycle.unknown": "Nicht eingeschätzt",
  "org.lifecycle.target": "Zielkunde",
  "org.lifecycle.prospect": "Interessent",
  "org.lifecycle.opportunity": "Chance",
  "org.lifecycle.customer": "Kunde",
  "org.lifecycle.former_customer": "Ehemaliger Kunde",
  "org.lifecycle.disqualified": "Disqualifiziert",
  "org.relType.customer": "Kunde",
  "org.relType.partner": "Partner",
  "org.relType.supplier": "Lieferant",
  "org.relType.investor": "Investor",
  "org.relType.portfolio_company": "Portfoliounternehmen",
  "org.relType.competitor": "Wettbewerber",
  "org.relType.other": "Sonstige",
  // Warum ein Fakt seinem eigenen Feld widerspricht. Der Fakt bleibt mit
  // seinem Beleg sichtbar — ein Mensch erkennt es, Ausblenden wäre schlechter.
  "co.factSuspect.phoneShapedLocation": "Sieht nach einer Telefonnummer aus",
  "co.factSuspect.notAPhone": "Sieht nicht nach einer Telefonnummer aus",
  "co.factSuspect.notAYear": "Sieht nicht nach einer Jahreszahl aus",
  "co.factSuspect.notAnEmail": "Sieht nicht nach einer E-Mail-Adresse aus",
  "co.factSuspect.notASize": "Sieht nicht nach einer Mitarbeiterzahl aus",
  // Die drei Aussagen, mit denen die Übersicht beginnt, und was das
  // Ausführen eines Vorschlags bedeutet.
  "co.strip.title": "Wo dieser Account steht",
  "co.strip.account": "Phase",
  "co.strip.engagement": "Wer am Zug ist",
  "co.strip.commercial": "Offene Arbeit",
  "co.strip.engagement.never_contacted": "Nie kontaktiert",
  "co.strip.engagement.active": "Im Gespräch",
  "co.strip.engagement.waiting_on_them": "Warten auf sie",
  "co.strip.engagement.waiting_on_us": "Warten auf uns",
  "co.strip.engagement.dormant": "Still geworden",
  "co.strip.lastBoth": "Sie schrieben {inbound} · wir schrieben {outbound}",
  "co.strip.never": "nie",
  "co.strip.openDeals": "{count} offen",
  "co.strip.stalled": "{count} ins Stocken geraten",
  "co.suggest.act.draftReply": "Antwort entwerfen",
  "co.suggest.act.openDeal": "Deal öffnen",
  "co.suggest.act.addTask": "Nächsten Schritt anlegen",
  // Ein Verlauf als ein Ereignis sagt zuerst, WAS er ist.
  "timeline.group.thread": "{count} Nachrichten",
  "timeline.group.threadOne": "1 Nachricht",
  "timeline.group.bulk": "an {count} Personen gesendet",
  "timeline.group.bulkOne": "an 1 Person gesendet",
  "timeline.group.expand": "Öffnen",
  "timeline.group.collapse": "Schließen",
  "timeline.group.openThread": "Ganzen Verlauf ansehen",
  "timeline.group.mayContinue": "kann früher weitergehen",
  "tab.people": "Personen",
  "tab.timeline": "Verlauf",
  // Das Briefing nach den Fragen, die es beantwortet, und die Art jeder
  // Aussage — eine Einschätzung darf nicht wie ein Fakt wirken.
  "co.brief.section.snapshot": "Was sie sind",
  "co.brief.section.fit": "Warum es für uns zählt",
  "co.brief.section.health": "Wie es steht",
  "co.brief.section.activity": "Was passiert ist",
  "co.brief.section.next_step": "Was zu tun ist",
  "co.brief.nature.fact": "Fakt",
  "co.brief.nature.assessment": "Unsere Einschätzung",
  "co.brief.nature.recommendation": "Vorschlag",
  "co.health.title": "Wie es steht",
  "co.health.sinceInbound": "Sie schrieben zuletzt vor {days} Tagen",
  "co.health.replyBalance": "{percent}% des Austauschs kam von ihnen",
  "co.health.activeContacts": "{count} Personen hier hatten je Kontakt",
  "co.health.openCommitments": "{count} offene Zusage(n)",
  "co.health.singleThreaded": "Ein Kontakt trägt diesen Account",
  "org.partnerSetUp": "Partnerprogramm einrichten",
  // Die Einstufung, wie ein Leser sie sieht. Die Spalte speichert das Enum;
  // das Enum selbst anzuzeigen ("prospect") sagte einem deutschen Leser nichts.
  "org.class.prospect": "Interessent",
  "org.class.customer": "Kunde",
  "org.class.agency": "Agentur",
  "org.class.reseller": "Wiederverkäufer",
  "org.class.tech_vendor": "Technologieanbieter",
  "org.class.platform": "Plattform",
  "org.class.partner": "Partner",
  "org.class.competitor": "Wettbewerber",
  "org.class.other": "Sonstige",
  "org.class.explain":
    "Wie diese Firma zu euch steht — keine Phase in einem Deal.",
  "signal.kind.stalled_deal": "Deal steht",
  "signal.kind.champion_left": "Champion ist weg",
  "signal.kind.reengagement": "Wieder ansprechen",
  "signal.kind.buying_intent": "Kaufinteresse",
  "signal.kind.risk": "Risiko",
  "signal.kind.other": "Sonstiges",
  "signal.kind.contract_ended": "Vertrag endet",
  "signal.kind.new_opportunity": "Neue Chance",
  "signal.kind.commitment_made": "Etwas wurde zugesagt",
  "signal.kind.ghosted_thread": "Keine Antwort",
  "co.strip.signal": "Wissenswert",
  "co.routeIn.open": "Zugang",
  "co.routeIn.title": "Wer hier mit {name} spricht",
  "co.routeIn.none": "Bisher hat hier niemand geschrieben.",
  "co.routeIn.partial":
    "Unter den lesbaren Verbindungen kein Zugang — einige wurden zurückgehalten oder ausgelassen.",
  "co.routeIn.mayBeMore":
    "Einige Verbindungen wurden zurückgehalten oder ausgelassen — es kann weitere geben.",
  "co.routeIn.band.strong": "regelmäßig in Kontakt",
  "co.routeIn.band.some": "etwas Kontakt",
  "co.routeIn.band.faint": "kaum in Kontakt",
  "co.routeIn.band.unknown": "Kontakt vorhanden, noch kein Muster",
  "record.profile": "Profil",
  "record.business": "Geschäft",
  "co.pulse.strongestLead": "Zugang \u00fcber",
  "co.pulse.strengthTail.one": "\u2014 der einzige Kontakt hier",
  "co.pulse.strengthTail.other": "\u2014 von {count} Kontakten hier",
  "co.pulse.noStrength": "Noch keine Interaktionen erfasst",
  // Zwei Zeitpunkte, nie zu einem "letzter Kontakt" verschmolzen: wer zuletzt
  // geschrieben hat, ist die eigentliche Frage.
  "co.pulse.lastInbound": "Sie schrieben {when}",
  "co.pulse.lastOutbound": "Wir schrieben {when}",
  "co.pulse.noInbound": "Sie haben nie geschrieben",
  "co.pulse.noOutbound": "Wir haben nie geschrieben",
  "co.pulse.neverTouched": "Noch nie kontaktiert",
  "co.pulse.owner": "Betreut von",
  "co.owner.notInRoster": "Aktuell zugeordnet (nicht mehr in der Nutzerliste)",
  "co.pulse.unowned": "Nicht zugewiesen",
  "co.since.first": "Du öffnest diesen Account zum ersten Mal.",
  "co.partial":
    "Teile dieser Seite konnten nicht geladen werden; sie zeigt möglicherweise nicht alles zu diesem Account.",
  "evidence.explain": 'Herkunft von "{value}"',
  "evidence.fullHistory": "Vollständiger Verlauf",
  "co.section.unavailable":
    "Konnte nicht geladen werden — das ist möglicherweise nicht das ganze Bild",
  "co.section.restricted":
    "Ausgeblendet \u2014 deine Rolle darf das nicht lesen",
  "co.next.title": "Nächste Schritte",
  "co.next.empty": "Keine offene Aufgabe zu diesem Account.",
  "co.next.overdue": "\u00dcberfällig",
  "co.next.due": "Fällig {when}",
  "co.next.undated": "Ohne Datum",
  "co.next.done": "Als erledigt markieren",
  "co.next.assignee": "Zuständig",
  "co.people.title": "Personen",
  "co.people.empty": "Noch kein Kontakt mit diesem Account verknüpft.",
  "co.people.singleThread":
    "Nur ein Kontakt \u2014 der Account hängt an einer Person",
  "co.people.consentGranted": "Kontakt erlaubt",
  "co.people.consentWithdrawn": "Widerrufen",
  "co.people.consentUnknown": "Keine Einwilligung hinterlegt",
  "co.brief.title": "Bevor du mit ihnen sprichst",
  "co.brief.unavailable":
    "Die Einschätzung zu diesem Account konnte nicht geladen werden — das hier ist nicht das ganze Bild.",
  "co.brief.empty":
    "Zu diesem Account gibt es noch zu wenig, um etwas daraus zu lesen.",
  "co.brief.rewrite": "Neu schreiben",
  "co.brief.rewriting": "Wird geschrieben…",
  "co.brief.by.model": "Von Margince geschrieben",
  "co.brief.by.deterministic": "Aus deinen Daten zusammengestellt",
  "co.brief.generatedAt": "Stand {when}",
  "co.brief.cite.deal": "Deal",
  "co.brief.cite.activity": "Aktivität",
  "co.brief.cite.person": "Kontakt",
  "co.brief.cite.organization": "Account",
  "co.brief.cite.fact": "Fakt",
  "co.brief.cite.deal.many": "{count} Deals",
  "co.brief.cite.activity.many": "{count} Aktivitäten",
  "co.brief.cite.person.many": "{count} Kontakte",
  "co.brief.cite.organization.many": "{count} Accounts",
  "co.brief.cite.fact.many": "{count} Fakten",
  "approval.kind.advance_deal": "Deal weiterbringen",
  "approval.kind.promote_lead": "Lead überführen",
  "approval.kind.close_date_correction": "Abschlussdatum korrigieren",
  "approval.kind.deal_follow_up": "Wiedervorlage zum Deal anlegen",
  "approval.kind.archive_record": "Datensatz archivieren",
  "approval.kind.merge_records": "Datensätze zusammenführen",
  "approval.kind.share_record": "Datensatz freigeben",
  "approval.kind.update_record": "Datensatz ändern",
  "approval.kind.create_record": "Datensatz anlegen",
  "approval.kind.send_email": "E-Mail senden",
  "approval.kind.book_meeting": "Termin buchen",
  "approval.kind.send_offer": "Angebot senden",
  "approval.kind.coldstart": "Neuen Account befüllen",
  "approval.kind.enrich": "Aus dem Web anreichern",
  "approval.kind.deepread": "Unternehmensseite lesen",
  "approval.kind.linkedin_match": "LinkedIn-Zuordnung",
  "approval.kind.site_lead": "Person von der Website aufnehmen",
  "approval.kind.capture_counterparty": "Person aus deiner Mail aufnehmen",
  "approval.kind.org_name_promotion": "Account umbenennen",
  "approval.kind.lifecycle_change": "Account-Phase",
  "approval.kind.fx_rate_proposal": "Wechselkurse aktualisieren",
  "approval.kind.ai_model_rate_proposal": "Modellpreise aktualisieren",
  "co.assistant.title": "Diesen Account befragen",
  "co.assistant.aiTag": "KI-gestützt",
  "co.decisions.open": "{count} offene prüfen",
  "co.decisions.title": "Offene Entscheidungen",
  "co.decisions.group": "{count} × {kind}",
  "co.decisions.empty": "Hier wartet nichts auf eine Entscheidung.",
  "co.ask.title": "Margince fragen",
  "co.ask.q.whats_open": "Was ist hier offen?",
  "co.ask.q.meeting_prep": "Auf ein Gespräch vorbereiten",
  "co.ask.q.whats_changed": "Was hat sich zuletzt geändert?",
  "co.ask.nothing": "Dazu ist nichts sichtbar, was das beantworten würde.",
  "co.ask.failed":
    "Die Frage konnte nicht beantwortet werden — bitte erneut versuchen.",
  "co.suggest.title": "Das lohnt sich als Nächstes",
  "co.suggest.kind.no_reply": "Keine Antwort",
  "co.suggest.kind.stalled_deal": "Deal steht",
  "co.suggest.kind.no_next_step": "Nichts geplant",
  "co.suggest.kind.lifecycle_conflict": "Widerspruch im Datensatz",
  "co.suggest.more": "{count} weitere hier nicht gezeigt.",
  "co.suggest.dismiss": "Nicht jetzt",
  "co.suggest.dismissFailed":
    "Konnte nicht ausgeblendet werden — es wird weiter angezeigt",
  "co.deals.title": "Deals",
  "co.deals.empty": "Kein offener Deal zu diesem Account.",
  "co.deals.wonLifetime": "Bisher gewonnen",
  "co.deals.lostCount": "{count} verloren",
  "co.deals.noStage": "Keine Phase",
  "co.connections.title": "Verbindungen",
  "co.connections.empty": "Noch nichts mit diesem Account verkn\u00fcpft.",
  "co.connections.ourSide": "Auf unserer Seite",
  "co.connections.theirSide": "Bei diesem Account",
  "co.connections.expand": "Gr\u00f6\u00dfer ansehen",
  "co.connections.collapse": "Schlie\u00dfen",
  "co.connections.introPath": "Weg hinein",
  "co.connections.more": "{count} weitere hier nicht gezeigt.",
  "co.connections.withheld": "F\u00fcr Sie nicht sichtbar: {groups}",
  "co.connections.rel.employment": "arbeitet hier",
  "co.connections.rel.has_deal": "offener Deal",
  "co.connections.rel.deal_stakeholder": "Stakeholder in einem Deal",
  "co.connections.rel.parent": "Muttergesellschaft",
  "co.connections.rel.child": "Tochtergesellschaft",
  "co.connections.rel.partner_of.counterparty": "Partner bei diesem Account",
  "co.connections.rel.partner_of.owner": "dieser Account ist ihr Partner",
  "co.connections.rel.referred_by.counterparty": "hat diesen Account empfohlen",
  "co.connections.rel.referred_by.owner": "von diesem Account empfohlen",
  "co.connections.rel.owns": "betreut diesen Account",
  "co.connections.rel.in_contact_with": "im Austausch",
  "co.connections.noSignal": "noch kein Signal",
  "linkedinImport.title": "LinkedIn-Kontakte",
  "linkedinImport.sub":
    "Eigenen Export importieren und sehen, wen das Team bereits kennt",
  "linkedinImport.explainer":
    "LinkedIn stellt Ihnen unter Einstellungen → Datenschutz → Kopie Ihrer Daten eine Connections.csv bereit. Hochgeladen zeigt sie, wer im Team bereits jemanden im Unternehmen kennt. Die Kontakte werden NICHT zu Personen: Sie erscheinen weder in Suche noch in Listen oder Kontaktseiten, und niemand kann ihnen schreiben.",
  "linkedinImport.profileLabel": "Ihre LinkedIn-Profil-URL",
  "linkedinImport.profilePlaceholder": "https://www.linkedin.com/in/…",
  "linkedinImport.saveProfile": "Profil speichern",
  "linkedinImport.connectedNote":
    "Verbunden. Importierte Kontakte werden diesem Profil zugeordnet, damit das CRM sagen kann, welche Kollegin jemanden kennt \u2014 und nicht nur, dass \u201edas Unternehmen\u201c ihn kennt.",
  "linkedinImport.notConnectedNote":
    "Noch nicht verbunden. Mit Ihrer Profil-URL werden importierte Kontakte Ihnen namentlich zugeordnet.",
  "linkedinImport.whichFile":
    "Gesucht ist die Datei Connections.csv \u2014 das Export-Archiv enth\u00e4lt ein Dutzend weitere.",
  "linkedinImport.choose": "Connections.csv ausw\u00e4hlen",
  "linkedinImport.noMatchesYet":
    "Noch keine Treffer, und das ist in einem neuen Workspace normal: Ihre Kontakte werden mit den Personen abgeglichen, die das CRM kennt, und die entstehen erst beim Lesen Ihrer E-Mails. Der Abgleich l\u00e4uft st\u00fcndlich erneut, Treffer erscheinen also nach und nach.",
  "linkedinImport.working": "Export wird gelesen…",
  "linkedinImport.imported": "Kontakte importiert",
  "linkedinImport.confirmed": "Einer Person zugeordnet",
  "linkedinImport.suggested": "Wartet auf Ihre Bestätigung",

  // Die Prüfliste und die Reichweiten-Tabelle (ADR-0078 §2.1b).
  "linkedinReach.title": "Wohin Ihr Netzwerk reicht",
  "linkedinReach.sub":
    "Firmen im CRM, bei denen Sie bereits jemanden kennen — die meisten Verbindungen zuerst.",
  "linkedinReach.empty":
    "Noch keine Ihrer Verbindungen arbeitet bei einer erfassten Firma.",
  "linkedinReach.allUnresolved":
    "Alle {unresolved} Ihrer Verbindungen arbeiten bei Firmen, die noch nicht erfasst sind.",
  "linkedinReach.account": "Firma",
  "linkedinReach.connections": "Sie kennen",
  "linkedinReach.onFile": "Bereits Personen",
  "linkedinReach.onFileOf": "{onFile} von {total}",
  "linkedinReach.footnote":
    "{shown} von {total} Firmen angezeigt. {unresolved} Verbindungen arbeiten bei einer Firma, die noch nicht erfasst ist.",
  "linkedinImport.skipped": "Übersprungen (kein verwertbarer Name)",
  "co.connections.rel.co_sell_with": "gemeinsamer Vertrieb",
  "co.connections.group.contacts": "Kontakte",
  "co.connections.group.deals": "Deals",
  "co.connections.group.our_side": "wer hier verbunden ist",
  "co.connections.group.intro_path": "der warme Einstieg",
  "co.signals.title": "Signale",
  "co.signals.empty": "Kein offenes Signal zu diesem Account.",
  "co.chronology.label": "Was im Verlauf angezeigt wird",
  "co.chronology.activities": "Aktivitäten",
  "co.chronology.changes": "Änderungen",
  "co.chronology.all": "Alles",
  "co.chronology.changesEmpty":
    "Seit dem Anlegen wurde kein Feld dieses Datensatzes geändert.",
  "co.chronology.allEmpty": "Zu diesem Account ist noch nichts passiert.",
  "co.chronology.truncated":
    "Ältere Einträge fehlen hier — es gibt mehr von beidem, als diese Ansicht in eine Reihenfolge bringen kann. Wähle Aktivitäten oder Änderungen, um weiter zurückzulesen.",
  "co.chronology.truncatedActivities":
    "Dieser Account hat mehr Aktivitäten, als hier hineinpassen. Gezeigt werden nur die neuesten.",
  "timeline.sent": "Gesendet",
  "timeline.received": "Erhalten",
  "timeline.textMore": "Lesen",
  "timeline.textLess": "Weniger",
  "co.profileField.display_name": "Firmenname",
  "co.profileField.offer_summary": "Was sie verkaufen",
  "co.profileField.icp": "An wen sie verkaufen",
  "co.profileField.buying_center": "Wer dort entscheidet",
  "co.profileField.value_proposition": "Was sie versprechen",
  "co.profileField.usp": "Wodurch sie sich abheben",
  "co.profileField.customer_pains": "Welches Problem sie lösen",
  "co.profileField.desired_outcomes": "Welches Ergebnis sie versprechen",
  "co.profileField.buying_intents": "Was einen Kauf auslöst",
  "co.profileField.common_objections": "Einwände, die sie hören",
  "co.profileField.sales_motion": "Wie sie verkaufen",
  "co.profileField.legal_name": "Eingetragener Name",
  "co.profileField.registered_address": "Eingetragene Anschrift",
  "co.profileField.register_vat": "Register / USt-IdNr.",
  "co.profileField.industry": "Branche",
  "co.profileField.history": "Historie",
  "co.profile.title": "Firmenprofil",
  "co.reach.window": "Kontaktstatus der letzten 90 Tage",
  "co.reach.answered": "Antwortet",
  "co.reach.silent": "Keine Antwort",
  "co.reach.untried": "Nie angesprochen",
  "co.role.set": "Rolle festlegen",
  "co.role.setOn": "Was ist {name} in diesem Deal?",
  "co.role.explain":
    "Der Champion setzt sich für euch ein, wenn ihr nicht im Raum seid. Der wirtschaftliche Entscheider unterschreibt. Beide zu benennen macht aus einer Kontaktliste ein Bild der Entscheidung.",
  "co.role.onDeal": "In welchem Deal",
  "co.role.role": "Rolle",
  "co.role.champion": "Champion",
  "co.role.economic_buyer": "wirtschaftlicher Entscheider",
  "co.role.blocker": "Bremser",
  "co.role.influencer": "Einflussnehmer",
  "co.role.user": "Anwender",
  "co.people.missing":
    "Für den offenen Deal ist noch kein {roles} benannt — leg die Rolle bei der Person fest, die es ist.",
  "co.people.untriedHint": "{count} Personen hier wurden nie angesprochen.",
  "co.people.untriedHintOne": "Eine Person hier wurde nie angesprochen.",
  "co.evidence.title": "Woher das stammt",
  "co.relationships.title": "Verknüpfte Personen und Firmen",
  "co.tools.title": "Daten & Werkzeuge",
  "co.prep.title": "Bevor du mit ihnen sprichst",
  "co.prep.sparse":
    "Zu diesem Account gibt es kaum Historie. Es lässt sich daraus nichts vorbereiten.",
  "co.prep.withheld":
    "Teile dieses Accounts sind für dich nicht sichtbar. Diese Einschätzung ist deshalb unvollständig.",
  "co.read.lastTouch": "Der letzte Kontakt war vor {days} Tagen.",
  "co.read.lastTouchOne": "Der letzte Kontakt war gestern.",
  "co.read.neverTouched": "Mit diesem Account hatte bisher niemand Kontakt.",
  "co.read.newActivityOne": "Ein neuer Vorgang seit deinem letzten Besuch.",
  "co.read.newActivityMany":
    "{count} neue Vorgänge seit deinem letzten Besuch.",
  "co.read.dealMovedOne":
    "Ein Deal hat seit deinem letzten Besuch die Phase gewechselt.",
  "co.read.dealMovedMany":
    "{count} Deals haben seit deinem letzten Besuch die Phase gewechselt.",
  "co.read.unansweredOne":
    "Du hast in den letzten {days} Tagen einen Kontakt hier angeschrieben, ohne Antwort.",
  "co.read.unansweredMany":
    "Du hast in den letzten {days} Tagen {count} Kontakte hier angeschrieben, keiner hat geantwortet.",
  "co.read.noContacts": "Du kennst bei diesem Account noch niemanden.",
  "co.read.singleThread":
    "Nur bei {name} sind in den letzten {days} Tagen E-Mails, Anrufe oder Termine erfasst.",
  "co.read.oneContact": "{name} ist dein einziger Zugang zu diesem Account.",
  "co.read.noChampion.one": "Am offenen Deal ist kein Champion benannt.",
  "co.read.noChampion.other":
    "An keinem der offenen Deals ist ein Champion benannt.",
  "co.read.stalled": "{name} kommt nicht voran.",
  "co.read.noOpenDeal": "Kein offener Deal, und bisher nichts gewonnen.",
  "co.read.noOpenDealCustomer":
    "Aktuell kein offener Deal. Der Account hat aber schon gekauft.",
  "co.read.overdueOne": "Überfällig: {subject}",
  "co.read.overdueMany": "{count} Zusagen sind hier überfällig.",
  "co.read.noNextStep": "Für diesen Account ist nichts Nächstes geplant.",
  "co.factField.founded_year": "Gegründet",
  "co.factField.employee_range": "Mitarbeitende",
  "co.factField.phone": "Telefon",
  "co.factField.contact_email": "Kontakt-E-Mail",
  "co.factField.location": "Standort",
  "co.factField.service": "Leistung",
  "co.factField.product": "Produkt",
  "co.factField.capability": "Fähigkeit",
  "co.factField.served_industry": "Bedient",
  "co.factField.company_size": "Größe",
  "co.factField.geography": "Region",
  "co.factField.language": "Sprache",
  "co.factField.certification": "Zertifizierung",
  "co.factField.partner": "Partner",
  "co.factField.named_customer": "Kunde",
  "co.factField.technology": "Technologie",
  "co.factField.quantified_outcome": "Ergebnis",
  "co.facts.showAll": "Alle {count} anzeigen",
  "co.facts.showLess": "Weniger anzeigen",
  "co.facts.title": "Kurzfakten",
  "co.tags.lists": "Listen",
  "co.tags.tags": "Tags",
  "co.tags.noLists": "Auf keiner Liste.",
  "co.tags.noTags": "Keine Tags vergeben.",
  "co.deal.new": "Neuer Deal",
  "co.tags.apply": "Tag hinzufügen",
  "co.tags.pick": "Tag-Name",
  "co.lists.add": "Zu Liste hinzufügen",
  "co.lists.pick": "Listenname",
  "co.tags.title": "Listen & Tags",
  "co.tags.empty": "Auf keiner Liste, keine Tags vergeben.",
  "co.timeline.filterKind": "Nach Typ filtern",
  "co.timeline.filterAll": "Alle Typen",
  "co.timeline.filterPerson": "Nach Person filtern",
  "co.timeline.allPeople": "Alle",
  "co.timeline.via": "Betrifft",
  "co.timeline.empty": "Zu diesem Account ist noch nichts erfasst.",
  "co.overlayFallback":
    "Dieser Account wird aus dem verbundenen führenden System bedient; die Firmenansicht wird hier nicht zusammengestellt. \u00d6ffne ihn dort für das vollständige Bild.",
  "org.firmographics": "Firmendaten",
  "org.domains": "Domains",
  "org.firmographicsEmpty":
    "Noch nichts gelesen — belegte Profilfelder erscheinen hier, sobald eine Website-Auswertung sie bestätigt.",
  "org.facts": "Von der Website gelesene Fakten",
  "org.factCategory.company": "Unternehmen",
  "org.factCategory.offering": "Angebot",
  "org.factCategory.market": "Markt",
  "org.factCategory.signal": "Signale",

  "lead.score": "Score",
  "lead.status": "Status",
  "lead.segregated":
    "Leads bleiben vom Kontaktgraphen getrennt, bis du sie übernimmst",
  "lead.promote": "Zum Kontakt machen",
  "lead.promoteIneligible": "braucht eine E-Mail und einen offenen Status",
  "lead.filterStatus": "Status",
  "lead.statusNew": "Neu",
  "lead.statusWorking": "In Bearbeitung",
  "lead.statusPromoted": "Übernommen",
  "lead.statusDisqualified": "Disqualifiziert",
  "lead.disqualified": "Disqualifiziert",
  "lead.status.new": "Neu",
  "lead.status.working": "In Bearbeitung",
  "lead.setStatus": "Status",
  "lead.explainScore": "Score erklären",
  "lead.scoreOverridden": "Manuell überschrieben: {reason}",
  "lead.machineScore": "Maschinen-Score war {score}",
  "lead.overrideScore": "Score überschreiben",
  "lead.clearOverride": "Überschreibung aufheben",
  "lead.overrideReason": "Begründung",
  "lead.machineComputed": "Maschinell berechneter Score",
  "lead.owner": "Verantwortlich: {owner}",
  "lead.ownerYou": "Sie",
  "lead.overriddenBadge": "überschrieben",
  "lead.unassigned": "Nicht zugewiesen",
  "lead.assignToMe": "Mir zuweisen",
  "lead.saveOverride": "Überschreibung speichern",
  "lead.overrideScoreValue": "Score",
  "lead.promoteDialog": "Zum Kontakt machen",
  "lead.trigger": "Auslöser der Übernahme",
  "lead.trigger.inboundReply": "Eingehende Antwort",
  "lead.trigger.meetingBooked": "Termin gebucht",
  "lead.trigger.meetingHeld": "Termin stattgefunden",
  "lead.trigger.humanQualify": "Manuell qualifiziert",
  "lead.evidenceNote": "Beleg-Notiz (optional)",
  "lead.promoteConfirm": "Übernehmen",

  "deals.viewBoard": "Board",
  "deals.viewTable": "Tabelle",
  "deals.amount": "Wert",
  "deals.stage": "Phase",
  "deals.close": "Erwarteter Abschluss",
  "deals.confirmAdvance": "Nach {stage} verschieben?",
  "deals.confirmTerminal":
    "Damit wird der Deal als {status} geschlossen. Erst bestätigen — bis dahin passiert nichts.",
  "deals.lostReason": "Verlustgrund",
  "deals.confirm": "Bestätigen",
  "deals.cancel": "Abbrechen",
  "deals.advanced": "Nach {stage} verschoben",
  "deal.pendingApprovals": "Wartet auf deine Bestätigung",
  "deal.stakeholders": "Beteiligte",
  "deal.edit": "Deal bearbeiten",
  "deal.ownerKeep": "Aktuellen Inhaber behalten",
  "deal.ownerMe": "Mir zuweisen",
  "deal.ownerUnassign": "Zuweisung aufheben",
  "deal.partnerOrg": "Partnerorganisation",
  "deal.forecastCategory": "Forecast-Kategorie",
  "deal.waitUntil": "Warten bis",
  "deal.fxBase": "Basis {value} · Kurs {rate} vom {date}",
  "deal.archive": "Deal archivieren",
  "deal.archiveConfirm":
    "Durch das Archivieren wird dieser Deal aus der aktiven Pipeline entfernt. Dies kann in der Oberfläche nicht rückgängig gemacht werden.",
  "deal.reopen": "Wieder öffnen",
  "deal.reopenPick": "Diesen Deal in eine offene Phase zurücksetzen",
  "deal.reopenConfirm": "Wieder öffnen",
  "deal.fcCommit": "Commit",
  "deal.fcBestCase": "Best Case",
  "deal.fcPipeline": "Pipeline",
  "deal.fcOmitted": "Ausgeschlossen",

  "deals.pipeline": "Pipeline",
  "deals.filterStalled": "Nur ins Stocken geraten",
  "deals.filterOwnerMe": "Meine Deals",
  "deals.filterPartnerSourced": "Über Partner",
  "deals.filterStageAll": "Alle Phasen",
  "deals.filterOrgAll": "Alle Firmen",
  "deals.filterStalledAll": "Alle Deals",
  "deals.filterOwnerAll": "Alle Inhaber",
  "deals.filterPartnerAll": "Alle Quellen",
  "deals.sortNewest": "Neueste",
  "deals.sortClose": "Abschlussdatum",
  "deals.sortAmount": "Größte",

  "deal.offers": "Angebote",
  "deal.newOffer": "Neues Angebot",
  "deal.offerNumber": "Angebots-Nr.",
  "deal.offerRevision": "Rev.",
  "deal.offersEmpty": "Noch keine Angebote",

  "offer.revision": "Revision {revision}",
  "offer.backToDeal": "Zurück zum Deal",
  "offer.totals": "Summen",
  "offer.net": "Netto",
  "offer.tax": "Steuer",
  "offer.gross": "Brutto",
  "offer.edit": "Kopfdaten bearbeiten",
  "offer.currency": "Währung",
  "offer.buyerOrg": "Käufer-Organisation",
  "offer.buyerOrgConfirm": "Käufer-Organisation: {name}",
  "offer.template": "Vorlage",
  "offer.validUntil": "Gültig bis",
  "offer.introText": "Einleitungstext",
  "offer.termsText": "Bedingungen",
  "offer.lines": "Positionen",
  "offer.addLine": "Position hinzufügen",
  "offer.position": "Pos.",
  "offer.description": "Beschreibung",
  "offer.unit": "Einheit",
  "offer.quantity": "Menge",
  "offer.unitPrice": "Einzelpreis",
  "offer.discountPct": "Rabatt %",
  "offer.taxRate": "Steuer %",
  "offer.lineTotal": "Positionssumme",
  "offer.unpriced": "kein Preis — von der Summe ausgeschlossen",
  "offer.removeLine": "Entfernen",
  "offer.pickProduct": "Produkt wählen",
  "offer.pickProductConfirm": "Produkt: {name}",
  "offer.send": "Senden",
  "offer.sendConfirm": "Dieses Angebot an den Käufer senden?",
  "offer.sendBody":
    "Das Angebot wird schreibgeschützt, bis der Käufer antwortet.",
  "offer.accept": "Annehmen",
  "offer.acceptConfirm": "Dieses Angebot als angenommen markieren?",
  "offer.acceptBody":
    "Betrag und Währung des Deals werden an dieses Angebot angeglichen.",
  "offer.reject": "Ablehnen",
  "offer.rejectConfirm": "Dieses Angebot als abgelehnt markieren?",
  "offer.rejectReason": "Grund (optional)",
  "offer.regenerate": "Revision neu generieren",
  "offer.aiDisclosureTitle": "KI-gestützter Hinweis",
  "offer.diffAdded": "{count} Position(en) hinzugefügt",
  "offer.diffRemoved": "{count} Position(en) entfernt",
  "offer.diffChanged": "{count} Position(en) geändert",
  "offer.renderPdf": "PDF erzeugen",
  "offer.viewPdf": "PDF ansehen",
  "offer.pdfUnavailable":
    "PDF-Erzeugung auf diesem Deployment nicht verfügbar.",

  "inbox.sub":
    "alles Vorgemerkte wartet auf deine Entscheidung — ohne sie läuft nichts",
  "inbox.expires": "läuft ab {at}",
  "inbox.viaTool": "über {verb}",
  "inbox.approveEdited": "Bearbeitet übernehmen",
  "inbox.reject": "Ablehnen",
  "inbox.tab.pending": "Ausstehend",
  "inbox.tab.decided": "Entschieden",
  "inbox.rejectReason": "Begründung",
  "inbox.rejectReasonHint":
    "Wird mit der Person geteilt, für die dies vorgemerkt wurde.",
  "inbox.tokenTitle": "Freigabe-Token",
  "inbox.tokenOnce": "Jetzt kopieren — dieses Token wird nur einmal angezeigt.",
  "inbox.copy": "Kopieren",
  "inbox.copied": "Kopiert",
  "inbox.tokenDone": "Fertig",
  "inbox.dismiss": "Schließen",
  "inbox.versionSkew":
    "Dieser Datensatz hat sich seit dem Vormerken geändert — bitte neu vormerken.",
  "inbox.reRead": "Neu einlesen",
  "inbox.alreadyDecided":
    "Bereits entschieden — hier gibt es nichts mehr zu tun.",
  "inbox.expired": "Abgelaufen",
  "inbox.expiresIn": "läuft ab in {countdown}",
  "inbox.detail": "Freigabe-Detail",
  "inbox.status.approved": "Genehmigt",
  "inbox.status.rejected": "Abgelehnt",
  "inbox.status.expired": "Abgelaufen",

  "home.brief": "Morgenbriefing",
  "home.sub": "aus echten Signalen sortiert — Vorgemerktes zuerst",
  "home.staged": "Wartet auf dich",
  "home.stalled": "Deals, die stocken",
  "home.queue": "Heute dran",
  "home.asOf": "Stand {at}",
  "home.refresh": "Briefing aktualisieren",
  "home.refreshing": "Sortiere neu…",
  "home.generate": "Erstes Briefing erstellen",
  "home.noneTitle": "Noch kein Briefing",
  "home.noneBody":
    "Dein Morgenbriefing sortiert die Deals, die deine erste Stunde verdienen — Gewinnchance, Umsatz, Timing, Momentum und Nähe, jeder Faktor mit Beleg. Erstell den ersten Lauf, sobald offene Deals da sind.",
  "home.honestShort":
    "Nur {count} Deals haben die Schwelle geschafft — die Liste wird nie aufgefüllt.",
  "home.overflow":
    "{shown} von {count} qualifizierten Deals — ehrlich kurz, oben die besten.",
  "home.quietRun":
    "Heute Morgen hat nichts die Schwelle geschafft. Keine erfundene Dringlichkeit — genieß die Ruhe.",
  "home.act": "Erledigt",
  "home.dismiss": "Ausblenden",
  "home.actedState": "erledigt",
  "home.dismissedState": "ausgeblendet",
  "home.why": "Warum das oben steht",
  "home.evidence": "{count} Belege",
  "home.evidenceOne": "1 Beleg",
  "home.score": "Score {pct} %",
  "home.openDeal": "Deal öffnen",
  "home.factorWinnability": "Gewinnchance",
  "home.factorRevenue": "Umsatz",
  "home.factorTiming": "Timing",
  "home.factorMomentum": "Momentum",
  "home.factorWarmth": "Nähe",

  "home.digest": "Über Nacht erfasst",
  "home.digestFor": "Digest vom {date}",
  "home.digestSynced": "E-Mails synchronisiert",
  "home.digestPeople": "Personen angelegt",
  "home.digestOrgs": "Firmen angelegt",
  "home.digestApprovals": "Genehmigungen offen",
  "home.digestDedupe": "Dubletten zu prüfen",
  "home.digestClassify":
    "Über Nacht einsortiert: {commitments} Zusagen · {meetings} Termine · {noise} Rauschen",

  "enrich.title": "Von der Website lesen",
  "enrich.sub":
    "belegt oder weggelassen — füllt nur leere Felder, und nur nach deiner Freigabe",
  "enrich.cta": "Jetzt lesen",
  "enrich.reading": "Liest…",
  "enrich.staged":
    "Vorgemerkt — noch nichts geschrieben. Übernimm es im Eingang; nur leere Felder werden gefüllt.",
  "enrich.toInbox": "Eingang öffnen",
  "enrich.from": "gelesen von {url}",

  "deepread.title": "Ganze Website lesen",
  "deepread.sub":
    "Liest bis zu 12 Seiten der Firmenwebsite. Funde werden zur Prüfung vorgemerkt — nichts wird geschrieben, bis du übernimmst.",
  "deepread.cta": "Ganze Website lesen",
  "deepread.starting": "Startet…",
  "deepread.unavailable":
    "Website-Lesen ist auf diesem Server nicht eingerichtet.",
  "deepread.statusQueued": "In Warteschlange",
  "deepread.statusDeferred": "Wartet auf KI-Budget",
  "deepread.statusRunning": "Liest…",
  "deepread.statusDone": "Fertig",
  "deepread.statusPartial": "Früh beendet",
  "deepread.statusFailed": "Fehlgeschlagen",
  "deepread.statusCancelled": "Abgebrochen",
  "deepread.resumesAt": "Wird automatisch am {when} fortgesetzt.",
  "deepread.pagesSoFar.one": "{count} Seite gelesen",
  "deepread.pagesSoFar.other": "{count} Seiten gelesen",
  "deepread.stoppedEarly": "Früh beendet: {reason}",
  "deepread.stopBudget": "Modellbudget",
  "deepread.stopPageCap": "Seitenlimit",
  "deepread.stopByteCap": "Größenlimit",
  "deepread.stopDeadline": "Zeitlimit",
  "deepread.factCount.one": "{count} belegter Fakt vorgemerkt",
  "deepread.factCount.other": "{count} belegte Fakten vorgemerkt",
  "deepread.pagesRead": "Gelesene Seiten",
  "deepread.skippedPages": "Übersprungene Seiten",
  "deepread.skipRobots": "robots.txt",
  "deepread.skipOffDomain": "außerhalb der Domain",
  "deepread.skipPageCap": "Seitenlimit",
  "deepread.skipByteCap": "Größenlimit",
  "deepread.skipUnreadable": "nicht lesbar",
  "deepread.proposals": "{count} Vorschläge warten auf deine Prüfung",
  "deepread.proposalsOne": "1 Vorschlag wartet auf deine Prüfung",
  "deepread.kindHome": "Startseite",
  "deepread.kindImpressum": "Impressum",
  "deepread.kindAbout": "Über uns",
  "deepread.kindTeam": "Team",
  "deepread.kindServices": "Leistungen",
  "deepread.kindProducts": "Produkte",
  "deepread.kindContact": "Kontakt",
  "deepread.kindOther": "Sonstiges",

  "create.cancel": "Abbrechen",
  "create.multiselect.required":
    "Erforderlich – mindestens eine Option wählen.",
  "create.save": "Anlegen",
  "create.saving": "Wird angelegt…",
  "create.contact": "Neuer Kontakt",
  "create.company": "Neue Firma",
  "create.lead": "Neuer Lead",
  "create.deal": "Neuer Deal",
  "create.fullName": "Vollständiger Name",
  "create.firstName": "Vorname",
  "create.lastName": "Nachname",
  "create.personTitle": "Titel",
  "create.email": "E-Mail",
  "create.phone": "Telefon",
  "create.linkedin": "LinkedIn",
  "create.linkedinUrl": "LinkedIn-URL",
  "create.candidateOrgKey": "Firmen-Schlüssel (Kandidat)",
  "create.displayName": "Firmenname",
  "create.legalName": "Rechtlicher Name",
  "create.industry": "Branche",
  "create.sizeBand": "Unternehmensgröße",
  "create.domain": "Primäre Domain",
  "create.companyName": "Firma",
  "create.dealName": "Deal-Name",
  "create.amount": "Wert",
  "create.currency": "Währung",
  "create.stage": "Phase",
  "create.organization": "Firma",
  "create.expectedClose": "Erwarteter Abschluss",

  "field.unset": "Nicht gesetzt",
  "field.addEmail": "E-Mail hinzufügen",
  "field.addPhone": "Telefon hinzufügen",
  "field.addDomain": "Domain hinzufügen",
  "field.domain": "Domain",
  "field.emailType": "Typ",
  "field.emailWork": "Geschäftlich",
  "field.emailPersonal": "Privat",
  "field.emailOther": "Sonstige",
  "field.phoneType": "Typ",
  "field.phoneWork": "Geschäftlich",
  "field.phoneMobile": "Mobil",
  "field.phoneHome": "Privat",
  "field.phoneOther": "Sonstige",
  "field.primary": "Primär",
  "field.removeRow": "Entfernen",
  "field.yes": "Ja",
  "field.no": "Nein",

  "dedupe.viewExisting": "Vorhandenen Eintrag anzeigen",

  "log.title": "Aktivität erfassen",
  "log.sub": "eine Notiz oder Aufgabe, direkt auf diese Timeline",
  "log.kind": "Art",
  "log.kindNote": "Notiz",
  "log.kindTask": "Aufgabe",
  "log.subject": "Betreff",
  "log.body": "Details",
  "log.dueAt": "Fällig am",
  "log.save": "Erfassen",
  "log.saving": "Wird erfasst…",

  "compose.reply": "Antworten",
  "compose.relink": "Neu verknüpfen",
  "compose.draftWithAi": "Mit KI entwerfen",
  "compose.drafting": "Wird entworfen…",
  "compose.discardDraft": "Entwurf verwerfen",
  "compose.discardDraftHint":
    "Meldet Ihrer Voice DNA, dass dieser Entwurf danebenlag. Der erzeugte Text wird nie gespeichert.",
  "compose.aiDisclosureTitle": "KI-gestützter Entwurf",
  "compose.aiDisclosureFallback":
    "Dieser Entwurf stammt von einer KI. Lesen und überarbeiten Sie ihn, bevor Sie senden.",
  "compose.voiceVersion": "Aus Ihrem Korpus gebaut · v{n}",
  "compose.provisional": "Vorläufige Stimme",
  "compose.provisionalHint":
    "Ihre Voice DNA wird noch aufgebaut. Sie prägt diesen Entwurf schon genauso wie eine fertige — es wird nichts zurückgehalten.",
  "compose.intent": 'Entwurf steuern (optional), z. B. "höfliche Nachfrage"',
  "compose.to": "An",
  "compose.cc": "Cc",
  "compose.subject": "Betreff",
  "compose.body": "Nachricht",
  "compose.purpose": "Einwilligungszweck",
  "compose.purposeHint":
    "Der Versand ist nur erlaubt, wenn jeder Empfänger für diesen Zweck eingewilligt hat.",
  "compose.send": "Senden",
  "compose.sendConfirmTitle": "Diese E-Mail senden?",
  "compose.sendBody":
    "Sie senden diese E-Mail jetzt. Dies ist eine ausgehende, unwiderrufliche Aktion.",
  "compose.sendMessageConfirmTitle": "Diese Nachricht senden?",
  "compose.sendMessageBody":
    "Sie senden diese Nachricht jetzt. Dies ist eine ausgehende, unwiderrufliche Aktion.",
  "compose.consentBlockedTitle": "Versand blockiert — keine Einwilligung",
  "compose.consentBlocked":
    "Ein Empfänger hat für diesen Zweck nicht eingewilligt, daher wurde der Versand unterdrückt (Standard-Ablehnung).",
  "compose.consentGoto": "Einwilligung prüfen",
  "compose.draftUnavailable":
    "KI-Entwurf ist nicht verfügbar (das Modell ist nicht konfiguriert). Sie können die E-Mail weiterhin selbst schreiben.",
  "compose.sendUnavailable":
    "Versand ist nicht verfügbar (kein Mailer konfiguriert).",
  "compose.mailboxNotSendCapable":
    "Ihr Postfach ist zum Erfassen verbunden, hat aber nie die Erlaubnis zum Senden erhalten. Verbinden Sie es neu und stimmen Sie dem Versand zu — ein Postfach, das vor der Versandfunktion verbunden wurde, lässt sich nicht nachträglich erweitern.",
  "compose.mailboxNotSendCapableGoto": "Postfach neu verbinden",
  "compose.sharedUnsubscribeToken":
    "Eine Nachricht mit Abmeldelink erreicht immer nur eine Adresse, denn dieser Link ist der Einwilligungsnachweis genau dieses Empfängers. Senden Sie sie einzeln, ohne Cc.",
  "compose.multiRecipientWarning":
    "Dieser Zweck führt einen Abmeldelink mit sich; ein Versand an mehr als eine Adresse wird deshalb abgelehnt. Senden Sie einzeln, ohne Cc.",
  "compose.relinkTitle": "Diese Aktivität neu verknüpfen",
  "compose.relinkTarget": "Person, Organisation, Deal oder Lead suchen",
  "compose.relinkReplace": "Verschieben statt zusätzlich verknüpfen",
  "compose.relinkReplaceHint":
    "Ersetzt die bestehende Verknüpfung desselben Typs, statt eine weitere hinzuzufügen.",
  "compose.relinkConfirm": "Neu verknüpfen",
  "compose.emptyRecipients": "Fügen Sie mindestens einen Empfänger hinzu.",
  "compose.removeRecipient": "{recipient} entfernen",
  "compose.actionFailed":
    "Die Anfrage ist fehlgeschlagen. Bitte erneut versuchen.",

  "tasks.overdue": "Überfällig",
  "tasks.today": "Heute",
  "tasks.upcoming": "Demnächst",
  "tasks.undated": "Ohne Termin",
  "tasks.complete": "Erledigt",
  "tasks.snooze": "1 Tag später",
  "tasks.detail": "Aufgabe",
  "tasks.isDone": "Abgeschlossen",
  "tasks.logged": "Erfasst",
  "tasks.new": "Neue Aufgabe",
  "tasks.subject": "Betreff",
  "tasks.dueDate": "Fällig am",
  "tasks.remindAt": "Erinnere mich am",
  "tasks.remind": "Erinnere mich",
  "tasks.reminder": "Erinnerung",
  "tasks.setReminder": "Erinnerung setzen",
  "tasks.clearReminder": "Erinnerung entfernen",

  "reports.sub": "Deals je Phase — ungewichtet neben gewichtet",
  "reports.count": "Deals",
  "reports.unweighted": "Ungewichtet",
  "reports.weighted": "Gewichtet",
  "reports.planNote":
    "der ausgeführte Plan und die Zeilen, auf die sich die Zahl zurückrechnet",
  "reports.reportDeals": "Deals nach Phase",
  "reports.reportForecast": "Forecast",
  "reports.reportOpenByCompany": "Offene Deals pro Firma",
  "reports.forecastBanner":
    "Kategoriesummen sind ungewichtet; die gewichtete Zahl des Boards ist Σ(Betrag × Phasenwahrscheinlichkeit) — bewusst zwei verschiedene Zahlen.",
  "reports.company": "Firma",
  "reports.openDeals": "Offene Deals",
  "explain.sources": "Quellzeilen",
  "explain.definition": "Wie diese Zahl hergeleitet wird",

  "ai.sub": "bring deinen eigenen Agenten mit — geregelt über die zwei Stufen",
  "ai.fromPalette": "Aus der Palette",
  "ai.tiers": "Was ein Agent darf",
  "ai.tierAutoExecute": "Lesen & Entwerfen läuft sofort.",
  "ai.tierAutoExecuteDetail":
    "Nachschlagen, Zusammenfassen, Entwürfe — sichtbar, umkehrbar, protokolliert.",
  "ai.tierConfirmationRequired": "Schreiben & Senden wartet auf dich.",
  "ai.tierConfirmationRequiredDetail":
    "Externe Sendungen und Datenänderungen landen zuerst im Eingang.",
  "ai.connect": "Agent verbinden",
  "ai.connectDetail":
    "Erstell in den Einstellungen einen Passport und häng einen MCP-fähigen Agenten an deinen Workspace. Er liest nur, was du sehen kannst.",
  "ai.paletteHint": "Frag von überall mit",

  "settings.identity": "Du",
  "role.admin": "Admin",
  "role.manager": "Manager",
  "role.rep": "Vertrieb",
  "role.readOnly": "Nur Lesen",
  "role.ops": "Ops",
  "rbac.masked": "Verborgener Wert",
  "settings.saved": "Gespeichert.",
  "settings.passports": "Agenten-Passports",
  "settings.passportsSub":
    "ein Agent handelt als du, nie über dir — jeder Aufruf prüft deine Rechte neu",
  "passport.select": "Passport",
  "passport.noneOption": "Kein Passport",
  "settings.passportsLendHint":
    "Diese kannst du verleihen. Verbindet sich ein MCP-Client, fragt er, welchen du übergibst — die Verbindung trägt danach genau die Scopes dieses Passports.",
  "settings.passportLabel": "Agentenname",
  "settings.mint": "Passport ausstellen",
  "agents.connected": "Verbundene Agenten",
  "agents.connectedSub":
    "MCP-Clients mit eigenem Credential, abgeleitet aus einem Passport, den du verliehen hast",
  "agents.noneConnected": "Noch ist kein Agent verbunden.",
  "agents.connectedOn": "verbunden {date}",
  "agents.lentFrom": "verliehen aus „{label}“",
  "agents.disconnect": "Trennen",
  "agents.disconnectNamed": "{client} trennen",
  "agents.disconnected": "getrennt",
  "agents.lapsed": "Credential abgelaufen",
  "agents.renewing": "wird erneuert",
  "agents.renewsBy": "Credential erneuert bis {date}",
  "agents.expiredOn": "Credential abgelaufen {date}",
  "agents.revokeGrant": "Verbindung beenden",
  "agents.revokeGrantNamed": "Verbindung zu {client} beenden",
  "agents.disconnectConfirm":
    "Das beendet die ganze Verbindung, nicht nur ein Credential: der Agent verliert den Zugriff beim nächsten Aufruf und kann nicht erneuern. Für eine neue Verbindung verleihst du wieder einen Passport.",
  "agents.connectHow": "Agent verbinden",
  "agents.connectSteps":
    "Stelle oben einen Passport aus und führe dann einen dieser Befehle aus. Der Client registriert sich selbst und bringt dich hierher zurück, um zu wählen, welchen Passport du verleihst.",
  "agents.connectAntigravityPath":
    "Antigravity hat keinen Add-Befehl — trage den Block in ~/.gemini/config/mcp_config.json ein.",
  "agents.connectorOff": "Der MCP-Connector ist für diese Installation aus.",
  "agents.connectorOffDetail":
    "Bis ein Betreiber ihn einschaltet, kann sich kein Agent verbinden. Deine Passports funktionieren weiterhin als REST-Credentials.",
  "settings.tokenOnce": "Jetzt kopieren — dieses Token siehst du nur einmal.",
  "settings.token": "Token",
  "settings.autonomy": "Autonomie-Stufen",
  "settings.autonomySub": "was sofort läuft und was im Eingang wartet",
  "settings.tierRead":
    "Lesen, Zusammenfassen, Entwerfen — läuft sofort, voll protokolliert.",
  "settings.tierSend":
    "E-Mail senden, Termine buchen, Daten ändern — wartet auf deine Freigabe.",
  "settings.tierAdvance": "Deal-Phase weiterschieben — immer erst bestätigen.",
  "settings.locked": "gesperrt",
  "settings.purposes": "Einwilligungszwecke",
  "settings.created": "erstellt {date}",
  "settings.expires": "läuft ab {date}",
  "settings.revoked": "widerrufen",
  "settings.revoke": "Widerrufen",
  "settings.revokeConfirm":
    "Das Credential dieses Passports wird sofort ungültig — der Agent verliert beim nächsten Aufruf den Zugriff.",
  "settings.automations": "Automatisierungen",
  "settings.automationsSub":
    "der geschlossene Starter-Katalog — aktivieren, parametrisieren, pausieren",
  "settings.openAutomations": "Automatisierungs-Editor öffnen",
  "settings.dangerZone": "Gefahrenzone",
  "settings.dangerZoneSub":
    "nur nicht-produktiv — auf dieser Installation nicht rückgängig zu machen",
  "settings.resetDataDesc":
    "Setzt diese Installation auf den Zustand nach der Ersteinrichtung zurück. Fach- und Workspace-Konfigurationsdaten werden gelöscht; die Organisation und ihre Nutzer bleiben erhalten und angemeldet.",
  "settings.resetDataButton": "Daten zurücksetzen…",
  "settings.resetDataConfirmTitle": "Alle Daten zurücksetzen?",
  "settings.resetDataConfirmBody":
    "Gib zur Bestätigung den Namen deiner Organisation ein. Dies kann nicht rückgängig gemacht werden.",
  "settings.resetDataConfirmName": "Gib diesen Organisationsnamen ein:",
  "settings.resetDataConfirmLabel": "Organisationsname bestätigen",
  "settings.resetDataResult":
    "{tables} Tabellen, {jobs} Job-Einträge, {streams} Event-Streams, {keys} Cache-Schlüssel und {objects} gespeicherte Dateien gelöscht.",
  "settings.resetDataDrainWarning":
    "Beim Start des Zurücksetzens lief noch ein Hintergrund-Job. Er schlägt gegen die gelöschten Daten fehl — unkritisch, aber es erscheint ein Fehler im Log.",
  "settings.audit": "Audit-Log",
  "audit.you": "Du",
  "audit.teammate": "Ein Teammitglied",
  "audit.system": "System",
  "audit.onBehalfOfYou": "in deinem Auftrag",
  "audit.onBehalfOfTeammate": "im Auftrag eines Teammitglieds",
  "settings.auditSub": "jede Aktion, zugeordnet — Mensch, Agent oder Connector",
  "settings.auditActor": "Akteur",
  "settings.auditEntity": "Entitätstyp",
  "settings.auditEntityId": "Entitäts-ID",
  "settings.auditAction": "Aktion",
  "settings.auditFrom": "Von",
  "settings.auditTo": "Bis",
  "settings.auditExpand": "Änderungsdetail anzeigen",
  "settings.auditRule": "Berechtigungsregel",
  "settings.auditOnBehalf": "im Auftrag von",
  "settings.privacy": "Datenschutz-Eingang",
  "settings.privacySub": "Betroffenenanfragen mit ihren gesetzlichen Fristen",
  "settings.due": "fällig {date}",

  "privacy.addPurpose": "Zweck hinzufügen",
  "privacy.purposeKey": "Schlüssel",
  "privacy.purposeLabel": "Bezeichnung",
  "privacy.purposeDoi": "Erfordert Double-Opt-in",
  "privacy.purposeCreate": "Zweck anlegen",
  "privacy.purposeAppendOnly":
    "Ein Zweck kann nach dem Anlegen nicht umbenannt oder entfernt werden — der Katalog ist append-only. Wähle den Schlüssel sorgfältig.",
  "privacy.facetAll": "Alle",
  "privacy.overdue": "Überfällig",
  "privacy.closed":
    "Abgeschlossen — eine abgeschlossene Anfrage wird nie wieder geöffnet. Ein neues Anliegen ist eine neue Anfrage.",
  "privacy.assignee": "Zuständig",
  "privacy.assigneeUnassignable":
    "Einmal gesetzt, kann die Zuständigkeit hier nicht entfernt werden.",
  "privacy.resolution": "Ergebnis",
  "privacy.resolutionRequired":
    "Zum Abschließen einer Anfrage braucht es ihre Antwort.",
  "privacy.movedOn":
    "Diese Anfrage ist weitergezogen — jemand anders hat zuerst entschieden. Bitte unten neu lesen.",
  "privacy.inProgress": "In Bearbeitung",
  "privacy.fulfil": "Erfüllen",
  "privacy.reject": "Ablehnen",
  "privacy.newRequest": "Neuer Antrag",
  "privacy.kind": "Art",
  "privacy.person": "Person",
  "privacy.subjectRef": "Betroffenen-Referenz",
  "privacy.dueAt": "Frist",
  "privacy.openRequest": "Antrag anlegen",
  "privacy.erasureNeedsPerson":
    "Ein Löschantrag muss eine Person in diesem Workspace benennen — bei Erfüllung wird genau dieser Datensatz gelöscht. Eine Freitext-Referenz kann nicht gelöscht werden.",
  "privacy.accessManual":
    "Ein Auskunftsantrag wird von Hand erfüllt: Halte im Ergebnis fest, was du versendet hast. Dieses System stellt die Daten nicht automatisch zusammen und exportiert sie nicht für dich.",
  "privacy.fulfilErasureTitle": "Löschantrag erfüllen",
  "privacy.erasureIrreversible":
    "Dies löscht die Person dauerhaft im gesamten System — Datensatz, erfasste Aktivität und abgeleitete Werte. Das kann nicht rückgängig gemacht werden. Die Löschung selbst wird protokolliert.",
  "privacy.typeErase": "Zum Bestätigen ERASE eingeben",
  "privacy.erasureConfirm": "Löschen + sperren",
  "privacy.legalHold":
    "Blockiert — gesetzliche Aufbewahrungspflicht. Diese Person befindet sich innerhalb einer gesetzlichen Aufbewahrungsfrist, daher setzt sich die Löschung hier nicht durch (Art. 17 Abs. 3 lit. b). Die Sperre gilt für jede Rolle, einschließlich Admin — es gibt kein Umgehen davon. Der Versuch wurde protokolliert.",

  "settings.pipelines": "Pipelines",
  "settings.pipelinesSub": "Pipelines und ihre Phasen konfigurieren.",
  "pipeline.new": "Neue Pipeline",
  "pipeline.edit": "Pipeline bearbeiten",
  "pipeline.name": "Name",
  "pipeline.default": "Standard",
  "pipeline.notDefault": "Kein Standard",
  "pipeline.position": "Position",
  "stage.new": "Neue Phase",
  "stage.edit": "Phase bearbeiten",
  "stage.name": "Name",
  "stage.semantic": "Semantik",
  "stage.winProb": "Gewinnwahrscheinlichkeit",
  "stage.semOpen": "Offen",
  "stage.semWon": "Gewonnen",
  "stage.semLost": "Verloren",

  "ob.read": "Einlesen",
  "ob.confirm": "Bestätigen",
  "ob.url": "Website",
  "ob.urlScheme": "https://",
  "ob.back": "Zurück",
  "ob.finish": "In den Workspace",
  "ob.restoring": "Deine Einrichtung wird wiederhergestellt…",
  "ob.readKick": "Schritt 1 von 4 · Firmenkontext",
  "ob.readTitle": "Dein Unternehmen",
  "ob.readSub": "Von deiner Website lesen, oder selbst eintragen.",
  "ob.readChoice": "Wähle, wie du deine Firma beschreiben möchtest",
  "ob.readWebsite": "Meine Website einlesen",
  "ob.readWebsiteSub": "Ich recherchiere; du bestätigst jedes Detail.",
  "ob.readManual": "Erzähl es mir selbst",
  "ob.readManualSub": "Ich frage dich Schritt für Schritt.",
  "ob.readTrustTitle": "Ich lese nur öffentliche Seiten. ",
  "ob.readTrustBody": "Ich speichere nichts, bevor du es bestätigst.",
  "ob.coreIntroTitle": "Zuerst muss ich dein rechtliches Unternehmen kennen.",
  "ob.coreIntroBody":
    "Ich brauche rechtliche Identität, Anschrift und USt-IdNr./UID oder Registerdaten. Danach lerne ich, was ihr verkauft, wen ihr bedient und wie ihr Geschäft gewinnt.",
  "ob.coreLegalKicker": "Ich beginne mit der rechtlichen Identität",
  "ob.corePathLabel": "Was ich lerne",
  "ob.corePathLegal": "Rechtliche Identität",
  "ob.corePathOffer": "Angebot",
  "ob.corePathCustomer": "Kunden",
  "ob.coreReadingPage": "Ich lese gerade",
  "ob.coreWebsiteTitle": "Welche Website soll ich lesen?",
  "ob.coreWebsiteBody":
    "Ich suche zuerst das Impressum und lese danach Produkte, Idealkunden, Positionierung und Vertriebsansatz.",
  "ob.corePreparing": "Ich bereite das Einlesen von {host} vor",
  "ob.coreLegalReading": "Ich lese die rechtliche Identität auf {host}",
  "ob.coreLegalReadingBody":
    "Ich suche Impressum, eingetragene Organisation, Anschrift sowie Register- oder USt-IdNr./UID. Was nicht genannt ist, bleibt leer.",
  "ob.coreBusinessReading": "Ich lerne, wie das Geschäft funktioniert",
  "ob.coreBusinessReadingBody":
    "Ich verbinde Produkte, Kunden und Positionierung mit dem genauen öffentlichen Text, der sie belegt.",
  "ob.coreReady": "Ich habe {count} belegte Firmendaten gefunden",
  "ob.corePartial": "Ich habe {count} nützliche Angaben gefunden — mit Lücken",
  "ob.coreReadyBody":
    "Ich habe noch nichts gespeichert. Bitte prüfe zuerst die rechtliche Identität, danach Angebot und Idealkunden.",
  "ob.coreDeferredBody": "Ich setze das Einlesen automatisch fort.",
  "ob.coreFailedBody":
    "Ich konnte diese Website nicht zuverlässig öffnen oder belegen und habe deshalb aufgehört, statt zu raten. Du kannst mir dieselben Angaben selbst geben.",
  "ob.coreFindingsTitle": "Was ich gefunden habe und belegen kann",
  "ob.coreFindingsBody":
    "Ich hänge an jeden Wert den öffentlichen Wortlaut seiner Quelle. Was ich rechtlich nicht belegen kann, lasse ich leer.",
  "ob.ai.identity": "Hallo, ich bin Margince",
  "ob.ai.role": "Deine KI für Firmenrecherche",
  "ob.ai.speaker": "M",
  "ob.ai.speakerName": "Margince",
  "ob.ai.ready": "Ich bin bereit für die Recherche",
  "ob.ai.configured": "Konfigurierte KI",
  "ob.ai.modelsUsed": "In dieser Aufgabe verwendete Modelle",
  "ob.ai.route": "Aufgabe · Stufe · Provider",
  "ob.ai.calls": "KI-Aufrufe",
  "ob.ai.tokens": "Tokens",
  "ob.ai.latency": "Modell-Latenz",
  "ob.ai.estimatedCost": "Geschätzte Provider-Kosten",
  "ob.ai.partialEstimate": "Teilbetrag · nicht bepreiste Nutzung vorhanden",
  "ob.ai.awaitingModel": "Nach meinem ersten Modellaufruf sichtbar",
  "ob.ai.notAvailableYet": "Noch nicht verfügbar",
  "ob.ai.runtimeUnavailable": "Laufzeitdetails nicht verfügbar",
  // Die Laufzeit-Offenlegung ist ein Chip zum Öffnen, kein Dauerband: Kosten
  // stehen da, WÄHREND sie entstehen, aber wer entscheidet, ob eine
  // Rechtsform stimmt, soll dafür keine Abrechnungstabelle lesen müssen.
  "ob.ai.runtimeChip": "Was antwortet, und was es kostet",
  "ob.ai.answeringNow": "Was gerade antwortet",
  "ob.ai.runScope": "Nur dieser Lauf. Das ganze Protokoll: Einstellungen → KI.",
  "ob.ai.tier.localSmall": "lokal, schnell",
  "ob.ai.tier.cheapCloud": "Cloud, effizient",
  "ob.ai.tier.premium": "Premium-Reasoning",
  "ob.ai.tier.localLarge": "lokal, erweitert",
  // Die Klartext-Zeile im Rail-Footer: Die genauen IDs sind einen Klick
  // entfernt in der Zeile „Konfigurierte KI“ des Laufzeit-Chips — hier steht
  // nur, was auf den ersten Blick zählt: wie viele Modelle, und wo sie laufen.
  "ob.ai.summary.cloud.one": "1 Modell, läuft in der Cloud",
  "ob.ai.summary.cloud.other": "{count} Modelle, laufen in der Cloud",
  "ob.ai.summary.local.one": "1 Modell, läuft lokal",
  "ob.ai.summary.local.other": "{count} Modelle, laufen lokal",
  "ob.ai.summary.hybrid.one": "1 Modell, teils Cloud, teils lokal",
  "ob.ai.summary.hybrid.other": "{count} Modelle, teils Cloud, teils lokal",
  "ob.ai.summary.development.one": "1 Modell, Entwicklungsmodus",
  "ob.ai.summary.development.other": "{count} Modelle, Entwicklungsmodus",
  "ob.ai.summary.none": "Noch kein Modell konfiguriert",
  "ob.ai.summaryProviders.one": "1 Provider konfiguriert",
  "ob.ai.summaryProviders.other": "{count} Provider konfiguriert",
  "ob.ai.readFirst": "Starte zuerst die Firmeneinrichtung.",
  "ob.ai.liveArtifact": "Lebendes, prüfbares Ergebnis",
  "ob.ai.companyKnowledge": "Was ich über dein Unternehmen verstehe",
  "ob.ai.companyKnowledgeBody":
    "Website-Belege bleiben von unserem Gespräch getrennt. Du entscheidest, was Firmenkontext wird.",
  "ob.ai.companyKnowledgeManualBody":
    "Deine Antworten und meine Vorschläge bleiben hier bearbeitbar. Du entscheidest, was Firmenkontext wird.",
  "ob.ai.askPlaceholder":
    "Frag mich zu einem Fund, korrigiere ein Detail oder sag mir, was fehlt…",
  "ob.ai.send": "An Margince senden",
  "ob.ai.reviewBoundary":
    "Ich kann hier Änderungen vorschlagen. Ich übernehme sie erst nach deiner Freigabe in den Entwurf.",
  "ob.ai.confirmBoundary":
    "Nichts wird Firmenkontext, bevor du diesen Entwurf bestätigst.",
  "ob.ai.confirmCompany": "Firma bestätigen und speichern",
  "ob.ai.thinking": "Ich prüfe das Dossier und bereite eine Antwort vor…",
  "ob.ai.suggestedChanges": "Vorgeschlagene Änderungen am Entwurf",
  "ob.ai.applyChanges": "In meinen Entwurf übernehmen",
  "ob.ai.applied": "In Entwurf übernommen",
  "ob.ai.finding": "belegter Fund",
  "ob.ai.findings": "belegte Funde",
  "ob.continueManual": "Erzähl es mir stattdessen",
  "ob.reviewFindings": "Meine Ergebnisse prüfen",
  "ob.live": "Live",
  "ob.readingHost": "Ich erschließe {host}",
  "ob.readStatus.queued": "Ich bereite mich vor",
  "ob.readStatus.deferred": "Ich warte auf KI-Budget",
  "ob.readStatus.reading": "Ich lese gerade",
  "ob.readStatus.ready": "Ich bin mit dem Lesen fertig",
  "ob.readStatus.partial": "Ich bin fertig — mit einigen Lücken",
  "ob.readStatus.failed": "Ich brauche deine Hilfe",
  "ob.readStatus.confirmed": "Ich habe deine Auswahl gespeichert",
  "ob.readStatus.abandoned": "Ich habe aufgehört",
  "ob.phaseDiscover": "Ich entdecke Seiten",
  "ob.phaseExtract": "Ich extrahiere belegte Fakten",
  "ob.phaseReady": "Ich bereite deine Prüfung vor",
  "ob.pagesRead": "Seiten, die ich gelesen habe",
  "ob.legalEntitiesFound": "rechtliche Einheiten, die ich gefunden habe",
  "ob.profileFindings": "Profildetails, die ich gefunden habe",
  "ob.usefulFacts": "weitere Fakten, die ich gefunden habe",
  "ob.coverageDetails": "Was ich abgedeckt und nicht lesen konnte",
  "ob.legalFoundTitle": "Rechtliche Einheiten, die ich gefunden habe",
  "ob.legalFoundBody":
    "Ich halte jeden rechtlichen Block zusammen: eingetragener Name, Anschrift und Register- oder USt-IdNr./UID. Wenn die Website mehrere Einheiten nennt, wählst du deine in der Prüfung aus.",
  "ob.legalEntity": "Rechtliche Einheit",
  "ob.confirmWebsite":
    "Ich habe diese Angaben mit {count} öffentlichen Seiten belegt. Änderungen werden deine Aussage; unveränderte Werte behalten ihre Belege.",
  "ob.confirmManual":
    "Du hast mir diese Angaben direkt gegeben, deshalb speichere ich sie als menschliche Aussagen.",
  "ob.legalTitle": "Welche rechtliche Einheit soll ich verwenden?",
  "ob.legalSub":
    "Ich habe mehrere Einheiten im Impressum gefunden. Wähle deine und ich trage ihre Daten ein.",
  "ob.factsTitle": "Weitere Fakten, die ich gefunden habe",
  "ob.factsSelected": "{selected} von {total} ausgewählt",
  "ob.factsSub":
    "Wähle ab, was nicht Teil des Firmenkontexts werden soll — bis zu 100 Angaben können ausgewählt sein.",
  "ob.nowUnderstands": "Ich verstehe jetzt",
  "ob.contextReady":
    "Ich kann diesen Kontext jetzt für relevante Entwürfe, Suche, Agenten und Voice DNA nutzen — inklusive Herkunft.",

  "ob.s1.kick": "Schritt 2 von 5 · bestätigen",
  "ob.s1.title": "Prüfe, was ich über dein Unternehmen gelernt habe",
  "ob.s1.sub":
    "Ich habe nur ausgefüllt, was ich auf deiner Website belegen konnte. Bitte korrigiere, was nicht stimmt.",
  "ob.s1.urlPlaceholder": "deinefirma.de",
  "ob.s1.identityLabel": "Rechtliche Organisation",
  "ob.s1.offerLabel": "Produkte und Angebot",
  "ob.s1.customerLabel": "Kunde",
  "ob.s1.salesLabel": "Positionierung und Vertriebskontext",
  "ob.s1.fieldRequired": "Pflichtfeld.",
  "ob.s1.requiredMissing": "Diese Felder fehlen noch: {fields}",
  "ob.s1.saving": "Wird gespeichert…",
  "ob.s1.saveFailed": "Deine Firma konnte nicht gespeichert werden",
  "ob.s1.savedNote":
    "In deinem Workspace gespeichert. Ändere hier etwas und geh weiter — dann wird erneut gespeichert.",
  "ob.s1.omitLabel": "Ich habe nichts erfunden",
  "ob.s1.omitBody":
    "Ich habe nur gefüllt, was ich von deiner Seite zitieren kann. Was ich nicht belegen konnte, kannst du ergänzen.",
  "ob.readGo": "Meine Website einlesen",
  "ob.trustPublic": "Ich lese nur deine öffentliche Website. Kein Login nötig.",
  "ob.urlWillRead": "Ich lese {host}",
  "ob.readFromSite": "von der Website gelesen",
  "ob.failTitle": "Ich konnte von dieser Website nicht genug lesen",
  "ob.tryAnother": "Andere URL versuchen",

  "ob.manualChapterLegal": "Deine rechtliche Organisation",
  "ob.manualChapterOffer": "Produkte und Angebot",
  "ob.manualChapterCustomer": "Idealkunde",
  "ob.manualChapterSales": "Wie du verkaufst",
  "ob.manualNext": "Nächste Frage",
  "ob.manualLater": "Später ergänzen",
  "ob.manualReview": "Antworten prüfen",
  "ob.manualRequired": "Erforderlich für ein nutzbares Firmenprofil",
  "ob.manualOptional": "Optional — leer lassen und später ergänzen",
  "ob.manual.display_name":
    "Unter welchem Namen kennen Kunden dein Unternehmen?",
  "ob.manual.display_nameHint":
    "Nutze den vertrauten Firmen- oder Markennamen, der in Margince erscheinen soll.",
  "ob.manual.legal_name":
    "Wie lautet der vollständige eingetragene Firmenname?",
  "ob.manual.legal_nameHint":
    "Inklusive Rechtsform wie GmbH, AG, Ltd oder Inc. Ergänze ihn später, wenn das nicht zutrifft.",
  "ob.manual.registered_address": "Wie lautet die eingetragene Anschrift?",
  "ob.manual.registered_addressHint":
    "Nutze die offizielle Anschrift aus Handelsregister oder Impressum.",
  "ob.manual.register_vat": "Wie lauten Register- und USt-IdNr./UID?",
  "ob.manual.register_vatHint":
    "Trage die Kennungen exakt wie ausgegeben ein. Leer lassen, wenn keine zutrifft.",
  "ob.manual.industry": "In welcher Branche ist das Unternehmen tätig?",
  "ob.manual.industryHint":
    "Wähle die Beschreibung, die deine Kunden sofort verstehen würden.",
  "ob.manual.history": "Welche Firmengeschichte sollte Margince kennen?",
  "ob.manual.historyHint":
    "Zum Beispiel Gründungsjahr, Ursprung oder eine wichtige Veränderung des Geschäfts.",
  "ob.manual.offer_summary": "Welche Produkte oder Leistungen verkauft ihr?",
  "ob.manual.offer_summaryHint":
    "Ein oder zwei konkrete Sätze genügen. Diese Erklärung nutzt Margince für euer Geschäft.",
  "ob.manual.value_proposition": "Welches Ergebnis schafft das Angebot?",
  "ob.manual.value_propositionHint":
    "Erkläre den Kundennutzen, nicht nur die Produktfunktionen.",
  "ob.manual.usp": "Warum entscheiden sich Kunden für euch?",
  "ob.manual.uspHint":
    "Nenne den wichtigsten echten Unterschied zu den Alternativen.",
  "ob.manual.icp": "Wer ist euer Idealkunde?",
  "ob.manual.icpHint":
    "Beschreibe Unternehmen oder Personen mit dem größten Nutzen — Größe, Branche, Situation oder Region.",
  "ob.manual.buying_center": "Wer prüft, kauft oder genehmigt den Kauf?",
  "ob.manual.buying_centerHint":
    "Nenne die typischen Rollen und wer am Ende entscheidet.",
  "ob.manual.customer_pains":
    "Mit welchen Problemen kommen diese Kunden zu euch?",
  "ob.manual.customer_painsHint":
    "Nutze die Probleme, wie Kunden sie selbst beschreiben würden.",
  "ob.manual.desired_outcomes": "Was möchten sie erreichen?",
  "ob.manual.desired_outcomesHint":
    "Beschreibe die praktischen oder geschäftlichen Ergebnisse, die ihnen wichtig sind.",
  "ob.manual.buying_intents": "Was signalisiert üblicherweise Kaufinteresse?",
  "ob.manual.buying_intentsHint":
    "Zum Beispiel eine neue Initiative, Einstellungen, eine Frist oder ein operatives Problem.",
  "ob.manual.common_objections": "Welche Einwände hört ihr am häufigsten?",
  "ob.manual.common_objectionsHint":
    "Nenne Bedenken, die einen Kauf regelmäßig verzögern oder verhindern.",
  "ob.manual.sales_motion": "Wie läuft ein typischer Verkauf ab?",
  "ob.manual.sales_motionHint":
    "Beschreibe den Weg vom ersten Gespräch zur Entscheidung, einschließlich Test oder Einkauf, wenn relevant.",

  "ob.field.display_name": "Firmenname",
  "ob.field.offer_summary": "Was verkaufst du?",
  "ob.field.icp": "Idealkunde",
  "ob.field.buying_center": "Wer kauft",
  "ob.field.value_proposition": "Nutzenversprechen",
  "ob.field.usp": "Was dich unterscheidet",
  "ob.field.customer_pains": "Kundenprobleme",
  "ob.field.desired_outcomes": "Gewünschte Ergebnisse",
  "ob.field.buying_intents": "Kaufanlässe",
  "ob.field.common_objections": "Häufige Einwände",
  "ob.field.sales_motion": "Vertriebsmodell",
  "ob.field.legal_name": "Eingetragener Firmenname",
  "ob.field.registered_address": "Anschrift",
  "ob.field.register_vat": "Register / USt-ID",
  "ob.field.industry": "Branche",
  "ob.field.history": "Firmengeschichte",

  "ob.s3.kick": "Schritt 3 von 4",
  "ob.s3.title": "Sieh, was du gebaut hast —",
  "ob.s3.titleEm": "ganz ohne Anbindung.",
  "ob.s3.sub":
    "Dein Workspace kennt schon dein Geschäft und deine Stimme. Verbinde als Nächstes dein Postfach, und er füllt sich mit deinen echten Menschen, Firmen und Deals.",
  "ob.s3.subNoVoice":
    "Dein Workspace kennt schon dein Geschäft. Verbinde als Nächstes dein Postfach, und er füllt sich mit deinen echten Menschen, Firmen und Deals.",
  "ob.s3.cardProfile": "Geschäftsprofil",
  "ob.s3.cardProfileBody":
    "Bestätigt und auf deiner Firmenseite gespeichert. Von deiner Seite gelesene Felder behalten ihre Quelle; der Rest steht in deinen eigenen Worten.",
  "ob.s3.cardProfileSkippedBody":
    "Von deiner Seite gelesen, aber noch nicht gespeichert — du hast den Bestätigen-Schritt übersprungen. Geh zurück und bestätige, dann landet es auf deiner Firmenseite.",
  "ob.s3.cardVoice": "Deine Schreibstimme",
  "ob.s3.cardVoiceBody":
    "Gebaut aus dem Korpus, den du uns gerade gegeben hast. Entwürfe klingen ab Tag eins nach dir.",
  "ob.s3.cardVoiceSkippedBody":
    "Du hast den Stimme-Schritt übersprungen — Entwürfe starten in einer neutralen Ausgangsstimme, bis du deine baust. Zwei Minuten, jederzeit, in den Einstellungen.",
  "ob.s3.cardPipeline": "Vertriebs-Pipeline",
  "ob.s3.cardPipelineBody":
    "Die Standard-B2B-Vorlage mit 7 Stufen, auf deine Branche vorgestimmt. Leer, bis du verbindest — dann füllen sich Deals aus deiner Post.",
  "ob.s3.cardDraft": "Ein Beispiel-Entwurf, in deiner Stimme",
  "ob.s3.cardDraftExample": "Ein Beispiel-Entwurf",
  "ob.s3.cardDraftBody": "Sieh ihn unten.",
  "ob.s3.exampleTag":
    "Illustratives Beispiel — noch nicht aus deinen Daten geschrieben",
  "ob.s3.exampleProspect": "Nordwind Robotics",
  "ob.s3.draftSample":
    "Betreff: Kurze Frage zu eurer Montagelinie\n\nHallo {{name}} — gesehen, dass {company} diskrete Montage im großen Stil fährt. Wir bringen Teams wie euch in 6 Wochen eine laufende Roboterzelle, ohne eure bestehenden SPS rauszureißen. 15 Minuten wert? Beste Grüße, Lars",
  "ob.s3.originLabel": "Woher diese Pipeline kommt",
  "ob.s3.originBody":
    "Keine Magie — es ist die Standard-B2B-Stufenvorlage, aus dem Schritt-1-Read auf deine Branche gestimmt. Sie ist gerade leer. Wenn du dein Postfach verbindest, liest Capture deine gesendete Post und Meetings und schlägt Deals in diese Stufen vor — jeder belegt und umkehrbar. Du gibst frei, was ein Deal wird.",
  "ob.s3.stillNothing":
    "Noch immer nichts verbunden. Du bestimmst, wann sich das ändert.",

  "ob.s4.sub":
    "Das Hirn steht. Verbinde dein Postfach, und dein CRM füllt sich selbst — Menschen, Firmen und Deals werden automatisch erfasst, du tippst sie nie.",
  "ob.s4.provGoogle": "Google",
  "ob.s4.provMicrosoft": "Microsoft",
  "ob.s4.provImap": "Beliebiges Postfach (IMAP)",
  "ob.s4.microsoftBtn": "Zugriff auf mein Microsoft erlauben",
  "ob.s4.microsoftHint":
    "Nur-Lese-Zugriff auf E-Mails. Du kannst die Verbindung jederzeit in den Einstellungen trennen.",
  "ob.s4.microsoftUnverified":
    "Eventuell erscheint ein Hinweis „nicht verifizierte App“ — das ist diese selbstgehostete Installation, kein Dritter.",
  "ob.s4.microsoftFailed":
    "Die Microsoft-Verbindung wurde nicht abgeschlossen.",
  "ob.s4.connectOkTitle": "Verbunden",
  "ob.s4.connectOkBody":
    "Dein Postfach ist verknüpft. Die Erfassung startet beim nächsten Sync.",
  "ob.s4.connectVerifying": "Verbindung wird bestätigt…",
  "ob.s4.connectLive": "Aktiv und erfassend",
  "ob.s4.connectConfirmFailed": "Die Verbindung konnte nicht bestätigt werden.",
  "ob.s4.connectRetry":
    "Öffne Einstellungen → Integrationen, um es erneut zu versuchen.",
  "ob.s4.connectDenied":
    "Du hast den Zugriff abgelehnt — es wurde nichts verbunden.",
  "ob.s4.googleBtn": "Zugriff auf mein Gmail erlauben",
  "ob.s4.soon": "Bald",
  "ob.s4.googleHint":
    "Nur Lesezugriff. Du bestätigst ihn auf Googles eigener Einwilligungsseite — und du kannst die Verbindung jederzeit wieder trennen.",
  "ob.s4.googleUnverified":
    "Falls Google einen Hinweis „nicht verifizierte App“ zeigt, wähle Erweitert → Fortfahren. Margince liest deine Mail nur — es sendet nie.",
  "ob.s4.googleOkTitle": "Gmail verbunden",
  "ob.s4.googleOkBody":
    "Die Erfassung läuft im Hintergrund — neue Mails erscheinen innerhalb von etwa einer Minute auf deiner Timeline und bleiben ab jetzt von selbst synchron.",
  "ob.s4.googleLive": "Verbindung bestätigt — Hintergrund-Erfassung läuft",
  "dedupe.title": "Mögliche Duplikate",
  "dedupe.intro":
    "Paare, die die Erfassung als vermutlich dieselbe Person oder Firma erkannt hat. Zusammenführen erhält die Historie beider Datensätze; Verwerfen bedeutet: nie wieder nach diesem Paar fragen.",
  "dedupe.loading": "Prüf-Warteschlange wird geladen…",
  "dedupe.empty": "Keine Duplikate offen — die Warteschlange ist leer.",
  "dedupe.confidence": "Übereinstimmung:",
  "dedupe.field": "Feld",
  "dedupe.left": "Links behalten",
  "dedupe.right": "Rechts behalten",
  "dedupe.kindPerson": "Person",
  "dedupe.kindOrganization": "Firma",
  "dedupe.mergeCta": "In Auswahl zusammenführen",
  "dedupe.notDuplicateCta": "Kein Duplikat",
  "dedupe.decided": "Entscheidung gespeichert.",
  "dedupe.undoCta": "Rückgängig",
  "dedupe.undone": "Paar wieder geöffnet.",
  "dedupe.dismissNote": "Ausblenden",
  "backfill.title": "E-Mail-Verlauf importieren",
  "backfill.intro":
    "Wählen Sie, wie weit zurück importiert wird. Umfang und geschätzte Kosten sehen Sie vor dem Start — Sie können diesen Schritt auch überspringen.",
  "backfill.windowLabel": "Import-Zeitraum",
  "backfill.window3m": "3 Monate",
  "backfill.window6m": "6 Monate",
  "backfill.window12m": "12 Monate",
  "backfill.previewCta": "Erst den Umfang zeigen",
  "backfill.previewLoading": "Postfach wird gezählt…",
  "backfill.estimateMessages": "Nachrichten in diesem Zeitraum:",
  "backfill.estimateCost": "Geschätzte KI-Kosten:",
  "backfill.estimateNote":
    "Eine Schätzung, keine Rechnung — der tatsächliche Verbrauch wird laufend gemessen und angezeigt.",
  "backfill.startCta": "Import starten",
  "backfill.starting": "Wird gestartet…",
  "backfill.skip": "Verlaufs-Import überspringen",
  "backfill.skippedNote":
    "Kein Verlauf importiert. Neue Mails werden ab jetzt trotzdem erfasst — der Import lässt sich später in den Einstellungen starten.",
  "backfill.loading": "Import-Status wird geprüft…",
  "backfill.statusUnavailable":
    "Der Import-Status ist gerade nicht lesbar — die Erfassung selbst läuft weiter.",
  "backfill.queuedTitle": "Import eingereiht",
  "backfill.runningTitle": "E-Mail-Verlauf wird importiert",
  "backfill.doneTitle": "Verlaufs-Import abgeschlossen",
  "backfill.errorTitle": "Der Import hat ein Problem",
  "backfill.cancelledTitle": "Import abgebrochen",
  "backfill.progressLabel": "Import-Fortschritt",
  "backfill.countScanned": "Nachrichten durchsucht",
  "backfill.countCaptured": "Erfasst",
  "backfill.statEmails": "E-Mails erfasst",
  "backfill.statPeople": "Personen",
  "backfill.statCompanies": "Firmen zu prüfen",
  "backfill.errorNote":
    "Er versucht es selbstständig erneut; alles bisher Erfasste bleibt erhalten.",
  "backfill.cancel": "Import stoppen",
  "backfill.cancelledNote": "Gestoppt. Alles bisher Erfasste bleibt erhalten.",
  "backfill.unsupportedNote":
    "Dieser Postfachtyp kann nicht rückwirkend importiert werden — ab jetzt werden nur neue E-Mails erfasst.",
  "backfill.narrowingNote":
    "Für dieses Postfach lief bereits ein größerer Zeitraum; der Import-Zeitraum kann nur erweitert, nicht verkleinert werden.",
  "backfill.staleUpdated":
    "Zuletzt aktualisiert vor {duration} — kein aktueller Fortschritt.",

  // Connected inboxes (Einstellungen → Integrationen).
  "connectors.title": "Verbundene Postfächer",
  "connectors.sub":
    "Postfächer, die dein CRM automatisch füllen. Trenne eines bei Bedarf — bereits erfasste Datensätze bleiben.",
  "connectors.loading": "Verbindungen werden geladen…",
  "connectors.loadFailed": "Verbindungen konnten nicht geladen werden.",
  "connectors.empty": "Noch kein Postfach verbunden.",
  "connectors.connectCta": "Postfach verbinden",
  "connectors.provGmail": "Gmail",
  "connectors.provGcal": "Google Kalender",
  "connectors.provGraph": "Microsoft",
  "connectors.provImap": "IMAP-Postfach",
  "connectors.statusConnected": "Aktiv",
  "connectors.statusPending": "Ausstehend — noch nicht bestätigt aktiv",
  "connectors.statusReauth": "Neu verbinden nötig",
  "connectors.statusError": "Sync-Fehler",
  "connectors.statusDisconnected": "Getrennt",
  "connectors.cannotSend": "Nur Erfassung — kein Versand",
  "connectors.reconnectToSend":
    "Verbinden Sie dieses Postfach neu, um daraus zu senden. Ein Postfach, das vor der Versandfunktion verbunden wurde, lässt sich nicht nachträglich erweitern — der Anbieter erteilt die Sendeerlaubnis nur bei einer neuen Verbindung.",
  "connectors.lastSynced": "Zuletzt synchronisiert {at}",
  "connectors.neverSynced": "Wartet auf die erste Synchronisierung",
  "connectors.nextCheck": "Nächste Prüfung ~{at}",
  "connectors.polled": "Wird gepollt (kein Push-Abo)",
  "connectors.pushRenewal": "Push-Erneuerung bis {at}",
  "connectors.notConfigured":
    "Die Mail-Erfassung ist in dieser Installation nicht konfiguriert.",
  "connectors.reconnect": "Neu verbinden",
  "connectors.disconnect": "Trennen",
  "connectors.disconnectTitle": "Dieses Postfach trennen?",
  "connectors.disconnectBody":
    "Dies löscht die für dieses Postfach gespeicherte Zugangsdaten. Die Erfassung stoppt sofort; alles bereits Erfasste bleibt in deinem CRM, und beim erneuten Verbinden wird wieder um Erlaubnis gebeten.",
  "connectors.disconnectBodyGoogleNote":
    "Google listet Margince unter Umständen weiterhin unter den Drittanbieter-Zugriffen deines Kontos — entferne es dort, wenn du den Zugriff vollständig widerrufen möchtest.",
  "connectors.disconnectBodyMicrosoftNote":
    "Microsoft listet Margince unter Umständen weiterhin unter den verbundenen Apps deines Kontos — entferne es dort, wenn du den Zugriff vollständig widerrufen möchtest.",
  "connectors.errRateLimited":
    "Der Anbieter drosselt uns. Die Erfassung läuft langsamer als sonst; es geht nichts verloren.",
  "connectors.errUnreachable":
    "Wir konnten den Anbieter nicht erreichen. Wir versuchen es weiter.",
  "connectors.errAuth":
    "Der Anbieter hat unsere Zugangsdaten abgelehnt. Neu verbinden, um fortzufahren.",
  "connectors.errHistoryGone":
    "Der Änderungsverlauf des Anbieters ist abgelaufen. Die nächste Synchronisierung setzt neu an.",
  "connectors.errInternal":
    "Bei uns ist etwas schiefgelaufen. Wir haben gestoppt, statt unvollständige Daten zu erfassen.",
  "connectors.errUnknown":
    "Bei der Erfassung ist ein Problem aufgetreten, das wir noch nicht einordnen können. Wir versuchen es weiter.",

  // Das OAuth-Rückkehrergebnis (Task 2): der Callback landet auf
  // #/settings/integrations/{outcome} — ein schließbarer Hinweis, gesteuert
  // von diesem Routensegment.
  "connectors.oauthOk": "Verbunden. Ihr Postfach erfasst jetzt.",
  "connectors.oauthDenied":
    "Sie haben den Zugriff abgelehnt — es wurde nichts verbunden.",
  "connectors.oauthError":
    "Die Verbindung konnte nicht hergestellt werden — bitte versuchen Sie es erneut.",
  // Zwei Fälle, für die "erneut versuchen" falsch wäre: der Anbieter hat die
  // Freigabe abgelehnt, und die API des Anbieters ist für diese Installation
  // nicht aktiviert (das kann keine Nutzeraktion beheben).
  "connectors.oauthRejected":
    "Der Anbieter hat die Verbindung abgelehnt. Bestätigen Sie alle angefragten Berechtigungen und versuchen Sie es dann erneut.",
  "connectors.oauthMisconfigured":
    "Diese Installation kann die Verbindung noch nicht abschließen — die API des Anbieters ist dafür nicht aktiviert. Ein Administrator muss sie aktivieren; das Server-Log nennt die betroffene API.",
  "connectors.dismissOutcome": "Schließen",

  // Das immer sichtbare "Verbindung hinzufügen"-Element (Task 1): der
  // Leerzustand und die Fußzeile der Liste teilen sich dieselben Buttons für
  // noch nicht verbundene Anbieter.
  "connectors.addConnection": "Verbindung hinzufügen",
  "connectors.googleSeparateNote":
    "Gmail und Google Kalender werden separat verbunden.",
  "connectors.providerNotConfigured":
    "{provider} ist in dieser Installation nicht konfiguriert.",

  // Das eingebettete IMAP-Verbindungsformular (Task 6).
  "connectors.imapConnectCta": "IMAP-Postfach verbinden",
  "connectors.imapModalTitle": "IMAP-Postfach verbinden",
  "connectors.imapHost": "IMAP-Server",
  "connectors.imapPort": "Port",
  "connectors.imapUsername": "E-Mail-Adresse",
  "connectors.imapSecret": "App-Passwort",
  "connectors.imapMailbox": "Postfach",
  "connectors.imapMaxMessages": "Nachrichten pro Synchronisierung",
  "connectors.imapSecretHint":
    "Verwende ein App-Passwort. Wir versiegeln es im Credential-Vault und lesen deine Mails nach Zeitplan, bis du trennst — beim Trennen wird es gelöscht.",
  "connectors.imapSubmitCta": "Verbinden",
  "connectors.imapLoginRejected":
    "Das Postfach hat diese Zugangsdaten abgelehnt. Prüfe Server, E-Mail und App-Passwort.",
  "connectors.imapUnreachable": "Der Mailserver konnte nicht erreicht werden.",

  // Das Telegram-Connector-Panel (Task 17, Design §9.1-§9.2): ein Bot
  // verbindet sich für den gesamten Workspace — kein OAuth-Handshake,
  // sondern ein BotFather-Token im selben eingebetteten Formular wie beim
  // IMAP-Connector. Anders als bei den Mail-Anbietern bleibt die Verbindung
  // vor Ort bearbeitbar: das Ersetzen des Tokens läuft über PATCH, nie über
  // ein Trennen.
  "connectors.provTelegram": "Telegram",
  "connectors.telegramTitle": "Telegram-Bot",
  "connectors.telegramSub":
    "Ein Bot empfängt und sendet Nachrichten für den gesamten Workspace.",
  "connectors.telegramNotConfigured":
    "Messaging-Kanäle sind in dieser Installation nicht konfiguriert.",
  "connectors.telegramConnectCta": "Telegram-Bot verbinden",
  "connectors.telegramEditToken": "Token ersetzen",
  "connectors.telegramDisconnectTitle": "Diesen Bot trennen?",
  "connectors.telegramDisconnectBody":
    "Dies löscht das gespeicherte Token und beendet das Abrufen neuer Nachrichten. Erfassung und Versand stoppen sofort; alles bereits Erfasste bleibt in deinem CRM.",
  "connectors.telegramModalTitle": "Telegram-Bot verbinden",
  "connectors.telegramEditTitle": "Bot-Token ersetzen",
  "connectors.telegramBotToken": "Bot-Token",
  "connectors.telegramBotTokenHint":
    "Füge das Token ein, das BotFather beim Anlegen des Bots ausgegeben hat. Wir versiegeln es im Credential-Vault und zeigen es nie wieder an.",
  "connectors.telegramSubmitCta": "Verbinden",
  "connectors.telegramReplaceCta": "Token ersetzen",
  "connectors.telegramConnectedAs": "Verbunden als @{username}.",

  // Die Consumer-Mail-Liste des Workspace (CAP-PARAM-5).
  "consumerMail.title": "Consumer-Mail-Domains",
  "consumerMail.sub":
    "Mail von einem privaten Postfach legt weiterhin die Person an — nur eben keine Firma. Margince liefert eine Liste dieser Anbieter mit; ergänze, was fehlt, oder nimm eine Domain wieder heraus, die zu Unrecht darauf steht.",
  "consumerMail.domainLabel": "Domain",
  "consumerMail.domainPlaceholder": "anbieter.example",
  "consumerMail.kindLabel": "Was diese Domain ist",
  "consumerMail.kind.extra": "Consumer-Mail — niemals eine Firma",
  "consumerMail.kind.never":
    "Eine echte Firma — mitgelieferte Liste ignorieren",
  "consumerMail.add": "Hinzufügen",
  "consumerMail.remove": "Entfernen",
  "consumerMail.none":
    "Nichts ergänzt. Die mitgelieferte Liste entscheidet über jede Domain.",
  "consumerMail.adminOnly":
    "Du hast keine Berechtigung, diese Liste zu ändern.",

  "ob.s4.googleVerifying": "Verbindung wird geprüft…",
  "ob.s4.googleDenied": "Du hast die Google-Einwilligung abgelehnt",
  "ob.s4.googleFailed": "Die Google-Verbindung wurde nicht abgeschlossen",
  "ob.s4.googleRetry":
    "Es wurde nichts gespeichert. Versuch es jederzeit erneut — oder verbinde stattdessen über IMAP.",
  "ob.s4.imapHost": "IMAP-Host",
  "ob.s4.imapHostPlaceholder": "imap.gmail.com",
  "ob.s4.imapPort": "Port",
  "ob.s4.imapEmail": "E-Mail",
  "ob.s4.imapPassword": "App-Passwort", // NOSONAR: UI translation string, not a credential
  "ob.s4.imapMailbox": "Postfach",
  "ob.s4.imapMax": "Wie viele aktuelle E-Mails",
  "ob.s4.imapHint":
    "Nutz ein App-spezifisches Passwort (Gmail: Konto → Sicherheit → App-Passwörter). Wir versiegeln es im Credential-Vault und lesen weiter neue Post, bis du die Verbindung trennst — beim Trennen wird es gelöscht.",
  "ob.s4.imapConnect": "Testen und verbinden",
  "ob.s4.connecting": "Sichere Verbindung…",
  "ob.s4.scope1Lead": "Wir lesen — wir müllen nichts voll.",
  "ob.s4.scope1Rest":
    "Deine Post wird automatisch zu Kontakten, Firmen und Aktivitäten.",
  "ob.s4.scope2Lead": "Wir senden nie etwas ohne deine Freigabe.",
  "ob.s4.scope2Rest": "Entwürfe warten in deinem Freigabe-Eingang.",
  "ob.s4.scope3Lead": "Deine Daten bleiben in deinem Workspace.",
  "ob.s4.scope3Rest":
    "Own-your-data — jederzeit alles exportieren oder löschen.",
  "ob.s4.scope4Lead": "Trennung mit einem Klick.",
  "ob.s4.scope4Rest": "Das CRM läuft weiter; es hört nur auf zu erfassen.",
  "ob.s4.capturedTitle": "Postfach verbunden",
  "ob.s4.capturedBody":
    "Lehn dich zurück — dein CRM baut sich von selbst auf. Neue Post trifft hier ein, sobald der erste Durchlauf läuft, meist innerhalb weniger Minuten.",
  "ob.s4.enterCrm": "Ins CRM",
  "ob.s4.connectFailed": "Dieses Postfach ließ sich nicht verbinden",
  "ob.s4.notNow": "Nicht jetzt",

  "ob.conv.threadLabel": "Einrichtungsgespräch",
  "ob.conv.welcome":
    "Hallo, ich bin Margince. Ich richte dein CRM ein, indem ich lese, was über dein Unternehmen bereits belegt ist, und zeige zu allem eine Quelle.",
  "ob.conv.welcomeMember":
    "Hallo, ich bin Margince. Dein Team ist schon eingerichtet. Zwei kurze Schritte, dann bist du drin.",
  "ob.conv.read.started": "Ich lese jetzt {host}. Ich sage dir, was ich finde.",
  "ob.conv.read.pages": "Bisher gelesene Seiten: {pages}.",
  "ob.conv.read.learnedField": "{field} gelernt: {value}",
  "ob.conv.read.extracting":
    "Das Durchsuchen ist fertig. Jetzt werte ich aus, was die Website über dein Geschäft sagt.",
  "ob.conv.read.warning": "Hinweis: {warning}",
  "ob.conv.read.failed":
    "Ich konnte diese Website nicht lesen. Probiere eine andere URL oder sag es mir direkt.",
  "ob.conv.read.pollFailed":
    "Ich habe die Verbindung beim Lesen verloren. Was ich schon gefunden habe, bleibt erhalten.",
  "ob.conv.read.deferred":
    "Das Einlesen pausiert gerade. Ich setze es automatisch fort.",
  "ob.conv.clarify.intro":
    "Eine Sache musst du entscheiden. Die Website ist hier nicht eindeutig.",
  "ob.conv.clarify.entity":
    "Die Website nennt mehr als eine juristische Person. Für welche ist diese Installation?",
  "ob.conv.review.ready":
    "Ich habe die Zuordnung vorbereitet. Prüfe sie und bestätige, was stimmt.",
  "ob.conv.company.confirmed":
    "Firmenprofil bestätigt. Alles Gespeicherte trägt seine Quelle.",
  "ob.conv.manual.chosen": "Ich tippe es selbst ein.",
  "ob.conv.voice.skipped": "Stimme erstmal überspringen.",
  "ob.conv.voice.uploadAdded": "{name} hinzugefügt.",
  "ob.conv.voice.speakerQuestion":
    "Dieses Transkript hat mehrere Sprecher. Wer davon bist du? Nur deine eigenen Worte zählen.",
  "ob.conv.voice.speakerOptionDetail": "Wörter: {words} · Beiträge: {turns}",
  "ob.conv.voice.guideSpeaker":
    "Rechts wartet eine Sprecherwahl — wähle, welche Person du bist.",
  "ob.conv.voice.speakerFoot": "Deine Wahl gilt nur für diese Datei.",
  "ob.conv.voice.speakerContinue": "Diese Person bin ich",
  "ob.conv.voice.continueSkippedStatus":
    "Erstmal übersprungen — später in den Einstellungen nachholbar.",
  "ob.conv.voice.continueFailedStatus":
    "Deine Materialien sind sicher — versuch es erneut oder mach weiter und komm später zurück.",
  "ob.conv.voice.continueDeferredStatus":
    "Hier ist nichts zu tun — mach weiter, der Rest läuft von selbst.",
  "ob.conv.voice.collectAsk":
    "Schick mir Texte, die du geschrieben hast. Gesprächs-Transkripte sind am besten: .vtt, .srt, .json oder Text mit Sprecher-Labels. Einfache Dokumente gehen auch.",
  "ob.conv.voice.composer": "Füge hier deinen Text ein",
  "ob.conv.voice.dropHint":
    "Du kannst Dateien auch überall in dieses Gespräch ziehen.",
  "ob.conv.voice.fileSkipped":
    "Ich kann {name} nicht lesen. Ich nehme .txt, .md, .vtt, .srt oder .json.",
  "ob.conv.voice.fileEmpty":
    "In {name} stehen keine Wörter, also wurde nichts gezählt.",
  "ob.conv.voice.reactionTranscript":
    "Behaltene Wörter: {kept} von {total}. Nur deine Beiträge zählen, und gesprochene Sprache schärft deine Stimme am meisten.",
  "ob.conv.voice.reactionDocument":
    "Gezählte Wörter: {words}. Jedes Wort hier ist deins, also zählen alle.",
  "ob.conv.voice.refusalUnattributed":
    "Das sieht nach einem Gespräch aus, aber ich kann nicht erkennen, welche Wörter deine sind. Ich habe nichts gezählt, denn ich zähle nur Wörter, die nachweislich von dir stammen.",
  "ob.conv.voice.refusalSpeaker":
    "Ich konnte diesen Sprecher im Transkript nicht finden. Nichts wurde gezählt.",
  "ob.conv.voice.refusalUnsupported":
    "Ich konnte diese Datei weder als Text noch als Transkript lesen. Nichts wurde gezählt.",
  "ob.conv.voice.ingestFailed":
    "Ich konnte diese Quelle nicht hinzufügen: {detail}",
  "ob.conv.voice.ingestUnexpected":
    "Ich konnte diese Quelle nicht hinzufügen. Versuch es gleich noch einmal.",
  "ob.conv.voice.pasteAdd": "Ja, in meinen Korpus.",
  "ob.conv.voice.pasteDiscard": "Nein, verwerfen.",
  "ob.conv.voice.pasteSource": "Eingefügter Text",
  "ob.conv.voice.buildFloor":
    "Eigene Wörter bisher: {words}. Ich brauche mindestens {min}, bevor ich bauen kann.",
  "ob.conv.voice.buildNudge":
    "Ich habe genug zum Bauen. Mehr Material hilft trotzdem: Ab 4.000 Wörtern wird deine Stimme deutlich schärfer.",
  "ob.conv.voice.buildChip": "Mein Stimmprofil bauen",
  "ob.conv.voice.retryBuild": "Aufbau erneut versuchen",
  "ob.conv.voice.buildPollFailed":
    "Ich habe die Verbindung während des Aufbaus verloren. Deine Texte bleiben erhalten. Versuche den Aufbau erneut.",
  "ob.conv.voice.statusBuilding": "Dein Stimmprofil entsteht",
  "ob.conv.voice.resultTitle":
    "Das ist deine Stimme, in deinen eigenen Worten.",
  "ob.conv.voice.resultLoading": "Ich lade, was der Aufbau gelernt hat.",
  "ob.conv.voice.resultEmpty":
    "Der Aufbau ist fertig, aber es gibt noch nichts zu zeigen. Du kannst ihn in den Einstellungen prüfen.",
  "ob.conv.voice.candidateNote":
    "Diese Version braucht deine Prüfung, bevor sie aktiv wird. Freigeben kannst du sie in den Einstellungen.",
  "ob.conv.voice.artifactTitle": "Stimm-Korpus",
  "ob.conv.voice.artifactBody":
    "Hier zählen nur deine eigenen Wörter. Jede Zahl kommt vom Server, nach dem Sprecher-Filter.",
  "ob.conv.voice.artifactEmpty":
    "Noch nichts gesammelt. Hänge ein Transkript oder einen eigenen Text an.",
  "ob.conv.voice.meterWords": "Eigene Wörter: {words} von {target}",
  "ob.conv.voice.meterBand": "Qualität: {band}",
  "ob.conv.voice.manifestKept": "{kept} von {total} Wörtern behalten",
  "ob.conv.voice.manifestWords": "{words} Wörter",
  "ob.conv.voice.registerMix": "Register: {mix}",
  "ob.conv.voice.stageTitle": "Aufbau-Fortschritt",
  "ob.conv.corpus.words": "Eigene Wörter jetzt im Korpus: {words}.",
  "ob.conv.corpus.band": "Korpusqualität ist jetzt {band}.",
  "ob.conv.build.snapshot": "Ich friere deinen Korpus ein.",
  "ob.conv.build.extract": "Ich suche deine typischen Formulierungen.",
  "ob.conv.build.evaluate": "Ich teste Entwürfe gegen zurückgehaltene Proben.",
  "ob.conv.build.activate": "Ich aktiviere dein Stimmprofil.",
  "ob.conv.build.succeeded": "Dein Stimmprofil ist fertig.",
  "ob.conv.build.deferred":
    "Der Aufbau wartet auf Budget. Er läuft automatisch an.",
  "ob.conv.build.failed":
    "Der Aufbau wurde nicht fertig. Deine Texte bleiben erhalten, du kannst es jederzeit erneut versuchen.",
  "ob.conv.recap":
    "Das weiß dein CRM jetzt, mit einer Quelle zu jedem Eintrag.",
  "ob.conv.consent":
    "Letzter Schritt: Was darf ich erfassen, und zu welchem Zweck? Nichts ist standardmäßig aktiv.",
  "ob.conv.done": "Einrichtung abgeschlossen. Dein CRM ist bereit.",
  "ob.conv.composer": "Frag mich, oder gib deine Website ein",
  "ob.conv.clarify.question": "{question}",
  "ob.conv.clarify.optionDetail": "{detail}",
  "ob.conv.clarify.dismiss": "Überspringen - ich trage es selbst ein",
  "ob.conv.clarify.keepMine": "Meinen Wert behalten",
  "ob.conv.review.skipped":
    "Du hast übersprungen: {fields}. Du kannst sie jederzeit bearbeiten.",
  "ob.conv.clarify.applyFailed":
    "Ich konnte diese Wahl nicht übernehmen: {detail} Wähle bitte erneut.",
  "ob.conv.clarify.applyMissing":
    "Der Server hat diese Wahl nicht bestätigt. Wähle bitte erneut.",
  "ob.conv.loadFailed":
    "Ich konnte deine Einrichtung nicht prüfen. Bitte versuche es erneut.",
  "ob.conv.retry": "Erneut versuchen",
  "ob.conv.connect.persistFailed":
    "Ich konnte den Abschluss nicht speichern. Versuche es erneut.",
  "ob.conv.review.title":
    "Hier ist alles, was ich gefunden habe. Korrigiere mich.",
  "ob.conv.review.showMore": "Ganzen Text zeigen",
  "ob.conv.review.showLess": "Weniger zeigen",
  "ob.conv.review.continue": "Weiter",
  "ob.conv.review.progressLabel": "Ausgefüllte Pflichtfelder",
  "ob.conv.review.requiredRemaining.one":
    "{count} Feld nötig, bevor du fortfahren kannst",
  "ob.conv.review.requiredRemaining.other":
    "{count} Felder nötig, bevor du fortfahren kannst",
  "ob.conv.review.requiredDone": "Nichts weiter nötig — du kannst fortfahren.",
  "ob.conv.review.confirmQuestionOpen":
    "Eine Entscheidung ist noch offen. Beantworte sie, um fortzufahren.",
  "ob.conv.triage.stateRequired": "erforderlich, noch leer",
  "ob.conv.triage.stateEmpty": "leer",
  "ob.conv.triage.stateTyped": "von dir eingetragen",
  "ob.conv.triage.stateStored": "aus deinem Profil",
  "ob.conv.triage.stateQuoted": "aus deinem Impressum gelesen",
  "ob.conv.triage.emptyHint":
    "Hier steht noch nichts. Trag es ein, wenn es zählt.",
  "ob.conv.triage.legalNotPublished":
    "Nicht auf deinem Impressum angegeben. Trag es selbst ein.",
  "ob.conv.triage.legalNotChecked":
    "Ich habe kein Impressum auf deiner Website gefunden, das ich prüfen könnte. Trag es selbst ein.",
  "ob.conv.triage.legalUnpicked":
    "Dein Impressum nennt mehr als ein Unternehmen. Wähl aus, welches deins ist, dann trage ich es ein.",
  "ob.conv.triage.omittedLabel": "Ausgelassen, nicht geraten",
  "ob.conv.triage.omittedField": "{field}: {reason}",
  "ob.conv.triage.mapLabel": "Zu einem Abschnitt springen",
  "ob.conv.triage.sectionBlocking": "{count} nötig, um fortzufahren",
  "ob.conv.triage.sectionAdvisory": "{count} prüfenswert",
  "ob.conv.triage.blockingHead": "Nötig, um fortzufahren",
  "ob.conv.triage.advisoryHead": "Prüfenswert",
  "ob.conv.triage.sectionSettled": "Hier ist nichts offen",
  "ob.conv.triage.sectionMore": "+{count} weitere",
  "ob.conv.triage.restTitle": "Hintergrund, keine Aufgabe",
  "ob.conv.triage.looksSolid": "Sieht belegt aus · {count}",
  "ob.conv.triage.companyWebsite": "Website",
  "ob.conv.triage.sourceCount": "{count} Quelle",
  "ob.conv.triage.peopleLabel": "Personen",
  "ob.conv.triage.peopleCount": "{count} gefunden",
  "ob.conv.triage.peopleEmpty": "Keine Personen auf deiner Website gefunden.",
  "ob.conv.triage.factsLabel": "Fakten",
  "ob.conv.triage.factsCount": "{count} gefunden",
  "ob.rail.spend": "Tokens für dieses Setup",
  "ob.rail.tokensUnit": "Tok.",
  "ob.conv.scene.step": "Schritt {n} von {m} · {label}",
  "ob.conv.scene.detour": "Ein kurzer Umweg",
  "ob.conv.scene.decisionSub":
    "Deine Website nennt mehr als eine juristische Einheit, und ich rate nicht, welche deine Verträge unterschreibt: Sie entscheidet, was auf jedem Angebot und jeder Rechnung steht.",
  "ob.conv.scene.continue": "Weiter",
  "ob.conv.scene.candidates": "{count} Kandidaten",
  "ob.conv.connect.sceneTitle": "Verbinde deine Konten.",
  "ob.conv.connect.sceneSub":
    "Ich baue deine Kontakte, Firmen und Historie aus dem, was schon in deinem Postfach liegt. Keine Handeingabe, keine CSV-Vorlage.",
  "ob.conv.connect.mailboxTitle": "Dein Postfach",
  "ob.conv.connect.mailboxHint":
    "Wähle eins. Von hier kommen deine Kontakte, Firmen und Historie.",
  "ob.conv.connect.networkTitle": "Dein Netzwerk",
  "ob.conv.connect.networkHint":
    "Optional, aber lohnend. Macht aus Bekannten Accounts und beobachtet sie auf Trigger.",
  "ob.conv.connect.required": "erforderlich",
  "ob.conv.connect.recommended": "empfohlen",
  "ob.conv.connect.gmailBrings": "Mail, Kontakte und Kalender von Google",
  "ob.conv.connect.microsoftBrings":
    "Mail, Kontakte und Kalender über die Graph-API",
  "ob.conv.connect.imapBrings": "Jeder andere Mail-Host, mit App-Passwort",
  "ob.conv.connect.linkedinAuth": "Profillink, nur lesend",
  "ob.conv.connect.scopeGoogle": "OAuth, Lese- und Sendeberechtigung",
  "ob.conv.connect.scopeMicrosoft": "OAuth, Graph API",
  "ob.conv.connect.scopeImap": "Jeder andere Anbieter, App-Passwort",
  "ob.conv.connect.connectCta": "verbinden →",
  "ob.conv.connect.connectedCta": "verbunden",
  "ob.conv.connect.blockedCard":
    "Du hast schon ein Postfach gewählt. Trenne es in den Einstellungen, um zu wechseln.",
  "ob.conv.connect.guaranteesHeading": "Was Verbinden tatsächlich bedeutet",
  "ob.conv.connect.railPromise":
    "Wir lesen nur, und nichts wird ohne deine Freigabe gesendet.",
  "ob.conv.connect.dialogHeadlineAccess": "Zugriff auf {name} nötig",
  "ob.conv.connect.dialogHeadlineImap": "Verbinde deinen Mail-Host",
  "ob.conv.connect.dialogIntro":
    "{brings}. Ich lese es einmal, um deine Kontakte und Historie aufzubauen, und halte es danach synchron.",
  "ob.conv.connect.dialogClose": "Schließen",
  "ob.conv.connect.linkedinName": "LinkedIn",
  "ob.conv.connect.linkedinConnected": "Verbunden",
  "ob.conv.connect.linkedinSkippedNote":
    "Übersprungen: später in den Einstellungen nachholbar",
  "ob.conv.connect.rosterFailedTitle":
    "Postfächer konnten nicht geprüft werden",
  "ob.conv.connect.rosterFailedBody":
    "Beim Laden deines Verbindungsstatus ist etwas schiefgelaufen. Versuche es erneut, bevor du einen Anbieter auswählst.",
  "ob.conv.voice.sceneTitle": "Zeig mir, wie du schreibst.",
  "ob.conv.voice.sceneSub":
    "Jede Mail, jede Antwort und jeder Nachfass, die dieses CRM entwirft, klingt nach dir, nicht nach einer Vorlage, und ohne deine Freigabe wird nichts versendet.",
  "ob.conv.voice.heroKicker": "Warum dieser Schritt zählt",
  "ob.conv.voice.heroBody":
    "Ton, Rhythmus und Formulierungen lernt es aus deinen eigenen Texten und trainiert ausschließlich damit, nie mit den Texten anderer.",
  "ob.conv.voice.dropTitle": "Leg deine Texte hier ab",
  "ob.conv.voice.dropSub":
    "Gesendete Mails eignen sich am besten, weil sie zeigen, wie du schreibst, wenn du etwas willst.",
  "ob.conv.voice.browse": "Dateien wählen",
  "ob.conv.voice.pasteInstead": "Stattdessen Text einfügen",
  "ob.conv.voice.sourcesTitle": "Quellen",
  "ob.conv.voice.meterLabel": "Fortschritt zum Minimum von {min} Wörtern",
  "ob.conv.voice.meterProgress": "{words} von {min} Wörtern",
  "ob.conv.voice.meterReady":
    "{words} Wörter — genug für den Aufbau. Mehr schärft es weiter.",
  "ob.conv.voice.footReady":
    "Das Training dauert etwa eine Minute. Du siehst ein Beispiel, bevor etwas gespeichert wird.",
  "ob.conv.voice.footFloor":
    "Mindestens {min} Wörter. Darunter kopiert das Modell nur Formulierungen.",
  "ob.conv.voice.buildingTitle": "Ich lerne deine Stimme",
  "ob.conv.voice.buildingMeta": "{words} Wörter, {sources} Quellen",
  "ob.conv.voice.resultSub":
    "Lies zuerst das Beispiel. Passt es, bestätige. Passt es nicht, gib mir mehr Quellen und ich baue neu.",
  "ob.conv.voice.resultSubNoSample":
    "Dein Korpus ist noch zu klein, um ein Beispiel zurückzuhalten. Hier ist, was der Aufbau über deinen Schreibstil gelernt hat — für ein Beispiel brauche ich mehr Quellen.",
  "ob.conv.voice.resultContinue": "Das bin ich",
  "ob.conv.voice.sampleEyebrow": "Beispiel, nicht gesendet",
  "ob.conv.voice.sampleAnother": "Anderes Szenario",
  "ob.conv.voice.sampleSubjectLabel": "Betreff",
  "ob.conv.voice.sampleWhyTag": "Warum",
  "ob.conv.voice.dimensionsTitle": "Gemessene Dimensionen",
  "ob.conv.voice.dimensionsCount": "Gemessen: {count}",
  "ob.conv.voice.dimSentenceName": "Satzlänge",
  "ob.conv.voice.dimSentencePoleLow": "Knapp",
  "ob.conv.voice.dimSentencePoleHigh": "Ausführlich",
  "ob.conv.voice.dimSentenceMeasured": "Mittel",
  "ob.conv.voice.dimSentenceEvidence": "Im Schnitt {count} Wörter pro Satz.",
  "ob.conv.scene.evidence": "Beleg",
  "ob.conv.scene.hideEvidence": "Beleg ausblenden",
  "ob.conv.scene.whyThis": "Was ich gelesen habe",
  "ob.conv.scene.foundOn": "Gefunden auf",
  "ob.conv.guide.decision":
    "Ich brauche eine Entscheidung von dir: {question} Sie steht rechts, mit den Belegen zu jeder Option.",
  "ob.conv.guide.reviewBlocked.one":
    "Deine Prüfung ist rechts bereit. {count} Feld blockiert die Übernahme.",
  "ob.conv.guide.reviewBlocked.other":
    "Deine Prüfung ist rechts bereit. {count} Felder blockieren die Übernahme.",
  "ob.conv.guide.reviewAdvisory.one":
    "Deine Prüfung ist rechts bereit. Nichts blockiert dich; {count} Punkt ist einen Blick wert.",
  "ob.conv.guide.reviewAdvisory.other":
    "Deine Prüfung ist rechts bereit. Nichts blockiert dich; {count} Punkte sind einen Blick wert.",
  "ob.conv.guide.reviewClean":
    "Deine Prüfung ist rechts bereit. Sie sieht sauber aus, prüfe was du willst und übernimm, wenn du bereit bist.",
  "ob.conv.guide.attentionHeading": "Diese brauchen deine Eingabe",
  "ob.conv.guide.attentionGroup.blocking": "Nötig, um fortzufahren",
  "ob.conv.guide.attentionGroup.decisions": "Braucht eine Entscheidung",
  "ob.conv.guide.attentionGroup.advisory": "Einen Blick wert",
  "ob.conv.guide.attentionStatus.blocks": "nötig zum Fortfahren",
  "ob.conv.guide.attentionStatus.empty": "noch leer",
  "ob.conv.guide.attentionStatus.decision": "braucht eine Entscheidung",
  "ob.conv.guide.attentionStatus.check": "einen Blick wert",
  "ob.conv.activity.steps": "{count} Schritte",
  "ob.conv.showField": "Zeig mir",
  "ob.conv.review.editDirectly": "Felder direkt bearbeiten",
  "ob.conv.review.backToDossier": "Zurück zum Dossier",
  "ob.conv.review.proposalFallback":
    "Ich konnte die vorbereitete Zuordnung nicht laden. Prüfe direkt, was ich gelesen habe. Jedes Feld behält seine Quelle.",
  "ob.conv.review.confirmFailed":
    "Ich konnte noch nicht speichern: {detail} Korrigiere das und übernimm erneut.",
  "ob.conv.review.confirmVersionSkew":
    "Deine Prüfung wurde gerade mit neueren Informationen aus dem Read aktualisiert. Schau sie dir an und drücke dann erneut Weiter.",
  "ob.conv.review.confirmVersionSkewStuck":
    "Ich habe noch einmal geprüft, aber es hat sich noch nichts geändert. Weiter würde jetzt genauso fehlschlagen. Schau noch einmal nach oder prüfe es gleich erneut.",
  "ob.conv.review.confirmNotReady":
    "Dieser Read hat noch keinen Entwurf zum Bestätigen, deshalb ist Weiter vorerst gesperrt. Prüfe erneut, sobald er fertig ist, oder starte einen neuen Read.",
  "ob.conv.review.confirmCheckFailed":
    "Dieser Read wurde bereits bestätigt, aber ich konnte das damit angelegte Unternehmen nicht laden. Prüfe es gleich noch einmal.",
  "ob.conv.artifact.empty":
    "Noch nichts gelesen. Nenn mir eine Website und dieses Panel füllt sich mit belegten Funden.",
  "ob.conv.results.continue": "Weiter",
  "ob.conv.results.artifactTitle": "Einrichtung im Überblick",
  "ob.conv.results.artifactBody":
    "Womit dein CRM startet. Hier steht nichts, das nicht wirklich passiert ist.",
  "ob.conv.results.company":
    "Firmenprofil für {name} bestätigt. Alles Gespeicherte trägt seine Quelle.",
  "ob.conv.results.companyUnsaved":
    "Deine Firmendaten sind noch nicht gespeichert. Du kannst sie später in den Einstellungen vervollständigen.",
  "ob.conv.results.voiceBuilt":
    "Dein Stimmprofil ist gebaut. Entwürfe klingen nach dir.",
  "ob.conv.results.voiceSkipped":
    "Noch kein Stimmprofil. Entwürfe nutzen eine neutrale Startstimme; deins kannst du später in den Einstellungen bauen.",
  "ob.conv.recap.back": "Willkommen zurück. Hier stehen wir.",
  "ob.conv.recap.company": "Dein Firmenprofil für {name} ist bestätigt.",
  "ob.conv.recap.companyUnsaved":
    "Deine Firmendaten sind noch nicht gespeichert. Du kannst sie in den Einstellungen vervollständigen.",
  "ob.conv.recap.voiceBuilt":
    "Dein Stimmprofil ist gebaut. Entwürfe können nach dir klingen.",
  "ob.conv.recap.voiceSkipped":
    "Du hast das Stimmprofil übersprungen. Entwürfe nutzen eine neutrale Startstimme.",
  "ob.conv.recap.corpus":
    "Dein Korpus enthält bereits {words} deiner eigenen Wörter.",
  "ob.conv.recap.readTerminal":
    "Willkommen zurück. Ich habe {host} bereits fertig gelesen: {count} belegte Funde. Deine Durchsicht ist unten bereit.",
  "ob.conv.recap.readReading":
    "Willkommen zurück. Ich lese {host} noch. Seiten bisher: {pages}.",
  "ob.conv.recap.readFailed":
    "Willkommen zurück. Mein früherer Lesevorgang von {host} wurde nicht fertig. Nenn mir wieder eine Website oder erzähl es mir direkt.",
  "ob.conv.recap.readDeferred":
    "Willkommen zurück. Mein Lesevorgang von {host} pausiert gerade. Nenn mir wieder eine Website oder erzähl es mir direkt.",
  "ob.conv.connect.pick":
    "Wähle einen Anbieter, um genau zu sehen, was das Verbinden tut. Oder überspringe es und verbinde später in den Einstellungen.",
  "ob.conv.linkedin.cardBody":
    "Macht aus deinem Netzwerk Firmen und Kontakte, und meldet, wenn jemand aus deinem Netzwerk den Job wechselt.",
  "ob.conv.linkedin.scope1Lead": "Deine Kontaktliste \u2014",
  "ob.conv.linkedin.scope1Rest":
    "Name, Position, Unternehmen und das Datum der Vernetzung.",
  "ob.conv.linkedin.scope2Lead": "Sonst nichts.",
  "ob.conv.linkedin.scope2Rest":
    "Keine Nachrichten, keine Beitr\u00e4ge, keine Profilbesuche, keine Aktivit\u00e4ten.",
  "ob.conv.linkedin.scope3Lead": "Dein Netzwerk bleibt deins.",
  "ob.conv.linkedin.scope3Rest":
    "Es wird dir zugeordnet, nie dem Unternehmen, und beim Trennen wird es entfernt.",
  "ob.conv.linkedin.scope4Lead": "Niemand wird kontaktiert.",
  "ob.conv.linkedin.scope4Rest":
    "Das Verbinden verschickt keine Einladungen und keine Nachrichten \u2014 nie.",
  "ob.conv.linkedin.neverContacts":
    "Deine Kontakte werden NICHT zu Kontakten im CRM \u2014 sie existieren nur f\u00fcr eine Frage: Kennt hier jemand bereits jemanden bei diesem Unternehmen?",
  "ob.conv.linkedin.profileLabel": "Deine LinkedIn-Profil-URL",
  "ob.conv.linkedin.profilePlaceholder": "https://www.linkedin.com/in/…",
  "ob.conv.linkedin.profileWhy":
    "Damit das Netzwerk dir namentlich zugeordnet wird \u2014 das CRM sagt \u201eAnna kennt sie\u201c, nie \u201edas Unternehmen kennt sie\u201c.",
  "ob.conv.linkedin.authorize": "Mit LinkedIn autorisieren",
  "ob.conv.linkedin.appPending":
    "Hinweis: Unsere LinkedIn-App wartet noch auf Freigabe, es werden also noch keine Kontakte synchronisiert \u2014 nur deine Zustimmung und dein Profil werden erfasst. Lade dazu in den Einstellungen deine Connections.csv hoch, das funktioniert heute schon.",
  "ob.conv.linkedin.skip": "LinkedIn vorerst \u00fcberspringen",
  "ob.conv.linkedin.connected":
    "LinkedIn autorisiert. Deine Kontakte werden synchronisiert, sobald die App freigegeben ist.",
  "ob.conv.linkedin.skipped":
    "LinkedIn \u00fcbersprungen. Sie k\u00f6nnen es jederzeit in den Einstellungen verbinden.",
  "ob.conv.connect.skip": "Verbinden vorerst überspringen",
  "ob.conv.connect.artifactTitle": "Postfach-Verbindung",
  "ob.conv.connect.artifactEmpty":
    "Wähle im Gespräch einen Anbieter, dann öffnet sich hier sein Verbindungs-Panel.",
  "ob.conv.next.decisionOne": "1 Entscheidung offen",
  "ob.conv.next.build": "Bereit, deine Stimme zu bauen",

  // Die Setup-Leiste: fünf Stationen, je ein Wort. Lang genug, den Schritt zu
  // benennen, kurz genug, dass fünf davon bei 10px in eine Spalte passen.
  "ob.rail.read": "Lesen",
  "ob.rail.confirm": "Bestätigen",
  "ob.rail.voice": "Stimme",
  "ob.rail.ready": "Fertig",
  "ob.rail.connect": "Verbinden",

  // --- das Tor: der erste Screen nach der Anmeldung ----------------------
  // Eine Frage und sonst nichts. Niemand soll das ganze Werkzeug auf dem
  // ersten Screen treffen, also nennt das Tor, was es tut, was es den Leser
  // kostet (zwei Minuten) und wer entscheidet (er selbst) — und fragt dann
  // einmal.
  "ob.gate.title": "Hallo {name}, ich bin die Margince-KI.",
  "ob.gate.titleAnonymous": "Ich bin die Margince-KI.",
  "ob.gate.sub":
    "Nenn mir deine Website und ich lese sie: was du verkaufst, wer bei dir kauft, die Menschen dahinter. Du prüfst alles, bevor es gespeichert wird, und ohne dein Ja geht nichts nach draußen. Etwa zwei Minuten.",
  "ob.gate.field": "Deine Website-Adresse",
  "ob.gate.placeholder": "deinefirma.de",
  "ob.gate.submit": "Meine Website lesen",
  "ob.gate.altPrompt": "Keine Website zur Hand?",
  "ob.gate.altAction": "Die Angaben selbst eintragen",
  "ob.gate.invalidUrl":
    "Das sieht nicht wie eine Web-Adresse aus. Versuch es als deinefirma.de.",
  // Ein String für zwei Fehler, die für den Leser gleich aussehen: die
  // Anfrage kam nie an, oder das Lesen begann und wurde nicht fertig.
  // {detail} ist die Erklärung des Servers und kann leer sein — der Satz muss
  // also auch ohne sie tragen.
  "ob.gate.startFailed":
    "Ich konnte diese Website nicht lesen. {detail} Versuch eine andere Adresse, oder gib die Daten selbst ein.",
  // Ein aufgeschobenes Lesen ist vertagt, nicht kaputt: der Server kommt darauf
  // zurück. Der Satz sagt also, was stimmt, und nennt beide Türen, ohne dass
  // der Leser irgendetwas reparieren soll.
  "ob.gate.readPaused":
    "Dieses Lesen ist gerade pausiert. {detail} Es läuft von selbst weiter — oder nenn mir eine andere Adresse, oder gib die Daten selbst ein.",

  // --- das Lese-Theater --------------------------------------------------
  // Sichtbar gemachtes Volumen. Die Schnittstelle liefert keinen Nenner für
  // die Seitenzahl, also ist jede Zahl hier ein offener Zähler — nie "14 von
  // 18", nie ein Balken mit bekanntem Ende, denn die Gesamtzahl zu erfinden
  // hieße, Daten zu erfinden.
  "ob.scan.title": "Ich lese {host}",
  "ob.scan.sub":
    "Ich gehe die Website jetzt durch. Jeder Fakt behält die Seite, von der er kommt, damit du alles nachprüfen kannst, was ich behaupte.",
  "ob.scan.doneTitle": "{host} gelesen",
  "ob.scan.doneSub":
    "{facts} Fakten und {fields} Profilfelder, jeweils mit der Seite, von der sie kommen. Ich öffne deine Durchsicht.",
  "ob.scan.phaseCrawling": "Ich hole Seiten",
  "ob.scan.phaseExtracting": "Ich ermittle, was du verkaufst",
  "ob.scan.phaseQueued": "In der Warteschlange, startet gleich",
  "ob.scan.phaseDeferred": "Vorerst pausiert",
  "ob.scan.pagesRead": "{pages} Seiten gelesen",
  "ob.scan.pagesSkipped": "{count} übersprungen",
  "ob.scan.factsSoFar": "{count} Fakten bisher",
  "ob.scan.stillReading": "lese noch",
  "ob.scan.pageStripLabel": "Bisher gelesene Seiten",
  "ob.scan.logLabel": "Die Seiten, die ich gerade durchgehe, neueste zuerst",
  "ob.scan.pageFetched": "{url} — gelesen",
  "ob.scan.pageSkipped": "{url} — übersprungen: {reason}",
  "ob.scan.pageFailed": "{url} — nicht lesbar: {reason}",
  "ob.scan.pageNoReason": "kein Grund erfasst",
  "ob.scan.pageStatusFetched": "gelesen",
  "ob.scan.pageStatusSkipped": "übersprungen: {reason}",
  "ob.scan.pageStatusFailed": "nicht lesbar: {reason}",
  "ob.scan.transparency": "Transparenz",
  "ob.scan.costLine": "{calls} Aufrufe · {tokens} Tokens · {cost}",
  "ob.scan.costPending": "noch keine Modellaufrufe berechnet",
  "ob.scan.costUnpriced": " · nicht bepreiste Nutzung vorhanden",

  // --- das Live-Panel: der belegte Datensatz baut sich selbst ------------
  "ob.live.headReading": "Ich lese {host}",
  "ob.live.headDone": "{host} gelesen",
  "ob.live.nothingSaved":
    "Noch ist nichts gespeichert. Wenn ich fertig bin, zeige ich dir alles.",
  "ob.live.summaryHeading": "Das habe ich verstanden",
  "ob.live.summaryYouAre": "Du bist",
  "ob.live.summaryYouSell": "Du verkaufst",
  "ob.live.summaryYouSellTo": "Du verkaufst an",
  "ob.live.summaryVolume":
    "{facts} Fakten aus {pages} Seiten, schon eingetragen. Öffne einen Abschnitt, um ihn zu prüfen.",
  "ob.live.stepWebsite": "Aus dem Lesen deiner Website",
  "ob.live.stepVoice": "Deine Schreibstimme",
  "ob.live.stepConnect": "Postfach und Kalender",
  "ob.live.stateDone": "fertig",
  "ob.live.stateNow": "läuft",
  "ob.live.stateWaiting": "wartet",
  "ob.live.review": "Prüfen",
  "ob.live.hide": "Ausblenden",
  "ob.live.countFields": "{count} Felder",
  "ob.live.countFacts": "{count} Fakten",
  "ob.live.countPeople": "{count} Lead-Vorschläge",
  "ob.live.countPages": "{read} gelesen · {skipped} übersprungen",
  "ob.live.cardIdentity": "Firmenidentität",
  "ob.live.cardPositioning": "Positionierung und Vertriebsansatz",
  "ob.live.cardPeople": "Gefundene Personen",
  "ob.live.cardCoverage": "Was ich gelesen und was ich übersprungen habe",
  "ob.live.cardVoice": "Stimmprofil",
  "ob.live.cardConnect": "Verbunden",
  "ob.live.voiceNotBuilt": "noch nicht gebaut",
  "ob.live.connectNone": "nichts verbunden",
  "ob.live.noValue": "—",
  "ob.live.peopleEmpty":
    "Noch niemand. Ich schlage eine Person nur vor, wenn die Seite Name und Rolle nennt.",
  "ob.live.coverageWarning": "Hinweis",
  "ob.live.coverageStopped": "Vorzeitig beendet",
  "ob.live.stoppedPageCap":
    "Ich habe das Seitenlimit für einen Lesevorgang erreicht. Es gibt also mehr auf deiner Website, das ich nicht geöffnet habe.",
  "ob.live.stoppedByteCap":
    "Ich habe das Größenlimit für einen Lesevorgang erreicht. Es gibt also mehr auf deiner Website, das ich nicht geöffnet habe.",
  "ob.live.stoppedBudget":
    "Ich habe das Budget für einen Lesevorgang erreicht. Es gibt also mehr auf deiner Website, das ich nicht geöffnet habe.",
  "ob.live.stoppedDeadline":
    "Mir ist die Zeit für einen Lesevorgang ausgegangen. Es gibt also mehr auf deiner Website, das ich nicht geöffnet habe.",
  "ob.live.coverageSkipped": "Übersprungen",
  "ob.live.coverageFailed": "Nicht lesbar",
  "ob.live.coverageClean":
    "Jede Seite, die ich versucht habe, kam zurück. Nichts wurde übersprungen, nichts ist fehlgeschlagen.",

  // --- Fakten: Vorschaukarte und die ganze Tabelle -----------------------
  "ob.facts.title": "Fakten",
  "ob.facts.catCompany": "Firma",
  "ob.facts.catOffering": "Angebot",
  "ob.facts.catMarket": "Markt",
  "ob.facts.catSignal": "Signal",
  "ob.facts.catAll": "Alle",
  "ob.facts.mixLabel": "Fakten nach Kategorie",
  "ob.facts.selected": "{selected} von {total} werden gespeichert",
  "ob.facts.selectAll": "Alle auswählen",
  "ob.facts.clearAll": "Auswahl aufheben",
  "ob.facts.previewNote":
    "Gezeigt werden die {count} Fakten mit der höchsten Konfidenz.",
  "ob.facts.openTable": "Ganze Tabelle öffnen",
  "ob.facts.tableTitle": "Alle Fakten, die ich gelesen habe",
  "ob.facts.search": "Fakten durchsuchen",
  "ob.facts.hits": "{hits} von {total}",
  "ob.facts.colSave": "Speichern",
  "ob.facts.colCategory": "Kategorie",
  "ob.facts.colFact": "Fakt",
  "ob.facts.colSource": "Quelle",
  "ob.facts.colConfidence": "Konfidenz",
  "ob.facts.rowSave": "Diesen Fakt speichern: {fact}",
  "ob.facts.noMatch": "Zu dieser Suche passt nichts.",
  "ob.facts.empty":
    "Ich habe die Website gelesen, aber keine einzelnen Fakten daraus gezogen. Was ich gelernt habe, steht in den Abschnitten darüber — jeweils mit Quelle.",
  "ob.facts.close": "Fertig",
  "ob.facts.closeTable": "Tabelle schließen",
  "ob.facts.capReached":
    "Du kannst bis zu {max} Fakten speichern. Nimm einen heraus, um Platz für einen anderen zu machen.",

  // --- der Gegenwert: was zwei Minuten wirklich gebracht haben -----------
  // Zahlen, kein Applaus. Jede Zelle ist eine echte Zahl von der
  // Schnittstelle, und eine Zelle ohne Zahl sagt das, statt eine Null zu
  // zeigen, die wie ein Ergebnis aussieht.
  "ob.payoff.lead": "Vor wenigen Minuten war das eine leere Installation.",
  "ob.payoff.leadResumed": "Das hier hat als leere Installation angefangen.",
  "ob.payoff.factsRead": "Fakten gelesen",
  "ob.payoff.factsConfirmed": "Fakten bestätigt",
  "ob.payoff.peopleFound": "Personen gefunden",
  "ob.payoff.profileFields": "Profilfelder",
  "ob.payoff.voiceWords": "Wörter deiner Stimme",
  "ob.payoff.pagesRead": "Seiten gelesen",
  "ob.payoff.voiceNotTrained": "Stimme noch nicht trainiert",
  "ob.payoff.body":
    "Alles darin kannst du korrigieren, und jeder Wert zeigt weiter auf die Seite, von der er kommt.",
  "ob.payoff.defaults":
    "Zwei Voreinstellungen, beide änderbar unter Einstellungen → Autonomie: Ich bereite vor und warte auf deine Bestätigung, und ich überschreibe nie ein Feld, das du selbst getippt hast.",
  "ob.payoff.seats":
    "Es fehlen nur noch deine Kollegen. Sitze sind kostenpflichtig, deshalb legst du sie unter Einstellungen → Nutzer selbst an.",
  "ob.payoff.understood": "Verstanden",

  // --- die Übergabe in die App ------------------------------------------
  "ob.enter.cta": "Margince öffnen",
  "ob.enter.assembling": "Dein Workspace wird zusammengestellt",

  // --- das Zurücklesen des Postfachs -------------------------------------
  // Ein anderer Vorgang als das Verbinden, und der Text muss die beiden
  // getrennt halten: Verbinden erteilt Zugriff, das Zurücklesen verbraucht
  // Budget, um den Verlauf zu lesen. Es liest nur und schreibt nichts,
  // solange der Leser nicht zustimmt.
  "ob.backread.heading": "Wie weit soll ich zurücklesen?",
  "ob.backread.window3m": "3 Monate — aktueller Kontext",
  "ob.backread.window6m": "6 Monate — empfohlen",
  "ob.backread.window12m": "12 Monate — ganzer Vertriebszyklus",
  "ob.backread.estimate": "Etwa {messages} Nachrichten in diesem Zeitraum.",
  "ob.backread.estimateHeuristic":
    "Aus dem Postfach geschätzt, noch nicht gezählt.",
  "ob.backread.estimateCost": "Ungefähr {cost} an Modellaufrufen.",
  "ob.backread.estimateFailed":
    "Ich konnte diesen Zeitraum nicht schätzen: {detail} Du kannst trotzdem starten oder einen anderen wählen.",
  "ob.backread.note":
    "Beim Zurücklesen wird nur gelesen. Ich importiere Personen, Firmen und Aktivitäten, und ich zeige dir, was ich gefunden habe, bevor etwas geschrieben wird.",
  "ob.backread.start": "Verbinden und lesen",
  "ob.backread.startFailed":
    "Ich konnte das Zurücklesen nicht starten: {detail} Versuch es erneut, oder mach weiter und starte es später in den Einstellungen.",
  "ob.backread.running": "Ich lese dein Postfach",
  "ob.backread.runningNote":
    "Du kannst das laufen lassen und weiterarbeiten. Ich mache dort weiter, wo ich stehen geblieben bin.",
  "ob.backread.queued": "In der Warteschlange. Es startet gleich.",
  "ob.backread.progress": "{scanned} von etwa {total} Nachrichten",
  "ob.backread.progressNoTotal": "{scanned} Nachrichten bisher",
  "ob.backread.tallyMessages": "Nachrichten gelesen",
  "ob.backread.tallyCaptured": "behalten",
  "ob.backread.tallySkipped": "ignoriert",
  "ob.backread.tallyPeople": "Personen gefunden",
  "ob.backread.tallyCompanies": "Firmen gefunden",
  "ob.backread.doneHeading": "Das steckt darin.",
  "ob.backread.doneNote":
    "Noch ist nichts geschrieben. Alles, was ich gefunden habe, wartet im Eingang auf deine Prüfung.",
  "ob.backread.failed":
    "Das Zurücklesen wurde gestoppt: {detail} Deine Verbindung ist in Ordnung — du kannst es in den Einstellungen erneut starten.",
  "ob.backread.cancelled":
    "Ich habe das Lesen gestoppt. Es wurde nichts geschrieben.",
  "ob.backread.cancelledPartial":
    "Ich habe das Lesen gestoppt. Was schon erfasst wurde, bleibt erhalten — es wartet im Eingang auf dich.",
  "ob.backread.cancelFailed":
    "Ich konnte das Lesen nicht stoppen: {detail} Versuch es erneut — es läuft in der Zwischenzeit weiter.",
  "ob.backread.detailUnavailable": "Etwas ist unerwartet schiefgelaufen.",
  "ob.backread.cancel": "Lesen stoppen",
  "ob.backread.explore": "In der Zeit Margince erkunden",
  "ob.backread.skip": "Verlauf jetzt nicht lesen",

  "auth.title": "Margince",
  "auth.checking": "Deine Sitzung wird geprüft…",
  "auth.pageTitle": "Anmelden · Margince",
  "auth.loginTitle": "Bei Margince anmelden",
  // "eine Admin-Person", nicht "deine Administration": eine Administration ist
  // im Deutschen eine Stelle oder eine Tätigkeit, keine Person — der Rest des
  // Katalogs sagt durchgehend "Admin-Person". Und der zweite Satz nennt das Verb
  // statt des Nominalstils ("Eine Selbstregistrierung gibt es nicht").
  "auth.loginSub":
    "Konten legt eine Admin-Person an. Selbst registrieren kannst du dich nicht.",
  "auth.coreDisclosure": "Margince · KI-System",
  "auth.coreBoundary":
    "Ich kann deinen Kontext erst nutzen, nachdem Margince bestätigt hat, dass du es bist.",
  // "Postfach", nicht "Mailbox": eine Mailbox ist im Deutschen der
  // Anrufbeantworter. Auf einer Zeile, die den Umfang des Zugriffs offenlegt,
  // benennt das eine andere Datenquelle als die englische Fassung.
  "auth.coreScope":
    "Dieser Kontext ist dein Postfach, dein Kalender und was ich öffentlich im Web lesen kann. Nichts anderes und nichts ohne deine Freigabe.",
  "auth.corePermission": "Ich nutze deine Berechtigungen.",
  "auth.coreCites": "Ich belege, was ich finde.",
  "auth.coreWaits": "Vor externen Aktionen warte ich auf deine Freigabe.",
  "auth.coreMarks": "Ich markiere jeden Wert, den ich geschrieben habe.",
  "auth.coreConfigured": "Konfiguriert",
  "auth.coreUnconfigured": "KI nicht konfiguriert",
  // "auch ohne", nicht "weiterhin": "weiterhin" ist zeitlich und liest sich neben
  // dem Hinweis "KI nicht konfiguriert" wie "noch, aber nicht mehr lange".
  "auth.coreStillWorks": "Das CRM funktioniert auch ohne.",
  "auth.coreDevelopment": "Entwicklungs-KI",
  "auth.coreModeCloud": "Cloud-Routing",
  "auth.coreModeLocal": "lokales Routing",
  "auth.coreModeHybrid": "hybrides Routing",
  "auth.coreModeNone": "kein Modell-Routing",
  // Die Nachbarwerte sind alle Betriebsarten; "Modus" ist dafür das deutsche
  // Wort, "Pfad" die Übersetzung von "path".
  "auth.coreModeDevelopment": "Offline-Entwicklungsmodus",
  "auth.coreProviderAnthropic": "Anthropic",
  "auth.coreProviderGemini": "Gemini",
  "auth.coreProviderOllama": "Ollama",
  "auth.coreProviderOpenAI": "OpenAI",
  "auth.coreProviderCompatible": "kompatibler Anbieter",
  "auth.coreProviderVllm": "vLLM",
  "auth.email": "E-Mail",
  // Der lokale Teil einer Adresse ist nie ein Pronomen — "du@" ist "you@"
  // Zeichen für Zeichen. "beispiel.de" ist im Deutschen, was "example.com" im
  // Englischen ist, und genau das pinnt die Login-Spec §7.2.
  "auth.emailPlaceholder": "name@beispiel.de",
  "auth.password": "Passwort",
  "auth.passwordPlaceholder": "Passwort",
  "auth.passwordHint": "mindestens 12 Zeichen",
  "auth.showPassword": "Passwort anzeigen",
  "auth.hidePassword": "Passwort ausblenden",
  "auth.capsLock": "Feststelltaste ist an",
  "auth.continueWith": "Weiter mit {brand}",
  "auth.orDivider": "oder",
  "auth.legalProtected": "Der Zugang zu dieser Organisation ist beschränkt.",
  "auth.legalTerms": "Nutzungsbedingungen",
  "auth.legalPrivacy": "Datenschutz",
  "auth.signIn": "Anmelden",
  "auth.signingIn": "Anmeldung läuft…",
  "auth.failed": "Das hat nicht geklappt",
  "auth.errCredentials":
    "Die Anmeldung war nicht möglich. Prüfe E-Mail-Adresse und Passwort und versuche es erneut.",
  "auth.errRateLimited":
    "Zu viele Anmeldeversuche. Warte einen Moment und versuche es erneut.",
  "auth.errUnreachable":
    "Margince ist nicht erreichbar. Prüfe deine Verbindung und versuche es erneut.",
  "auth.retry": "Erneut versuchen",
  "auth.noticeSignedOut": "Du wurdest abgemeldet.",
  "auth.noticeSessionExpired":
    "Deine Sitzung ist abgelaufen. Melde dich erneut an, um fortzufahren.",
  "auth.connectionTitle": "Margince ist nicht erreichbar",
  "auth.connectionBody":
    "Prüfe deine Verbindung und versuche es erneut. Besteht das Problem weiter, startet der Server womöglich gerade neu.",
  "auth.unavailableTitle": "Installation nicht bereit",
  // "Betreiber", nicht "Operator": ein Operator ist im Deutschen ein
  // mathematisches Zeichen oder eine Telefonvermittlung. Und eine Einrichtung
  // wird korrigiert, nicht repariert — repariert werden Geräte.
  "auth.unavailableBody":
    "Diese Margince-Installation kann dich noch nicht anmelden. Ein Betreiber muss die Einrichtung abschließen oder korrigieren.",
  "auth.forgotLink": "Passwort vergessen?",
  "auth.forgotTitle": "Passwort zurücksetzen",
  // "gibt", nicht "existiert" (Amtsdeutsch), und das Feld will eine Adresse, keine
  // E-Mail — die E-Mail ist die Nachricht. Was unterwegs ist, ist ebenfalls die
  // Nachricht, nicht der Link. Die Existenz des Kontos bleibt offen.
  "auth.forgotSub":
    "Gib deine E-Mail-Adresse ein. Wenn es dazu ein Konto gibt, schicken wir dir einen Link.",
  "auth.sendResetLink": "Link senden",
  "auth.forgotSentTitle": "Prüfe dein Postfach",
  "auth.forgotSentBody":
    "Wenn es zu dieser Adresse ein Konto gibt, ist die E-Mail unterwegs. Der Link läuft in einer Stunde ab.",
  "auth.resetTitle": "Neues Passwort wählen",
  "auth.resetSub": "Dein Link ist gültig. Wähle ein neues Passwort.",
  "auth.newPassword": "Neues Passwort",
  "auth.setNewPassword": "Neues Passwort speichern",
  // "bereits verwendet", nicht "verbraucht": ein Link wird verwendet, nicht
  // verbraucht wie Kraftstoff.
  "auth.resetFailed":
    "Dieser Link ist ungültig, bereits verwendet oder abgelaufen.",
  // "nicht akzeptiert": abgelehnt werden Anträge und Angebote, nicht Passwörter.
  // Ein anderes zu wählen IST der neue Versuch, also entfällt der Nachsatz.
  "auth.resetRejectedPassword":
    "Dieses Passwort wurde nicht akzeptiert. Wähle ein anderes.",
  // "speichern", nicht "setzen": gesetzt wird eine Variable, und "speichern" ist
  // genau das Verb, das auf dem Button darunter steht. Drei Sätze statt eines
  // Komma-Spleißes zwischen Aussage und Aufforderung.
  "auth.resetServerFailed":
    "Wir konnten dein neues Passwort gerade nicht speichern. Dein Link bleibt gültig. Versuche es gleich noch einmal.",
  // Nicht "setze … erneut": das liest sich als "zurücksetzen", und dieser Schritt
  // liegt hinter dem Zurücksetzen.
  "auth.resetRateLimited":
    "Zu viele Versuche. Warte einen Moment und speichere dein Passwort dann erneut.",
  "auth.requestNewLink": "Neuen Link anfordern",
  "auth.askAdminForNewLink":
    "Bitte deine Administratorin oder deinen Administrator um einen neuen Passwort-Link.",
  // "geändert", wie im Satz darunter: aktualisiert werden Daten, die veralten.
  "auth.resetDoneTitle": "Passwort geändert",
  // "beendet", nicht "abgemeldet": abmelden tut sich eine Person, eine Sitzung
  // wird beendet.
  "auth.resetDoneBody":
    "Dein Passwort ist geändert und alle anderen Sitzungen sind beendet. Melde dich mit dem neuen Passwort an.",
  "auth.backToLogin": "Zurück zur Anmeldung",
  "auth.signOut": "Abmelden",

  "client.back": "Zurück zu Margince",
  "client.title": "Margince neben deinem Postfach",
  "client.sub": "die Extension-Oberfläche — ohne Shell, kennt den Datensatz",
  "client.sender": "Absender",
  "client.lookup": "Nachschlagen",
  "client.open360": "360 öffnen",
  "client.unknown": "Noch nicht in deinem Workspace.",
  "client.unknownDetail":
    "Dieser Absender passt zu keinem Kontakt, den du sehen kannst. Von woanders wurde nichts geholt.",
  "client.createLead": "Als Lead erfassen",
  "client.isolation": "spricht nur mit DEINEM Workspace",
  "client.attribution": "Jede Erfassung ist zugeordnet und prüfbar.",

  "book.title": "Termin buchen",
  "book.sub": "echte Verfügbarkeit aus dem verbundenen Kalender",
  "book.min15": "15 Min.",
  "book.min30": "30 Min.",
  "book.min60": "60 Min.",
  "book.attendee": "Teilnehmer-E-Mail",
  "book.welcomeBack": "Erkannt: {name}",
  "book.subject": "Termin über Margince",
  "book.confirmed": "Gebucht. Die Einladung ist unterwegs.",
  "book.failed": "Die Buchung ging nicht durch — es wurde nichts eingetragen.",
  "book.publicSub": "Slot auswählen — ganz ohne Konto",
  "book.name": "Dein Name",
  "book.email": "Deine E-Mail",
  "book.consentWording":
    "Ich bin einverstanden, dass mein Name und meine E-Mail gespeichert werden, um diesen Termin zu vereinbaren und nachzufassen.",

  "prefs.title": "Wähle, was du von uns hörst",
  "prefs.sub":
    "Jeder Zweck steht für sich — hier ist nicht alles oder nichts. Transaktionale Nachrichten lassen sich hier nicht abschalten, weil du sie brauchst; alles andere bestimmst du selbst.",
  "prefs.invalidLink":
    "Dieser Link ist nicht mehr gültig. Präferenz-Links laufen ab oder können widerrufen werden — frag in einer aktuellen E-Mail nach einem neuen.",
  "prefs.rateLimited":
    "Gerade zu viele Versuche von hier aus. Warte eine Minute und lade neu.",
  "prefs.subscribed": "Abonniert",
  "prefs.notSubscribed":
    "Nicht abonniert — du bekommst für diesen Zweck nichts",
  "prefs.alwaysOn": "immer an",
  "prefs.lockedWhy": "Transaktional — von der Abmeldung ausgenommen.",
  "prefs.notSaved": "Noch nicht gespeichert.",
  "prefs.savePending": "Ausstehend: {changes}.",
  "prefs.saveProof":
    "Wir speichern den genauen Wortlaut, den du gesehen hast, und einen Zeitstempel als Nachweis — danach gilt er für jeden künftigen Versand.",
  "prefs.save": "Einstellungen speichern",
  "prefs.discard": "Verwerfen",
  "prefs.partialSave":
    "Beim Speichern ist etwas schiefgelaufen. Einige deiner Entscheidungen wurden möglicherweise schon übernommen — wir haben deinen aktuellen Stand neu geladen, damit du genau siehst, wo du stehst.",
  "prefs.wordingGeneric": "„{label} senden.“",
  "prefs.wording.marketing_email":
    "„Schick mir Produkt-Updates und gelegentliche Marketing-E-Mails.“",
  "prefs.wording.events": "„Schick mir Einladungen zu Events und Webinaren.“",
  "prefs.unsubscribeAll": "Von allem Marketing abmelden",
  "prefs.unsubscribeAllHint":
    "Lieber alle nicht notwendigen Mails auf einmal stoppen? Transaktionale Nachrichten bekommst du weiterhin.",
  "prefs.oneClickDone":
    "Erledigt — du bekommst keine Marketing-E-Mails mehr von uns. Das gilt sofort für jede Kampagne.",
  "prefs.oneClickAlreadyOff": "Nichts zu tun — das war bereits abgeschaltet.",
  "prefs.undo": "Rückgängig — Marketing weiter erhalten",
  "prefs.undoExplicit":
    "Ein erneutes Abonnieren ist eine ausdrückliche Zustimmung — wir schalten es nicht stillschweigend wieder ein. Speichere unten, um deine Zustimmung festzuhalten, oder verwirf.",

  "auto.sub":
    "ein geschlossener Katalog — Typ wählen, Parameter setzen, aktivieren",
  "auto.readOnly":
    "Nur-Lese-Ansicht — du hast keine Berechtigung, Automatisierungen zu ändern.",
  "auto.catalog": "Starter-Bibliothek",
  "auto.catalogSub": "die geschlossene Menge an Automatisierungstypen",
  "auto.instances": "Eingerichtete Automatisierungen",
  "auto.use": "Vorlage verwenden",
  "auto.name": "Name",
  "auto.create": "Anlegen",
  "auto.createdPaused":
    "Pausiert angelegt — es läuft nichts, bis du aktivierst.",
  "auto.enable": "Aktivieren",
  "auto.pause": "Pausieren",
  "auto.delete": "Löschen",
  "auto.statusEnabled": "aktiv",
  "auto.statusPaused": "pausiert",

  "auto.runs.open": "Läufe",
  "auto.runs.title": "Laufverlauf",
  "auto.runs.filterAll": "Alle",
  "auto.runs.filterFired": "Ausgelöst",
  "auto.runs.filterFailed": "Fehlgeschlagen",
  "auto.runs.filterBlocked": "Blockiert",
  "auto.runs.filterSkipped": "Übersprungen",
  "auto.runs.filterQueued": "Zur Freigabe eingereiht",
  "auto.runs.empty": "Diese Automatisierung wurde noch nicht ausgelöst.",
  "auto.runs.emptyFiltered": "Keine Läufe mit diesem Ergebnis.",
  "auto.runs.needsApproval": "Freigabe erforderlich",
  "auto.runs.why": "Warum",
  "auto.runs.target": "Ziel",
  "auto.runs.result": "Ergebnis",
  "auto.runs.reason": "Grund",
  "auto.runs.outcomeFired": "ausgelöst",
  "auto.runs.outcomeFailed": "fehlgeschlagen",
  "auto.runs.outcomeBlocked": "blockiert",
  "auto.runs.outcomeSkipped": "übersprungen",
  "auto.runs.outcomeQueued": "eingereiht",

  "auto.preview.open": "Vorschau",
  "auto.preview.title": "Probelauf – Reichweite",
  "auto.preview.window": "Zeitfenster",
  "auto.preview.window7": "7 T",
  "auto.preview.window30": "30 T",
  "auto.preview.window90": "90 T",
  "auto.preview.matchesNow": "Treffer jetzt: {n}",
  "auto.preview.wouldFire": "Würde auslösen: ~{n} / {days} T",
  "auto.preview.notComputable": "Rückblickende Schätzung nicht möglich",
  "auto.preview.hidden": "{n} ausgeblendet – kein Zugriff",
  "auto.preview.explainer":
    "Ein reiner Probelauf – es werden keine Datensätze geändert und nichts gesendet.",

  "strength.title": "Beziehungsstärke",
  "strength.score": "Score {score}/100",
  "strength.bucket.dormant": "Ruhend",
  "strength.bucket.weak": "Schwach",
  "strength.bucket.warm": "Warm",
  "strength.bucket.strong": "Stark",
  "strength.factor.recency": "Aktualität",
  "strength.factor.frequency": "Häufigkeit",
  "strength.factor.reciprocity": "Reziprozität",
  "strength.factor.direction": "Richtung",
  "strength.lastInteraction": "Letzte Interaktion: {when}",
  "strength.none": "Noch keine Interaktionen",
  "strength.inout": "{in} eingehend · {out} ausgehend (90 Tage)",
  "strength.computedFrom": "Berechnet aus {count} Aktivitäten",

  // Die Beziehungsgraph-Karten (ADR-0078). Die Kollegen-Stufen sind die von
  // PO-F-3b und unterscheiden sich bewusst von denen der arbeitsbereichsweiten
  // Karte: beide messen Verschiedenes und dürfen nicht vergleichbar wirken.
  "network.title": "Wer kennt diese Person bei uns",
  "network.empty":
    "Niemand hier hat bisher erfassten Kontakt zu dieser Person.",
  "network.interactions": "{count} Interaktionen (90 Tage)",
  "network.neverSpoken": "Kein erfasster Kontakt",
  "network.bucket.none": "Kein Kontakt",
  "network.bucket.weak": "Schwach",
  "network.bucket.moderate": "Mittel",
  "network.bucket.strong": "Stark",
  "coverage.title": "Abdeckung",
  "coverage.clear":
    "Nichts markiert — dieser Deal besteht alle Abdeckungsprüfungen.",
  "coverage.daysSinceTouch": "{days} Tage",
  "coverage.risk.single_threaded_theirs": "Nur ein Kontakt",
  "coverage.risk.single_threaded_ours": "Von einer Person getragen",
  "coverage.risk.coverage_gap": "Kein engagierter Fürsprecher",
  "coverage.risk.champion_left": "Fürsprecher hat gekündigt",
  "coverage.risk.stakeholder_left": "Stakeholder hat gekündigt",
  "coverage.risk.going_cold": "Wird kalt",

  "cf.title": "Benutzerdefinierte Felder",
  "cf.formSection": "Benutzerdefinierte Felder",
  "cf.subtitle":
    "Füge einem vorhandenen Objekt ein einfaches typisiertes Feld hinzu — zur Laufzeit, ohne Entwickler, ohne Deploy. Neue Objekte und Beziehungen laufen weiterhin über Code.",
  "cf.object": "Objekt",
  "cf.obj.deal": "Deal",
  "cf.obj.organization": "Firma",
  "cf.obj.person": "Kontakt",
  "cf.obj.lead": "Lead",
  "cf.onObject": "Benutzerdefinierte Felder auf {object}",
  "cf.coreExcluded":
    "Kernfelder werden nicht angezeigt — sie sind hier nicht bearbeitbar",
  "cf.col.field": "Feld",
  "cf.col.type": "Typ",
  "cf.col.addedBy": "Hinzugefügt von",
  "cf.addedByYou": "Du",
  "cf.addedByAdmin": "Admin",
  "cf.empty.deal":
    "Noch keine benutzerdefinierten Felder auf Deal. Füge unten eines hinzu, wenn du etwas erfasst, das wir nicht mitgeliefert haben.",
  "cf.empty.organization":
    "Noch keine benutzerdefinierten Felder auf Firma. Füge unten eines hinzu, wenn du etwas erfasst, das wir nicht mitgeliefert haben.",
  "cf.empty.person":
    "Noch keine benutzerdefinierten Felder auf Kontakt. Die Kernfelder decken den Kontaktdatensatz ab; füge unten eines hinzu, wenn du mehr erfasst.",
  "cf.empty.lead":
    "Noch keine benutzerdefinierten Felder auf Lead. Ein Feld, das du hier hinzufügst, erscheint auch, sobald ein Lead zu einem Kontakt befördert wird.",
  "cf.type.text": "Text",
  "cf.type.number": "Zahl",
  "cf.type.date": "Datum",
  "cf.type.currency": "Währung",
  "cf.type.picklist": "Auswahlliste",
  "cf.type.boolean": "Ja / Nein",
  "cf.builder.addTo": "Feld zu {object} hinzufügen",
  "cf.builder.noCode": "ohne Code",
  "cf.builder.intro":
    "Ein neues Feld ist eine echte Spalte auf der bestehenden Tabelle — es filtert, erscheint in Berichten, Exporten und in der API wie jedes Kernfeld. Es ist kein neues Objekt.",
  "cf.label": "Bezeichnung",
  "cf.apiKey": "API-Schlüssel",
  "cf.apiKeyHint":
    "Automatisch abgeleitet, unveränderlich sobald live. Mit cf_ präfixiert, damit er nie mit einem Kernfeld kollidiert.",
  "cf.typeLabel": "Typ",
  "cf.currencyCode": "Währungscode",
  "cf.currencyHint":
    "Dreibuchstabiger ISO-4217-Code (z. B. EUR, USD). Geld wird auf den Cent genau gespeichert.",
  "cf.options": "Optionen",
  "cf.addOption": "Option hinzufügen",
  "cf.removeOption": "Option entfernen",
  "cf.optionPlaceholder": "Optionsbezeichnung",
  "cf.lastOptionBlocked": "Eine Auswahlliste braucht mindestens eine Option",
  "cf.gate.title": "Ein Feld hinzuzufügen ist bestätigungspflichtig.",
  "cf.gate.body":
    "Bei Bestätigung wird es zu einer Live-Spalte auf jedem {object} — auf der 360, in Suche & Filtern, Listen, Export und der API. Das Hinzufügen wird im Audit-Trail festgehalten.",
  "cf.refuse.title":
    "Das sieht nach einem neuen Objekt oder einer Beziehung aus, nicht nach einem Feld.",
  "cf.refuse.body":
    "Dieser Builder fügt nur einfache Felder zu bestehenden Datensätzen hinzu. Ein neues Objekt, eine Verknüpfung zwischen Objekten oder ein berechneter Roll-up ist eine strukturelle Änderung — sie kommt als geprüfte Änderung an Margince in einer neuen Version, gemacht von Menschen, nicht vom Produkt, das seinen eigenen Code bearbeitet.",
  "cf.refuse.route":
    "Leite es über den Entwicklungsweg — deine eigenen Entwickler, einen Implementierungspartner oder Gradion-Services.",
  "cf.confirm": "Bestätigen & Feld hinzufügen",
  "cf.reset": "Zurücksetzen",
  "cf.writing": "wird geschrieben…",
  "cf.added":
    'Feld "{label}" hinzugefügt — live auf 360, Filtern, Export & API',
  "cf.edit": "Bezeichnung bearbeiten",
  "cf.archive": "Feld archivieren",
  "cf.archived":
    '"{label}" archiviert — aus neuen Datensätzen ausgeblendet, in Audit & Historie behalten (umkehrbar)',
  "cf.renamePrompt": "Neue Bezeichnung",
  "cf.renamed": 'Umbenannt in "{label}"',
  "cf.audit.title": "Letzte Feldänderungen",
  "cf.audit.empty": "Noch keine Änderungen an benutzerdefinierten Feldern.",
  "cf.audit.loading": "Aktuelle Änderungen werden geladen…",
  "cf.audit.error":
    "Letzte Änderungen konnten nicht geladen werden — bitte kurz erneut versuchen.",
  "cf.audit.footer":
    "Jedes Hinzufügen / Bearbeiten / Archivieren wird dauerhaft im Audit-Log festgehalten.",
  "cf.noPermission":
    "Du hast nur Lesezugriff auf benutzerdefinierte Felder — der Builder sowie die Bearbeiten- und Archivieren-Steuerelemente sind deaktiviert.",
  "cf.retired": "Archiviert",
  "cf.propagate.title": "Wo ein neues Feld auftaucht",
  "cf.propagate.360": "In der 360-Ansicht des Datensatzes",
  "cf.propagate.filters": "In Suche & Filtern",
  "cf.propagate.list": "Als Listen-/Berichtsspalte",
  "cf.propagate.export": "Im CSV-Export",
  "cf.propagate.api": "In der öffentlichen REST-/MCP-API",
  "nav.customFields": "Benutzerdefinierte Felder",
  "settings.customFields": "Benutzerdefinierte Felder",
  "settings.customFieldsSub":
    "Füge einem Kernobjekt ein typisiertes Feld hinzu — ohne Code, ohne Deploy.",
  "settings.openCustomFields": "Benutzerdefinierte Felder öffnen",
  "settings.navAria": "Einstellungsbereiche",
  "settings.tab.account": "Konto",
  "settings.tab.company": "Firmenkontext",
  "settings.tab.ai": "KI & Autonomie",
  "settings.tab.data": "Datenmodell",
  "settings.tab.catalog": "Katalog",
  "settings.tab.rates": "Kurse & Kosten",
  "settings.tab.privacy": "Datenschutz & Einwilligung",
  "settings.tab.audit": "Audit-Log",
  "settings.tab.voice": "Voice DNA",
  "settings.tab.integrations": "Integrationen",
  "settings.tab.overlay": "Overlay",
  "settings.group.you": "Deine Einstellungen",
  "settings.group.org": "Organisation",
  "settings.rates.fxTitle": "Währungskurse",
  "settings.rates.fxIntro":
    "Wechselkurse, die Fremdwährungsbeträge in deine Basiswährung umrechnen. Neue Kurse gelten ab heute oder später; vergangene Kurse werden nie geändert.",
  "settings.rates.fxAdd": "Kurs setzen",
  "settings.rates.fxEmpty": "Noch keine Währungskurse.",
  "settings.rates.fxModalTitle": "Währungskurs setzen",
  "settings.rates.rateToBase": "Kurs (zur Basiswährung)",
  "settings.rates.modelTitle": "KI-Modellkosten",
  "settings.rates.modelIntro":
    "Preise je Modell in USD pro 1 Mio. Token zur Schätzung der KI-Kosten. Nur zur Transparenz — Preise ändern das Modell-Routing nie.",
  "settings.rates.modelAdd": "Modellpreis hinzufügen",
  "settings.rates.modelEmpty": "Noch keine Modellpreise.",
  "settings.rates.modelModalTitle": "Modellpreis setzen",
  "settings.rates.setRate": "Speichern",
  "settings.rates.refresh": "Von Quellen aktualisieren",
  "settings.rates.refreshEnqueued":
    "Aktualisierung angefordert — etwaige Vorschläge erscheinen im Posteingang.",
  "settings.rates.colFrom": "Von",
  "settings.rates.colRate": "Kurs (→{base})",
  "settings.rates.colEffective": "Gültig ab",
  "settings.rates.colProvider": "Anbieter",
  "settings.rates.colModel": "Modell",
  "settings.rates.colInput": "Eingabe $/M",
  "settings.rates.colOutput": "Ausgabe $/M",
  "settings.rates.colCacheRead": "Cache-Lesen $/M",
  "settings.rates.colCacheWrite": "Cache-Schreiben $/M",
  "settings.voice.title": "Voice DNA",
  "settings.voice.intro":
    "Deine persönliche Schreibstimme. Sie prägt Entwürfe, die für dich gemacht werden, bleibt privat und lernt nur aus Quellen, die du hinzufügst.",
  "settings.voice.emptyTitle": "Noch keine Voice DNA",
  "settings.voice.emptyBody":
    "Füg unten ein paar Schreibproben hinzu und bau deine Voice DNA — oder mach es beim Onboarding.",
  "settings.voice.status.collecting": "Sammelt",
  "settings.voice.status.ready": "Bereit",
  "settings.voice.status.stale": "Neuaufbau nötig",
  "settings.voice.bandThin": "dünn",
  "settings.voice.bandGood": "gut",
  "settings.voice.bandRich": "dicht",
  "settings.voice.bandSharp": "scharf",
  "settings.voice.version": "Version {n}",
  "settings.voice.derivedLabel": "Deine abgeleitete Stimme",
  "settings.voice.derivedEmpty":
    "Noch nicht gebaut — füg Proben hinzu und bau, um deine abgeleitete Stimme zu sehen.",
  "settings.voice.personalityLabel": "Deine Vorgaben",
  "settings.voice.personalityPlaceholder":
    "Notizen dazu, wie du klingen willst — genau so behalten, wie du sie schreibst; das Modell überschreibt das nie.",
  "settings.voice.savePreferences": "Vorgaben speichern",
  "settings.voice.corpusLabel": "Schreibproben",
  "settings.voice.meter": "{count} von {target} W\u00f6rtern",
  "settings.voice.register.email": "E-Mail",
  "settings.voice.register.social": "Social",
  "settings.voice.register.long_form": "Langform",
  "settings.voice.register.spoken": "gesprochen",
  "settings.voice.register.general": "allgemein",
  "settings.voice.bandDrop":
    "Das Entfernen stuft deine Stimme von {from} auf {to} zur\u00fcck. Zum Best\u00e4tigen Entfernen erneut ausl\u00f6sen.",
  "voice.insights.avoidLabel": "Was deine Stimme vermeidet",
  "voice.insights.voiceScore": "Stimm-Treffer {pct}%",
  "voice.insights.next.addTranscript":
    "F\u00fcge ein Gespr\u00e4chs- oder Meeting-Transkript hinzu \u2014 gesprochene Worte sind dein st\u00e4rkstes Signal.",
  "voice.insights.next.addEmail":
    "F\u00fcge gesendete E-Mails hinzu \u2014 sie sind die wichtigste Quelle daf\u00fcr, wie du beruflich schreibst.",
  "voice.insights.next.addWords":
    "F\u00fcge etwa {count} weitere W\u00f6rter hinzu, um das scharfe Band zu erreichen.",
  "voice.insights.next.atTarget":
    "Dein Korpus ist am Ziel; halte ihn mit gelegentlichen neuen Texten frisch.",
  "voice.status.active": "aktiv",
  "voice.status.candidate": "wartet auf Pr\u00fcfung",
  "voice.status.superseded": "abgel\u00f6st",
  "voice.status.rejected": "abgelehnt",
  "voice.classification.routine": "routinem\u00e4\u00dfige \u00c4nderung",
  "voice.classification.material": "wesentliche \u00c4nderung",
  "voice.outcome.autoActivated": "automatisch aktiviert",
  "voice.outcome.reviewRequired": "Pr\u00fcfung erforderlich",
  "voice.outcome.manuallyActivated": "von dir aktiviert",
  "voice.outcome.rejected": "abgelehnt",
  "voice.outcome.rollback": "wiederhergestellt",
  "voice.history.versionRow": "v{n} \u00b7",
  "voice.history.loadMore": "\u00c4ltere Eintr\u00e4ge anzeigen",
  "voice.insights.provenance": "Aus deinem Korpus gebaut \u00b7 v{n}",
  "voice.insights.statWords": "W\u00f6rter: {count}",
  "voice.insights.statSources": "Quellen: {count}",
  "voice.insights.statSentence": "\u2248{count} W\u00f6rter pro Satz",
  "voice.insights.thinkingLabel": "Wie du denkst",
  "voice.insights.movesLabel":
    "Deine Signature Moves \u2014 in deinen eigenen Worten",
  "voice.insights.samplesLabel": "Beispielentw\u00fcrfe in deiner Stimme",
  "voice.insights.draftOnly": "nur Entwurf \u2014 wird nie gesendet",
  "voice.insights.disclosure":
    "KI-gest\u00fctzte Entw\u00fcrfe; jeder Versand bleibt eine menschliche Entscheidung.",
  "voice.insights.nextBestLabel": "So wird sie besser:",
  "voice.candidate.title":
    "Eine neue Stimmen-Version (v{n}) wartet auf deine Pr\u00fcfung.",
  "voice.candidate.apply": "Diese Version verwenden",
  "voice.candidate.reject": "Meine aktuelle Stimme behalten",
  "voice.history.label": "Versionen und Lernen",
  "voice.history.empty":
    "Noch keine Versionen \u2014 baue zuerst deine Stimme.",
  "voice.history.deltasLabel": "Was sich ge\u00e4ndert hat",
  "voice.history.deltaRow": "v{from} \u2192 v{to}",
  "voice.history.learning":
    "Lernt kontinuierlich \u2014 erstellte Entw\u00fcrfe: {drafted} \u00b7 vor dem Senden bearbeitet: {edited} \u00b7 abgelehnt: {rejected}.",
  "voice.history.rollback": "Version {n} wiederherstellen",
  "settings.voice.corpusEmpty": "Noch keine Proben.",
  "settings.voice.excluded": "ausgeschlossen",
  "settings.voice.removeSource": "Probe entfernen",
  "settings.voice.pastedLabel": "Eingefügter Text",
  "settings.voice.addPlaceholder":
    "Füg eine E-Mail, einen Post oder etwas Geschriebenes ein…",
  "settings.voice.addSource": "Probe hinzufügen",
  "settings.voice.addFirstLabel": "Deine erste Schreibprobe",
  "settings.voice.addFirstCta": "Hinzufügen und Voice DNA starten",
  "settings.voice.building": "Baue…",
  "settings.voice.rebuild": "Voice DNA neu bauen",
  "settings.voice.buildNeedsWords":
    "Noch etwa {n} Wörter, dann kann ich deine erste Voice DNA bauen. Darunter liegt zu wenig von deinem Schreiben vor, um ehrlich etwas daraus zu lernen.",
  "settings.voice.buildProvisional":
    "Genug, um daraus zu bauen. Etwa {n} Wörter mehr geben dem Build ein vollständigeres Bild davon, wie du schreibst.",
  "settings.voice.buildStatus.succeeded": "Voice DNA aktualisiert.",
  "settings.voice.buildStatus.failed":
    "Der Aufbau ist nicht fertig geworden — versuch es noch mal.",
  "settings.voice.buildStatus.deferred":
    "In der Warteschlange — sie wird gleich fertig und aktualisiert sich automatisch.",
  "settings.voice.buildStatus.pending":
    "Wird noch gebaut — das kann einen Moment dauern; es aktualisiert sich hier, sobald es fertig ist.",
  "settings.tab.users": "Nutzer & Rollen",
  "users.title": "Nutzer & Rollen",
  "users.sub":
    "Mitglieder einladen, Rollen setzen und Zugriff deaktivieren. Nur für Admins.",
  "users.empty": "Noch keine Mitglieder.",
  "users.adminOnly": "Mitglieder verwalten können nur Admins.",
  "users.emailLabel": "E-Mail des neuen Mitglieds",
  "users.nameLabel": "Vollständiger Name des neuen Mitglieds",
  "users.emailPlaceholder": "name@firma.de",
  "users.namePlaceholder": "Vollständiger Name",
  "users.deactivateConfirmTitle": "{name} deaktivieren?",
  "users.deactivateConfirmBody":
    "Die Person wird überall abgemeldet und ihre Agent-Pässe werden sofort widerrufen. Du kannst sie später reaktivieren, aber sie muss sich dann neu anmelden.",
  "users.roleLabel": "Rolle für das neue Mitglied",
  "users.invite": "Einladen",
  "users.setRole": "Rolle setzen…",
  "users.setRoleFor": "Rolle für {name} setzen",
  "users.rolesHeld": "Hat {roles}. Eine auszuwählen ersetzt alle",
  "users.deactivate": "Deaktivieren",
  "users.reactivate": "Reaktivieren",
  "users.status.active": "Aktiv",
  "users.status.deactivated": "Deaktiviert",
  "users.status.suspended": "Gesperrt",
  "users.role.admin": "Admin",
  "users.role.manager": "Manager",
  "users.role.rep": "Rep",
  "users.role.read_only": "Nur Lesen",
  "users.role.ops": "Ops",
  "users.link.action": "Passwort-Link erstellen",
  "users.link.title": "Passwort-Link für {name}",
  "users.link.pending": "Link wird erstellt…",
  "users.link.body":
    "Sende diesen Link über einen vertrauenswürdigen Kanal an das Mitglied. Er funktioniert einmal und wird nur jetzt angezeigt. Nach dem Schließen kannst du in der Mitgliederzeile einen neuen erstellen.",
  "users.link.urlLabel": "Passwort-Link",
  "users.link.copy": "Link kopieren",
  "users.link.copied": "Kopiert",
  "users.link.copyFailed":
    "Automatisches Kopieren nicht möglich. Markiere den Link und kopiere ihn.",
  "users.link.expires": "Gültig bis {when}.",
  "users.link.failed":
    "Das Mitglied wurde angelegt, der Link jedoch nicht. Ohne Link kann es sich nicht anmelden.",
  "users.link.offline":
    "Server nicht erreichbar. Prüfe deine Verbindung und versuche es erneut.",
  "users.link.retry": "Erneut versuchen",
  "users.link.done": "Fertig",
  "settings.companyKicker": "Firmenwissen",
  "settings.companyTitle": "Was Margince über dein Unternehmen weiß",
  "settings.companySub":
    "Halte den gemeinsamen Geschäftskontext für Entwürfe, Angebote, Suche und gesteuerte Agenten aktuell. Jede Aussage bleibt mit Quelle und Urheber verbunden.",
  "settings.companyTrust":
    "Nur bestätigtes Wissen — Website-Texte werden nie zu Anweisungen.",
  "settings.companyConfirmed": "bestätigte Aussagen",
  "settings.companyWebsite": "Öffentliche Unternehmenswebsite",
  "settings.companyWebsiteRequired":
    "Füge vor der Aktualisierung eine Unternehmenswebsite hinzu.",
  "settings.companyRefresh": "Von Website aktualisieren",
  "settings.companyEssentials": "Die drei Grundlagen",
  "settings.companyPositioning": "Positionierung, Käufer und Vertrieb",
  "settings.companyIdentity": "Identität und rechtliche Angaben",
  "settings.companyViewSource": "Quelle ansehen",
  "settings.companySave": "Firmenkontext speichern",
  "settings.companySaved": "Gespeichert",
  "settings.companyRefreshUnavailable":
    "Diese Website-Aktualisierung ist nicht mehr verfügbar.",
  "settings.companyRefreshUnreadable":
    "Wir haben den Stand dieses Website-Lesevorgangs verloren. Starte die Aktualisierung erneut.",
  "settings.companyRefreshStale":
    "Der Website-Vorschlag hat sich geändert. Prüfe den neuen Vergleich vor dem Übernehmen.",
  "settings.companyRefreshReview": "Website-Vergleich",
  "settings.companyRefreshReady": "Änderungen prüfen",
  "settings.companyRefreshReading": "Website wird gelesen und belegt…",
  "settings.companyCoverage": "Seitenabdeckung",
  "settings.companyResolveAll":
    "Wähle für jeden Konflikt mit menschlichen Angaben eine Entscheidung.",
  "settings.companyApplyRefresh": "Ausgewählte Änderungen übernehmen",
  "settings.companySelectChange": "Änderung „{field}“ auswählen",
  "settings.companyCurrent": "Aktuell bestätigter Wert",
  "settings.companyWebsiteProposal": "Website-Vorschlag",
  "settings.companyClass.new": "Neu",
  "settings.companyClass.machine_change": "Website geändert",
  "settings.companyClass.human_conflict": "Entscheidung nötig",
  "settings.companyClass.unchanged": "Unverändert",
  "settings.companyResolution.keep_current": "Aktuellen Wert behalten",
  "settings.companyResolution.accept_proposal": "Website übernehmen",
  "settings.companyResolution.use_value": "Meinen bearbeiteten Wert nutzen",
  "settings.companyManualKicker": "Private, manuelle Einrichtung",
  "settings.companyManualTitle": "Gib Margince die Grundlagen",
  "settings.companyManualSub":
    "Das Lesen der Website ist in dieser Rollout-Stufe nicht aktiviert. Diese drei Antworten reichen für einen nützlichen Firmenkontext — ohne Modellaufruf und ohne externe Anfrage.",
  "settings.companyCreateWorkspace": "Firmenkontext erstellen",
  "product.title": "Produkte",
  "product.settingsSub":
    "Rate-Card-Einträge, auf deren Grundlage Angebotspositionen einen Snapshot erstellen.",
  "product.open": "Produkte öffnen",
  "product.new": "Neues Produkt",
  "product.edit": "Produkt bearbeiten",
  "product.archive": "Produkt archivieren",
  "product.archiveConfirm":
    "Dieses Produkt archivieren? Bestehende Angebotszeilen behalten ihren Snapshot.",
  "product.name": "Name",
  "product.sku": "SKU",
  "product.description": "Beschreibung",
  "product.unit": "Einheit",
  "product.unitPrice": "Stückpreis",
  "product.currency": "Währung",
  "product.taxRate": "Standard-Steuersatz %",
  "product.active": "Aktiv",
  "product.activeFilter": "Nur aktive",
  "product.inactive": "Inaktiv",
  "product.archived": "Archiviert",
  "product.sortName": "Name",
  "product.sortCreated": "Neueste",
  "product.empty": "Noch keine Produkte.",

  "template.title": "Angebotsvorlagen",
  "template.settingsSub": "Markenkonforme DE/EN-PDF-Layouts für Angebote.",
  "template.open": "Angebotsvorlagen öffnen",
  "template.new": "Neue Vorlage",
  "template.edit": "Vorlage bearbeiten",
  "template.archive": "Vorlage archivieren",
  "template.archiveConfirm":
    "Diese Vorlage archivieren? Angebote, die sie referenzieren, fallen auf die Standardvorlage der Sprache zurück.",
  "template.name": "Name",
  "template.locale": "Sprache",
  "template.isDefault": "Standard für Sprache",
  "template.header": "Kopftext",
  "template.footer": "Fußtext",
  "template.localeFilter": "Sprache",
  "template.localeDE": "Deutsch (DE)",
  "template.localeEN": "Englisch (US)",
  "template.sortName": "Name",
  "template.empty": "Noch keine Angebotsvorlagen.",

  "tools.title": "Agenten-Werkzeuge",
  "tools.sub":
    "Die geregelte Oberfläche, die ein Passport aufrufen kann — dieselbe Liste, die ein MCP-Client sieht.",
  "tools.col.tool": "Werkzeug",
  "tools.col.verb": "Aktion",
  "tools.col.scope": "Bereich",
  "tools.col.tier": "Stufe",
  "tools.col.egress": "Ausgang",
  "tools.egress": "verlässt den Workspace",
  "tools.scopeAll": "Alle Passports",
  "tools.scopedTo": "Erreichbar durch {label}",
  "tools.unreachable": "Bereich nicht gewährt",

  "aiusage.title": "KI-Nutzung & Budget",
  "aiusage.sub":
    "Ihre eigene Rechnung sichtbar — nach Aufgabe und Stufe, in Tokens.",
  "aiusage.budget": "{spent} von {budget} Tokens · {pct}%",
  "aiusage.band.normal": "normal",
  "aiusage.band.degraded": "Sparmodus",
  "aiusage.band.queued": "Budget erreicht — Hintergrund-KI wartet",
  "aiusage.band.unknown": "Unbekannter Budgetstatus",
  "aiusage.col.task": "Aufgabe",
  "aiusage.col.tier": "Stufe",
  "aiusage.col.calls": "Aufrufe",
  "aiusage.col.cached": "Aus Cache",
  "aiusage.col.tokensIn": "Tokens ein",
  "aiusage.col.tokensOut": "Tokens aus",
  "aiusage.col.cost": "Geschätzte Kosten",
  "aiusage.costNote": "Kosten sind Schätzungen zu den konfigurierten Tarifen.",
  "aiusage.days.show": "Tage anzeigen",
  "aiusage.days.hide": "Tage ausblenden",
  "aiusage.empty": "Keine KI-Aufrufe in diesem Zeitraum.",
  "aiusage.prevMonth": "Vorheriger Monat",
  "aiusage.nextMonth": "Nächster Monat",

  "aibanner.degraded": "KI läuft im Sparmodus.",
  "aibanner.queued": "KI-Budget erreicht — Hintergrund-KI wartet.",
  "aibanner.unknown": "Der KI-Budgetstatus ist unbekannt.",
  "aibanner.link": "Nutzung anzeigen",
  "aibanner.dismiss": "Schließen",

  "aicalls.title": "KI-Aufrufprotokoll",
  "aicalls.sub":
    "Jeder Modellaufruf — Routing, Tokens, Wiederholungen und erfasste Nutzdaten.",
  "aicalls.col.when": "Zeitpunkt",
  "aicalls.col.task": "Aufgabe",
  "aicalls.col.model": "Modell",
  "aicalls.col.tokens": "Tokens",
  "aicalls.col.latency": "Latenz",
  "aicalls.ms": "{value} ms",
  "aicalls.badge.cacheHit": "Cache-Treffer",
  "aicalls.badge.degraded": "reduziert",
  "aicalls.badge.retries": "Wiederholung ×{count}",
  "aicalls.filter.all": "Alle Aufgaben",
  "aicalls.loadMore": "Mehr laden",
  "aicalls.empty": "Noch keine KI-Aufrufe aufgezeichnet.",
  "aicalls.detail.identity":
    "{served} über {provider} bereitgestellt (konfiguriert: {configured})",
  "aicalls.detail.source": "Quelle der Modellidentität: {source}",
  "aicalls.detail.context": "Eingebetteter Kontext: {scopes}",
  "aicalls.detail.contextNone": "Kein Unternehmenskontext eingebettet",
  "aicalls.detail.attempts": "Versuche",
  "aicalls.detail.request": "Anfrage-Nutzdaten",
  "aicalls.detail.response": "Antwort-Nutzdaten",
  "aicalls.payload.off":
    "Nutzdatenerfassung ist aus — ai.capture_payloads: true in margince.yaml aktiviert die Aufzeichnung von Anfrage und Antwort.",
  "aicalls.payload.none": "Für diesen Aufruf wurden keine Nutzdaten erfasst.",

  "aiexport.button": "Als Zertifizierungsszenario exportieren",
  "aiexport.title": "Lauf als Zertifizierungsszenario exportieren",
  "aiexport.nameLabel": "Szenarioname",
  "aiexport.checklist":
    "Geheimnisse wurden bei der Erfassung entfernt. Personenbezogene Daten NICHT — prüfen und entfernen Sie PII und ersetzen Sie anschließend sanitized_by, bevor Sie die Datei in den Korpus übernehmen.",
  "aiexport.copy": "YAML kopieren",
  "aiexport.copied": "Kopiert",
  "aiexport.download": ".yaml herunterladen",
  "aiexport.copyFailed":
    "Kopieren fehlgeschlagen — Vorschau verwenden oder Datei herunterladen.",
  "aiexport.close": "Schließen",
  "aiexport.previewLabel": "Szenariovorschau",
  "aiexport.responseLabel": "Modellantwort",

  "countdown.minutesSeconds": "{minutes}m {seconds}s",
  "countdown.expired": "Abgelaufen",

  // Quotas & Zielerreichung (RD-T06): menschlich gesetzte Umsatzziele mit
  // serverseitig berechneter Zielerreichung, im Reports-Segment „Quotas“.
  "quotas.tab": "Quotas",
  "quotas.sub": "Umsatzziele — menschlich gesetzt, Zielerreichung berechnet",
  "quotas.role.owner": "Individuelle Quota",
  "quotas.role.team": "Team-Quota",
  "quotas.periodRange": "{start} – {end}",
  "quotas.empty.title": "Keine Quota gesetzt",
  "quotas.empty.body":
    "Eine Quota ist ein Ziel, das ein Mensch setzt — Inhaber oder Team, Zeitraum, Betrag. Wir raten es nicht. Setzen Sie ein Ziel, um die Zielerreichung aus gewonnenen Deals zu verfolgen.",
  "quotas.empty.cta": "Ziel setzen",
  "quotas.attained": "erreicht",
  "quotas.closedWon": "Gewonnen in diesem Zeitraum",
  "quotas.target": "Ziel",
  "quotas.gap": "Abstand zum Ziel",
  "quotas.baseCurrencyNote":
    "Beträge in der Basiswährung des Workspace ({currency}).",
  "quotas.pace.ahead":
    "Vor dem Plan — {pct}% erreicht bei {pace}% des Zeitraums.",
  "quotas.pace.behind":
    "Hinter dem Plan — {pct}% erreicht bei {pace}% des Zeitraums.",
  "quotas.pace.met": "Ziel erreicht — {pct}% erreicht.",
  "quotas.computed": "serverseitig berechnet",
  "quotas.contributing.title": "Was zur Zielerreichung zählt",
  "quotas.contributing.subtitle": "gewonnene Deals · Basiswert im Zeitraum",
  "quotas.contributing.deal": "Deal",
  "quotas.contributing.amount": "Gezählter Betrag",
  "quotas.contributing.total": "Gezählte Summe",
  "quotas.contributing.caption":
    "Basiswährung · offene / verlorene / ausgeschlossene Deals ausgenommen",
  "quotas.explain.formula":
    "Zielerreichung = Σ(Basiswert gewonnener Deals) ÷ Ziel, auf den Cent",
  "quotas.explain.closedWon": "gewonnen = {sum} ({count} Deals im Zeitraum)",
  "quotas.explain.target": "Ziel = {target} (menschlich gesetzt)",
  "quotas.explain.result": "Zielerreichung = {sum} ÷ {target} = {pct}%",
  "quotas.explain.exclusions":
    "offene / verlorene / ausgeschlossene Deals ausgenommen; nur sauberer Kern",
  "quotas.scopeNote.title": "Was diese Quota bewusst ist",
  "quotas.scopeNote.flag": "gekennzeichnet, nicht verborgen",
  "quotas.scopeNote.body":
    "Das Ziel ist menschlich gesetzt — die KI erfindet keine Quota-Zahl. Die Zielerreichung wird aus dem Basiswert gewonnener Deals berechnet und ist vollständig auditierbar. Es gibt kein KI-gesetztes Ziel, kein Forecast-zu-Quota und keine Provisions-Engine.",
  "quotas.target.title": "Ziel des Zeitraums",
  "quotas.target.new": "Ziel setzen",
  "quotas.target.edit": "Ziel bearbeiten",
  "quotas.target.save": "Ziel speichern",
  "quotas.target.note":
    "Das Bearbeiten schreibt einen menschlich getippten Wert und protokolliert die Änderung. Die Zielerreichung wird neu berechnet.",
  "quotas.target.sideFixed":
    "Die Inhaber-/Team-Seite einer Quota ist fest — zum Wechseln archivieren und neu anlegen.",
  "quotas.side.label": "Zugewiesen an",
  "quotas.side.owner": "Inhaber",
  "quotas.side.team": "Team",
  "quotas.owner": "Inhaber",
  "quotas.team": "Team",
  "quotas.pickOwner": "Inhaber auswählen…",
  "quotas.pickTeam": "Team auswählen…",
  "quotas.amountHint": "Ganze Euro — keine Dezimalstellen",
  "quotas.periodStart": "Zeitraum-Beginn",
  "quotas.periodEnd": "Zeitraum-Ende",
  "quotas.amount": "Zielbetrag",
  "quotas.currency": "Währung",
  "quotas.err.targetZero": "Diese Quota hat noch kein Ziel",
  "quotas.err.computeFailed": "Zielerreichung konnte nicht berechnet werden",
  "quotas.err.ownerXorTeam": "Wählen Sie genau eines: Inhaber oder Team.",
  "quotas.archive.title": "Quota archivieren",
  "quotas.archive.confirm":
    "Das Archivieren entfernt diese Quota aus der Liste und stoppt die Verfolgung der Zielerreichung. Archivierte Quotas können nicht bearbeitet werden.",

  "settings.tab.installation": "Installation",
  "installationSettings.title": "Installation",
  "installationSettings.sub":
    "Wie diese Installation heißt und in welcher Zeitzone und Währung jede Auswertung berechnet wird.",
  "installationSettings.name": "Name der Organisation",
  "installationSettings.nameHint":
    "Wird überall dort angezeigt, wo das Produkt Ihre Organisation benennt.",
  "installationSettings.timezone": "Zeitzone für Auswertungen",
  "installationSettings.timezoneHint":
    "IANA-Zonenname (zum Beispiel Europe/Berlin). Periodengrenzen aller Auswertungen werden darin berechnet, unabhängig von Ihrer eigenen Anzeigezeitzone.",
  "installationSettings.baseCurrency": "Basiswährung",
  "installationSettings.baseCurrencyHint":
    "ISO-4217-Code, in den alle Beträge für Auswertungen umgerechnet werden. Änderbar, bis der erste Deal dagegen umgerechnet wurde.",
  "installationSettings.baseCurrencyLocked":
    "Gesperrt: Es wurden bereits Deals gegen diese Währung umgerechnet — eine Änderung würde jede darauf aufbauende Auswertung neu bedeuten.",
  "installationSettings.save": "Speichern",
  "captureSettings.title": "Erfassung",
  "captureSettings.sub":
    "Wie erfasste Unternehmen und Kontakte nach ihrer Erstellung angereichert werden.",
  "captureSettings.autoEnrich.label":
    "Erfasste Unternehmen automatisch anreichern",
  "captureSettings.autoEnrich.help":
    "Wenn aktiviert, erhält jedes aus erfassten E-Mails erstellte Unternehmen automatisch ein Web-Dossier — seine Website wird gelesen und sein Profil ausgefüllt. Läuft unter einem Tageslimit.",
  "captureSettings.adminOnly":
    "Nur ein Administrator oder Ops kann dies ändern.",

  "ownDomains.title": "Eigene E-Mail-Domains",
  "ownDomains.sub":
    "Die Domains, die zu diesem Unternehmen gehören. Schreiben sich Kolleg:innen untereinander, wird diese Nachricht nicht gespeichert. Auch nicht für Sie.",
  "ownDomains.irreversible":
    "Eine Domain hier einzutragen wirkt ab der nächsten Nachricht. Wird sie später entfernt, wird ab diesem Zeitpunkt wieder erfasst. Was übersprungen wurde, solange sie eingetragen war, liefert kein Postfach ein zweites Mal. Bereits erfasste E-Mails bleiben.",
  "ownDomains.fromCompany": "Aus dem Unternehmensprofil. Dort zu ändern:",
  "ownDomains.empty":
    "Keine weiteren Domains eingetragen. Tragen Sie eine ein, wenn Ihr Unternehmen unter mehr als einer Domain schreibt.",
  "ownDomains.confirmed": "bestätigt",
  "ownDomains.candidate":
    "aus einem verbundenen Postfach, noch nicht bestätigt",
  "ownDomains.add": "Hinzufügen",
  "ownDomains.addLabel": "Eigene Domain hinzufügen",
  "ownDomains.placeholder": "beispiel.de",
  "ownDomains.remove": "{domain} entfernen",

  "webhooks.title": "Webhooks",
  "webhooks.sub":
    "Ausgehende Abonnements, die signierte HTTP-POSTs für ausgewählte Ereignisse empfangen.",
  "webhooks.new": "Neues Abonnement",
  "webhooks.notConfigured":
    "Ausgehende Webhooks sind auf dieser Installation nicht aktiviert — zuerst muss ein Signaturschlüssel konfiguriert werden.",
  "webhooks.state.active": "Aktiv",
  "webhooks.state.paused": "Pausiert",
  "webhooks.updated": "Aktualisiert {date}",
  "webhooks.field.targetUrl": "Ziel-URL",
  "webhooks.field.eventTypes": "Ereignistypen",
  "webhooks.field.state": "Status",
  "webhooks.edit": "Bearbeiten",
  "webhooks.archive": "Archivieren",
  "webhooks.archiveConfirm":
    "Das Archivieren stoppt jede Zustellung für dieses Abonnement. Dies kann nicht rückgängig gemacht werden.",
  "webhooks.rotate": "Schlüssel rotieren",
  "webhooks.rotateConfirm.title": "Signaturschlüssel rotieren?",
  "webhooks.rotateConfirm.body":
    "Mit dem Bestätigen wird der aktuelle Schlüssel sofort ungültig und der neue Schlüssel danach einmalig angezeigt. Kopieren Sie ihn und aktualisieren Sie Ihren Empfänger, sobald die Rotation abgeschlossen ist.",
  "webhooks.secret.title": "Signaturschlüssel",
  "webhooks.secret.warning":
    "Dieser Schlüssel wird nur einmal angezeigt und kann danach nicht erneut abgerufen werden. Speichern Sie ihn jetzt — Zustellungen werden damit signiert.",
  "webhooks.secret.copy": "Kopieren",
  "webhooks.secret.copied": "Kopiert",
  "webhooks.secret.copyFailed":
    "Automatisches Kopieren fehlgeschlagen — bitte den Schlüssel manuell auswählen und kopieren.",
  "webhooks.secret.done": "Fertig",

  "webhooks.deliveries.show": "Zustellungen anzeigen",
  "webhooks.deliveries.hide": "Zustellungen ausblenden",
  "webhooks.deliveries.empty": "Noch keine Zustellversuche.",
  "webhooks.deliveries.deadLetterGroup": "Dead-Letter ({count})",
  "webhooks.deliveries.allGroup": "Weitere Versuche",
  "webhooks.deliveries.column.status": "Status",
  "webhooks.deliveries.column.event": "Ereignis",
  "webhooks.deliveries.column.attempts": "Versuche",
  "webhooks.deliveries.column.lastStatusCode": "Letzter Status",
  "webhooks.deliveries.column.lastError": "Letzter Fehler",
  "webhooks.deliveries.column.created": "Erstellt",
  "webhooks.deliveries.column.resolved": "Abgeschlossen / nächster Versuch",
  "webhooks.deliveries.status.pending": "Ausstehend",
  "webhooks.deliveries.status.delivered": "Zugestellt",
  "webhooks.deliveries.status.retrying": "Wird wiederholt",
  "webhooks.deliveries.status.dead_lettered": "Dead-Letter",
  "webhooks.deliveries.replay": "Erneut zustellen",
  "webhooks.deliveries.replayConfirm.title":
    "Diese Zustellung erneut versuchen?",
  "webhooks.deliveries.replayConfirm.body":
    "Versucht die Zustellung sofort erneut, signiert mit dem aktuellen Schlüssel und einem neuen Zeitstempel. Es wird nicht auf den nächsten geplanten Versuch gewartet.",
  "reindexbanner.needed": "Neuindizierung erforderlich",
  "reindexbanner.link": "In den Einstellungen prüfen",

  "embedreindex.title": "Suchindex",
  "embedreindex.sub":
    "Der Neuindizierungsstatus des Embedding-Speichers — nur admin/ops, auch zum Ansehen.",
  "embedreindex.loading": "Indexstatus wird geprüft…",
  "embedreindex.statusUnavailable":
    "Der Indexstatus ist gerade nicht verfügbar.",
  "embedreindex.statusIdle": "Aktuell",
  "embedreindex.statusNeeded": "Neuindizierung erforderlich",
  "embedreindex.statusReembedding": "Neuindizierung läuft…",
  "embedreindex.lastProgress": "Letzter Fortschritt vor {duration}",
  "embedreindex.entitiesPending": "{count} Einträge ausstehend",
  "embedreindex.workspacePending": "{count} ausstehend",
  "embedreindex.reviewCta": "Prüfen & neu indizieren",
  "embedreindex.rebuildCta": "Index neu aufbauen",
  "embedreindex.confirmTitle": "Neuindizierung starten",
  "embedreindex.rebuildTitle": "Suchindex neu aufbauen",
  "embedreindex.confirmCta": "Neuindizierung starten",
  "embedreindex.rebuildConfirmCta": "Jetzt neu aufbauen",
  "embedreindex.starting": "Wird gestartet…",
  "embedreindex.previewLoading": "Umfang wird geschätzt…",
  "embedreindex.estimateEntities": "Neu einzubettende Einträge:",
  "embedreindex.estimateTokens": "Geschätzte KI-Tokens:",
  "embedreindex.estimateCost": "Geschätzte Kosten:",
  "embedreindex.estimateQualityHeuristic":
    "Heuristische Schätzung — eine kalte Arbeitsmengen-Untergrenze, kein beobachteter Verbrauch.",
  "embedreindex.utilizationTitle": "Budgetauswirkung je Workspace",
  "embedreindex.impact.normal": "normal",
  "embedreindex.impact.degraded": "würde in den Sparmodus wechseln",
  "embedreindex.impact.queued": "würde in die Warteschlange gestellt",

  "consent.title": "Zugriff autorisieren",
  "consent.asks": "{client} möchte in Margince als du handeln.",
  "consent.lend": "Leih ihm einen deiner Agent-Passports",
  "consent.grantedNote":
    "Diese Verbindung erhält genau die gezeigten Berechtigungen — die dieses Passports.",
  "consent.offline":
    "Sie bleibt verbunden, ohne erneut zu fragen, und erneuert den Zugriff, bis du ihn widerrufst.",
  "consent.approve": "Autorisieren",
  "consent.deny": "Zugriff verweigern",
  "consent.emptyTitle": "Du brauchst zuerst einen Agent-Passport",
  "consent.emptyBody":
    "Ein Passport ist die Vollmacht, die du einem Agenten leihst — er überschreitet nie deine eigenen Berechtigungen, und du kannst ihn jederzeit widerrufen. Stell einen aus, und wir bringen dich hierher zurück, um die Verbindung mit {client} abzuschließen.",
  "consent.emptyCta": "Passport ausstellen",
  "consent.expires": "läuft ab am {date}",
  "consent.resumeTitle": "Verbindung mit {client} abschließen",
  "consent.resumeBody":
    "Du bist hierher gekommen, um einen Passport für {client} auszustellen. Sobald du einen hast, machst du dort weiter, wo du aufgehört hast.",
  "consent.resume": "Verbindung fortsetzen",
  "consent.resumeDismiss": "Diese Verbindung abbrechen",
  "consent.reentering": "Verbinde erneut…",
  "consent.backToApp": "Zurück zu Margince",
  "consent.staleTitle": "Diese Anfrage ist abgelaufen",
  "consent.staleBody":
    "Die Verbindungsanfrage ist nicht mehr gültig. Geh zurück zur App, die du verbinden wolltest, und starte erneut — ein Neuladen dieser Seite hilft nicht.",
  "consent.unlendableTitle": "Dieser Passport kann nicht mehr verliehen werden",
  "consent.unlendableBody":
    "Der für {client} gewählte Passport wurde widerrufen, ist abgelaufen oder bereits an eine andere Verbindung gebunden. Wähle unten einen anderen aus.",
  "consent.invalidTitle":
    "Diese Verbindungsanfrage konnte nicht abgeschlossen werden",
  "consent.invalidBody":
    "Diese Installation autorisiert die Anfrage in dieser Form nicht — die App ist hier möglicherweise nicht mehr registriert. Geh zurück zur App, die du verbinden wolltest, und starte erneut.",
  "consent.unnamedPassport": "Unbenannter Passport ({id})",
  "person.thin.title": "Was wir bisher wissen",
  "person.thin.known":
    "Wir haben {what} zu {name}, aber niemand hier hat bisher einen erfassten Austausch mit ihnen.",
  "person.thin.remediation.capture":
    "Verbinden Sie das Postfach, das mit ihnen schreibt - dann fuellt sich diese Seite von selbst, jedes Feld mit seiner Quelle.",
  "person.thin.remediation.employer":
    "Hinterlegen Sie den Arbeitgeber, dann liest Margince dessen Website nach ihrer Rolle.",
  "person.thin.logFirst": "Ersten Kontakt erfassen",
  "person.timeline.all": "Alle",
  "person.timeline.messages": "Nachrichten",
  "person.timeline.meetings": "Termine",
  "person.timeline.tasks": "Aufgaben",
  "person.enriched.title": "Was Margince gelesen hat",
  "person.enriched.sub":
    "Jeder Wert mit dem Text, aus dem er gelesen wurde. Eine Korrektur bleibt bestehen.",
  "person.enriched.field.title": "Position",
  "person.enriched.field.phone": "Telefon",
  "person.enriched.field.role": "Rolle",
  "person.enriched.field.linkedin": "LinkedIn",
  "person.enriched.field.org_name": "Unternehmen",
  "person.enriched.readFrom": "Gelesen aus {source} am {when}",
  "person.enriched.correctedByYou": "Von Ihnen korrigiert",
  "person.enriched.confirmed": "Best\u00e4tigt",
  "person.enriched.correct": "Korrigieren",
  "person.enriched.confirm": "Das stimmt",
  "person.enriched.save": "Korrektur speichern",
  "person.enriched.cancel": "Abbrechen",
  "person.graph.loading": "Das Netzwerk um diesen Kontakt wird gelesen \u2026",
  "person.graph.routeTitle": "Der w\u00e4rmste Weg hinein",
  "person.graph.routeDirect": "{name} steht bereits im Austausch mit ihnen.",
  "person.graph.routeVia":
    "{name} steht im Austausch mit {through} im selben Unternehmen.",
  "person.graph.noRoute":
    "Bisher steht hier niemand im Austausch mit ihnen oder mit jemandem in ihrem Unternehmen.",
  "person.graph.direct": "Wer sie kennt",
  "person.graph.directSub":
    "Kolleginnen und Kollegen, die selbst mit diesem Kontakt korrespondiert haben.",
  "person.graph.noDirect": "Hier hat niemand mit ihnen korrespondiert.",
  "person.graph.account": "Im selben Unternehmen",
  "person.graph.accountSub":
    "Ihre Kolleginnen und Kollegen \u2014 und wer hier zu jedem den w\u00e4rmsten Draht hat.",
  "person.graph.noAccount":
    "Keine weiteren Kontakte in diesem Unternehmen erfasst.",
  "person.graph.omitted":
    "Ein Teil davon ist ausgeblendet, weil die Berechtigung daf\u00fcr fehlt.",
  "person.graph.noEdge": "Keine erfasste Korrespondenz mit {name}.",
  "person.graph.withColleague": "mit {name}",
  "person.graph.withContact": "mit diesem Kontakt",
  "person.graph.counts":
    "{total} Interaktionen in 90 Tagen \u00b7 {inbound} eingehend, {outbound} ausgehend",
  "person.graph.countsOnly":
    "Nur Z\u00e4hlwerte \u2014 die Nachrichten selbst bleiben in der Chronik.",
  "person.graph.untitledMessage": "Nachricht ohne Betreff",
  "person.graph.dropped": "{count} weitere werden nicht angezeigt.",
  "person.moment.dismiss": "Nicht jetzt",
  "person.moment.recommended": "Als N\u00e4chstes:",
  "person.moment.willConfirm": "wird zur Best\u00e4tigung vorgelegt",
  "person.moment.blocked": "F\u00fcr diesen Datensatz nicht verf\u00fcgbar.",
  "person.moment.kind.replied_after_gap": "Sie haben sich gemeldet",
  "person.moment.kind.unanswered_inbound": "Antwort offen",
  "person.moment.kind.meeting_ahead": "Steht an",
  "person.moment.kind.task_overdue": "\u00dcberf\u00e4llig",
  "person.moment.kind.went_quiet": "Still geworden",
  "person.change.repliedAfterGap": "Antwort nach {days} stillen Tagen.",
  "person.change.wentQuiet": "Seit {days} Tagen ist nichts passiert.",
  "person.change.warmed": "Die Beziehung ist von {from} auf {to} gestiegen.",
  "person.change.cooled": "Die Beziehung ist von {from} auf {to} gefallen.",
  "person.band.none": "kein Kontakt",
  "person.band.weak": "schwach",
  "person.band.moderate": "mittel",
  "person.band.strong": "stark",
  "person.pulse.title": "Beziehung",
  "person.pulse.warmestIs": "{name} hat hier die engste Beziehung.",
  "person.pulse.nobodyYet":
    "Niemand hier hat bisher einen erfassten Austausch mit ihnen.",
  "person.pulse.lastInbound": "Letzte Nachricht von ihnen",
  "person.pulse.lastOutbound": "Letzte Nachricht von uns",
  "person.pulse.neverInbound": "nie",
  "person.pulse.neverOutbound": "nie",
  "person.pulse.why": "Wie das berechnet wird",
  "person.pulse.arithmetic":
    "Wert {score}/100 = 100 x Aktualitaet {recency} x Haeufigkeit {frequency} x Gegenseitigkeit {reciprocity}. Beim Lesen berechnet, nie gespeichert.",
  "person.identity.title": "Identitaet",
  "person.identity.email": "E-Mail",
  "person.identity.phone": "Telefon",
  "person.identity.currentRole": "Aktuelle Rolle",
  "person.identity.buyingRole": "Rolle im Kaufprozess",
  "person.career.title": "Fruehere Rollen",
  "person.consent.title": "Ausgehend-Schutz",
  "person.consent.allowed": "Erlaubt: {purposes}",
  "person.consent.noneGranted":
    "Kein Zweck ist eingewilligt, ausgehende Nachrichten bleiben blockiert.",
  "person.consent.blocked": "Blockiert: {purposes}",
  "person.network.title": "Wer kennt sie hier",
  "person.network.twoWay": "{count} beidseitige Kontakte in 90 Tagen",
  "person.network.oneSided": "{count} Kontakte in 90 Tagen, einseitig",
  "person.network.replied": "antwortete {when}",
} as const satisfies Record<MessageKey, string>;
