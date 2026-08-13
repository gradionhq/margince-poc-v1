import { api, QueryStates, throwProblem } from "@margince/frontend/api";
import {
  formatDateTime,
  useCan,
  useCanWrite,
  useLocale,
  useT,
} from "@margince/frontend/app";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  SectionHeader,
  Select,
  TextInput,
} from "@margince/frontend/design-system";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

// #/ext/notes — "Demo Notepad", the reference extension's one screen.
//
// It lives in the UNIT's own tree, as a pnpm workspace package
// (@margince-ext/notes), and gen-composition lists it in the composed screen
// registry that App.tsx's ExtensionRoute dispatches through. That is the
// supply-chain decision this slice is: unit-authored TSX, and every npm package
// the unit declares, are compiled into the SPA bundle. The guards on it are
// collectUnitFrontend (the package must be private, correctly named, and must
// take react / react-dom / @tanstack/react-query as PEERS so the host's single
// copy is the one that runs) and frontend/scripts/check-ext-imports.sh (the
// core is reachable only through frontend/package.json's exports map, and npm
// only through what this package declares).
//
// THREE consequences, all of which the UAT depends on:
//
//   - This file is in the COMPOSED lane only. `api.POST("/ext/notes/…")` is
//     correctly a type error in the vanilla lane — that installation does not
//     serve the route — so tsconfig.composed.json compiles it against the
//     MERGED contract and reaches it through the generated registry's imports.
//     If the overlay did not merge, this file does not typecheck: the route half
//     of the ordering constraint is still a compile-time guard, even though the
//     RBAC-object half is not (see capability.ts).
//   - Its copy travels with it too, in ./i18n/<locale>.json, namespaced
//     `extNotes.` and merged into the one catalogue by gen-composition — so
//     `useT` here is the same lookup a core screen makes, and removing the unit
//     takes its strings with it.
//   - `rm -rf extensions/notes && make composition` still reproduces the
//     vanilla stubs byte for byte, because nothing here is generated. The
//     registry goes empty, the dispatch misses, and #/ext/notes answers the
//     ordinary not-found card.
//
// The shared `api` client, no second client and no cast: contract paths are
// unprefixed and client.ts's baseUrl carries the /v1 mount, so an extension
// route reads exactly like a core one at the call site.
//
// Each operation is called with the method its own fragment declares, and where
// the arguments go follows from that: the reads are GETs sending nothing, `add`
// POSTs a body, `signing-key` is a PUT because storing replaces, and `remove` is
// a DELETE whose id rides `params.query` because a DELETE carries no body.
//
// `signature` is the one to look twice at — it is a read on a POST, on purpose,
// because its payload has no business in a URL. See the fragment, which is where
// that reasoning lives.

/** The RBAC object the unit's record operations gate on. */
const NOTE_OBJECT = "ext_notes_note";

/**
 * The RBAC object the unit's three SECRETS operations gate on — a second
 * object, not the notes one.
 *
 * The distinction is the point: a role that may add a note has no business
 * rotating the installation's signing key, and one object for both would make
 * that a single grant. Until the UAT re-run found it (R1) these operations
 * declared no object at all, so any authenticated seat — read-only included —
 * could replace the key on both the screen and the agent path.
 */
const SIGNING_KEY_OBJECT = "ext_notes_signing_key";

/** One poll interval for the heartbeat, in milliseconds. */
const HEARTBEAT_POLL_MS = 15_000;

export default function NotesScreen() {
  const t = useT();
  return (
    <div className="wrap narrow">
      {/* level 1, and this is the rule for a unit screen rather than a choice
          this one made: the app shell yields the page's name to a composed unit,
          so the screen's own top header IS the page's h1. Every other header
          under it stays at the default 2. */}
      <SectionHeader
        title={t("extNotes.title")}
        sub={t("extNotes.sub")}
        level={1}
      />
      <SigningCard />
      <FilingCard />
      <NotesCard />
    </div>
  );
}

// ── secrets ──────────────────────────────────────────────────────────────────

/**
 * `enabled` is the caller's `read` grant, not a convenience: without it an
 * ungranted seat fires a request the server answers 403, and the card would
 * render a failed query where the honest answer is "you were not granted this".
 */
function useSigningKeyStatus(enabled: boolean) {
  return useQuery({
    enabled,
    queryKey: ["ext", "notes", "signing-key"],
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/ext/notes/signing-key/status",
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      // The declared field, or an error. `data.stored` on a body that does not
      // carry it is `undefined`, which is falsey — so a response this screen
      // could not read would render "No key stored", which is a claim about
      // the installation rather than about the read. Wrong in the one
      // direction that matters: it invites someone to paste a key over one
      // that is already there.
      if (typeof data?.stored !== "boolean") {
        throw new Error("the signing-key status carried no `stored` field");
      }
      return data.stored;
    },
  });
}

/**
 * The signing card: paste a key, then sign something with it.
 *
 * The key is never displayed, and nothing here asks the server for it — no
 * operation returns it, masked or otherwise. What the screen shows is the two
 * facts a stored credential can honestly produce: that one is present, and a
 * signature computed with it.
 */
function SigningCard() {
  const t = useT();
  const queryClient = useQueryClient();
  // BEFORE the query hook's data is used, and deliberately not folded together:
  // `read` decides whether this card has anything to say (status and signing
  // both need it), while `update` decides only whether the key can be REPLACED.
  // A seat that may verify a signature must still see the card.
  const canRead = useCan(SIGNING_KEY_OBJECT, "read");
  const canStore = useCanWrite(SIGNING_KEY_OBJECT, "update");
  const status = useSigningKeyStatus(canRead);
  const [key, setKey] = useState("");
  const [payload, setPayload] = useState("");
  const [signature, setSignature] = useState("");
  // A signature belongs to the payload it was computed over, so it is cleared
  // the moment that payload changes and again when a later attempt fails.
  // Without that it survives both, and the card then shows a valid signature
  // beside text it does not sign — which reads as verification of the new
  // payload.
  const [signatureFailed, setSignatureFailed] = useState(false);
  const changePayload = (value: string) => {
    setPayload(value);
    setSignature("");
    setSignatureFailed(false);
  };

  const store = useMutation({
    mutationFn: async (value: string) => {
      const { error, response } = await api.PUT("/ext/notes/signing-key", {
        body: { key: value },
      });
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    // onSettled, not onSuccess: a request that failed did not necessarily fail
    // to STORE. A response lost on the way back leaves the key committed while
    // the client sees an error, so the one thing this screen must not do is
    // assert a rollback it cannot know about — it re-reads the status instead,
    // and the failure copy says the outcome is open. The input is cleared only
    // on success, because a key the operator may still need to re-paste must
    // not vanish under a failure.
    onSuccess: () => setKey(""),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: ["ext", "notes", "signing-key"],
      }),
  });

  const sign = useMutation({
    mutationFn: async (value: string) => {
      setSignature("");
      setSignatureFailed(false);
      const { data, error, response } = await api.POST("/ext/notes/signature", {
        body: { payload: value },
      });
      if (error || !response.ok) {
        throwProblem(error);
      }
      return data;
    },
    // GUARDED, like useSigningKeyStatus's `typeof data?.stored !== "boolean"`
    // and useNotes' Array.isArray, and for the same reason both of those
    // exist: the governed-tool envelope nests the payload under `data`, the
    // generated types mark both members required so the compiler does not
    // help, and rendering them unchecked puts the string "undefined undefined"
    // on screen as a signature. A signature a person cannot tell from a real
    // one is the worst thing this card can show.
    onSuccess: (data) => {
      if (
        typeof data?.algorithm !== "string" ||
        typeof data?.signature !== "string"
      ) {
        setSignature("");
        setSignatureFailed(true);
        return;
      }
      setSignature(`${data.algorithm} ${data.signature}`);
    },
  });

  if (!canRead) {
    return (
      <Card>
        <EmptyState>{t("extNotes.signing.noGrant")}</EmptyState>
      </Card>
    );
  }
  return (
    <Card>
      <SectionHeader
        title={t("extNotes.signing.title")}
        sub={t("extNotes.signing.sub")}
      />
      {/*
        Through the query gate, not off `status.data` directly. `data` is
        undefined while the read is in flight and undefined when it failed, and
        both would render the "No key stored" badge — a statement about the
        installation made from a read that produced nothing. The two are not
        interchangeable: one of them invites someone to paste a key over one
        that is already there.
      */}
      <QueryStates query={status}>
        <p>
          {status.data ? (
            <Badge tone="success">{t("extNotes.signing.connected")}</Badge>
          ) : (
            <Badge tone="warn">{t("extNotes.signing.notConnected")}</Badge>
          )}
        </p>
      </QueryStates>
      {canStore ? (
        <>
          <Field label={t("extNotes.signing.keyLabel")}>
            {(control) => (
              <TextInput
                {...control}
                type="password"
                value={key}
                onChange={(event) => setKey(event.target.value)}
              />
            )}
          </Field>
          <Button
            disabled={key.trim() === "" || store.isPending}
            onClick={() => store.mutate(key)}
          >
            {t("extNotes.signing.store")}
          </Button>
          {store.isError ? <p>{t("extNotes.signing.storeFailed")}</p> : null}
        </>
      ) : null}
      <Field label={t("extNotes.signing.payloadLabel")}>
        {(control) => (
          <TextInput
            {...control}
            value={payload}
            onChange={(event) => changePayload(event.target.value)}
          />
        )}
      </Field>
      <Button
        disabled={payload === "" || sign.isPending}
        onClick={() => sign.mutate(payload)}
      >
        {t("extNotes.signing.sign")}
      </Button>
      {signature === "" ? null : <p>{signature}</p>}
      {sign.isError || signatureFailed ? (
        <p>{t("extNotes.signing.signFailed")}</p>
      ) : null}
    </Card>
  );
}

// ── migrations + api + jobs ──────────────────────────────────────────────────

/**
 * `enabled` is the caller's `read` grant, for the reason the signing card's is:
 * an ungranted seat would otherwise fire a request the server answers 403 —
 * and then fire it again every {@link HEARTBEAT_POLL_MS}, because this query
 * polls. A refused read on a timer is a failed query where the honest answer
 * is "you were not granted this".
 */
function useNotes(enabled: boolean) {
  return useQuery({
    enabled,
    queryKey: ["ext", "notes", "notes"],
    queryFn: async () => {
      const { data, error, response } = await api.GET("/ext/notes/list");
      if (error || !response.ok) {
        throwProblem(error);
      }
      // Same rule as the signing status, and here it is what the UAT caught:
      // a body shaped like the governed-tool envelope carries the notes under
      // `data`, so `data.notes` is `undefined` — and `undefined?.length === 0`
      // is falsey, so the list rendered as though the workspace simply had no
      // notes. An unreadable response must reach the query gate's error card,
      // never the empty state.
      if (!Array.isArray(data?.notes)) {
        throw new Error("the notes list carried no `notes` array");
      }
      return data.notes;
    },
    // The heartbeat writes a row with no user action, and the whole point of
    // showing it is that it appears while somebody watches. A screen that only
    // refetched on a mutation would show the tick on the next click, which
    // reads as "the note I just added arrived late".
    refetchInterval: HEARTBEAT_POLL_MS,
  });
}

// The RBAC object filing gates on, and the reason it is a THIRD object rather
// than the notepad's: a seat that may jot a note in the unit's own table has no
// business writing to the shared timeline every other record hangs its history
// off. No seeded role holds it, so this card is absent on a fresh installation
// until an admin grants it deliberately.
const FILING_OBJECT = "ext_notes_filing";

/** The records a note can be filed to, exactly as the operation declares them. */
type SubjectType = "person" | "organization" | "deal" | "lead";
const SUBJECT_TYPES: readonly SubjectType[] = [
  "person",
  "organization",
  "deal",
  "lead",
];

/**
 * File a note to a record: the unit's own row and a core activity, together.
 *
 * This is the one card on this screen that writes something the PRODUCT owns,
 * and the gate is honest about needing two grants. `ext_notes_filing` decides
 * whether the card appears; the core activity is written under the caller's own
 * permissions, so a seat without `activity:create` sees the control, presses
 * it, and is refused by the server. That is not a gap this screen can close —
 * nothing declares the pairing — so the failure copy says the write did not
 * happen rather than guessing which half was missing.
 */
function FilingCard() {
  const t = useT();
  const queryClient = useQueryClient();
  const canFile = useCanWrite(FILING_OBJECT, "create");
  const [body, setBody] = useState("");
  // Typed as the contract's own enum, not a string: the Select's options are
  // the four the operation admits, and a widened state is how a fifth value
  // reaches a request the schema refuses.
  const [subjectType, setSubjectType] = useState<SubjectType>("person");
  const [subjectID, setSubjectID] = useState("");
  const [filed, setFiled] = useState(false);

  const file = useMutation({
    mutationFn: async () => {
      setFiled(false);
      const { error, response } = await api.POST("/ext/notes/file", {
        body: { body, subject_type: subjectType, subject_id: subjectID },
      });
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    // Both halves landed or neither did, so the whole form clears on success
    // and nothing clears on failure: a person who was refused still has what
    // they typed.
    onSuccess: () => {
      setBody("");
      setSubjectID("");
      setFiled(true);
    },
    // onSettled, and the same key the notepad reads under — the two spellings
    // of one query key are how a list silently stops refreshing. A request
    // that FAILED did not necessarily fail to write: the server's write is one
    // transaction, but a response lost on the way back leaves it committed
    // while the client sees an error, so the list is re-read either way and
    // the failure copy leaves the outcome open. The store and add mutations on
    // this screen take the same posture, for the same reason.
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: ["ext", "notes", "notes"] }),
  });

  if (!canFile) {
    return (
      <Card>
        <EmptyState>{t("extNotes.filing.noGrant")}</EmptyState>
      </Card>
    );
  }
  return (
    <Card>
      <SectionHeader title={t("extNotes.filing.title")} />
      <Field label={t("extNotes.filing.bodyLabel")}>
        {(control) => (
          <TextInput
            {...control}
            value={body}
            onChange={(event) => {
              setBody(event.target.value);
              setFiled(false);
            }}
          />
        )}
      </Field>
      <Field label={t("extNotes.filing.subjectTypeLabel")}>
        {(control) => (
          <Select
            {...control}
            value={subjectType}
            onChange={(value) => {
              // The Select hands back a string; only one of the four it was
              // given can come back, and narrowing here is what keeps the
              // request's type honest without a cast.
              const picked = SUBJECT_TYPES.find((type) => type === value);
              if (picked) {
                setSubjectType(picked);
              }
            }}
            options={SUBJECT_TYPES.map((type) => ({
              value: type,
              label: t(`extNotes.filing.${type}`),
            }))}
          />
        )}
      </Field>
      <Field label={t("extNotes.filing.subjectIdLabel")}>
        {(control) => (
          <TextInput
            {...control}
            value={subjectID}
            onChange={(event) => {
              // Trimmed at the edge: the contract declares format: uuid, and a
              // pasted id with a stray space is a request the server refuses
              // for a reason the person cannot see.
              setSubjectID(event.target.value.trim());
              setFiled(false);
            }}
          />
        )}
      </Field>
      <Button
        disabled={
          body.trim() === "" || subjectID.trim() === "" || file.isPending
        }
        onClick={() => file.mutate()}
      >
        {t("extNotes.filing.file")}
      </Button>
      {filed ? <p>{t("extNotes.filing.filed")}</p> : null}
      {file.isError ? <p>{t("extNotes.filing.failed")}</p> : null}
    </Card>
  );
}

/**
 * The notepad: the list, the Add control, and per-row removal.
 *
 * `Add` and the row's remove control are gated on the unit's OWN RBAC object,
 * registered into the vocabulary at boot from the fragment's `x-rbac-object`
 * and reported in /me. This is UX honesty, never enforcement — and unlike the
 * core screens this phrasing is inherited from, that sentence is now backed by
 * a server that actually refuses: `extensionTool.Handle` requires the declared
 * object and action of the calling principal before the handler runs, on BOTH
 * the mounted REST route and the agent's tools/call, because the two converge
 * on `Registry.Invoke`.
 *
 * It was not, until this round. The object registered into the vocabulary,
 * reached /me, and gated nothing — so this screen hid its controls from a
 * principal who could still list and write the same notes through the agent.
 * The gating actions below (`create` on Add, `delete` on Remove) are the same
 * ones the fragment declares in `x-rbac-action`, and they have to stay that
 * way: a screen gating on a verb the route does not require is a control that
 * disappears for someone the server would have served, and the reverse is a
 * control that leads to a 403.
 */
function NotesCard() {
  const t = useT();
  const { locale } = useLocale();
  // The reader's own zone: a note's timestamp is only useful next to the clock
  // on the wall behind them, and nothing about a demo note belongs to a
  // workspace-configured zone.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const queryClient = useQueryClient();
  // Not folded into canAdd: the read grant is what decides whether the card
  // has anything to say at all, and a seat that may read but not write must
  // still see the list. Read BEFORE the query, because it gates it.
  const canRead = useCan(NOTE_OBJECT, "read");
  const notes = useNotes(canRead);
  const canAdd = useCanWrite(NOTE_OBJECT, "create");
  const canRemove = useCanWrite(NOTE_OBJECT, "delete");
  const [body, setBody] = useState("");

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["ext", "notes", "notes"] });

  const add = useMutation({
    mutationFn: async (value: string) => {
      const { error, response } = await api.POST("/ext/notes/add", {
        body: { body: value },
      });
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    // Same posture as the signing card's store: the list is re-read whichever
    // way the request ended, because a lost response can leave the note
    // written, and the copy tells the reader to look rather than claiming the
    // write was undone.
    onSuccess: () => setBody(""),
    onSettled: invalidate,
  });

  // WHICH note was refused, not merely that one was. A single flag over a list
  // puts the message under the whole list, so a person who pressed Remove on
  // the third row reads a refusal that names none of them — and if they then
  // press the fourth and it succeeds, the message from the third is still
  // there. The id is the only thing that makes the line answerable.
  const [removeFailedID, setRemoveFailedID] = useState<string | null>(null);
  const remove = useMutation({
    onMutate: () => setRemoveFailedID(null),
    onError: (_error, id: string) => setRemoveFailedID(id),
    mutationFn: async (id: string) => {
      // The id rides the query string: the operation is a DELETE, which carries
      // no body, so the generated client takes it under `params.query`.
      const { error, response } = await api.DELETE("/ext/notes/remove", {
        params: { query: { id } },
      });
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    onSettled: invalidate,
  });

  if (!canRead) {
    return (
      <Card>
        <EmptyState>{t("extNotes.notes.noGrant")}</EmptyState>
      </Card>
    );
  }
  return (
    <Card>
      <SectionHeader title={t("extNotes.notes.title")} />
      {canAdd ? (
        <>
          <Field label={t("extNotes.notes.bodyLabel")}>
            {(control) => (
              <TextInput
                {...control}
                value={body}
                onChange={(event) => setBody(event.target.value)}
              />
            )}
          </Field>
          <Button
            disabled={body.trim() === "" || add.isPending}
            onClick={() => add.mutate(body)}
          >
            {t("extNotes.notes.add")}
          </Button>
          {add.isError ? <p>{t("extNotes.notes.addFailed")}</p> : null}
        </>
      ) : null}
      <QueryStates query={notes}>
        {notes.data?.length === 0 ? (
          <EmptyState>{t("extNotes.notes.empty")}</EmptyState>
        ) : (
          <ul>
            {notes.data?.map((item) => (
              <li key={item.id}>
                {formatDateTime(item.created_at, locale, zone)} — {item.body}
                {canRemove ? (
                  <Button
                    variant="ghost"
                    disabled={remove.isPending}
                    onClick={() => remove.mutate(item.id)}
                  >
                    {t("extNotes.notes.remove")}
                  </Button>
                ) : null}
                {removeFailedID === item.id ? (
                  <p>{t("extNotes.notes.removeFailed")}</p>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </QueryStates>
    </Card>
  );
}
