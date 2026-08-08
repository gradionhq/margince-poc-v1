import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../../api/client";
import { useCan, useCanWrite } from "../../app/capability";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  SectionHeader,
  TextInput,
} from "../../design-system/atoms";
import { formatDateTime } from "../../format/format";
import { useLocale, useT } from "../../i18n";
import { QueryStates, throwProblem } from "../common";

// #/ext/crm-demo — "Demo Notepad", the reference extension's one screen.
//
// It lives in the CORE tree, not in extensions/crm-demo/frontend/. That layer
// is still refused on sight by gen-composition's scan, and lifting the refusal
// means bundling unit-authored TSX into the SPA — a supply-chain decision with
// its own reviewed slice. So the unit ships its contract, its SQL and its Go,
// and the screen that drives them is committed here, dispatched by unit name
// from App.tsx's ExtensionRoute through the composed screen registry
// (src/screens/ext/index.tsx).
//
// TWO consequences of that, both of which the UAT depends on:
//
//   - This file is in the COMPOSED lane only. `api.POST("/ext/crm-demo/…")` is
//     correctly a type error in the vanilla lane — that installation does not
//     serve the route — so tsconfig.app.json excludes src/screens/ext/ and
//     tsconfig.composed.json compiles it against the MERGED contract. If the
//     overlay did not merge, this file does not typecheck: the route half of
//     the ordering constraint is still a compile-time guard, even though the
//     RBAC-object half is not (see capability.ts).
//   - `rm -rf extensions/crm-demo && make composition` still reproduces the
//     vanilla stub byte for byte, because nothing here is generated. The
//     registry goes empty, the dispatch misses, and #/ext/crm-demo answers the
//     ordinary not-found card.
//
// The shared `api` client, no second client and no cast: contract paths are
// unprefixed and client.ts's baseUrl carries the /v1 mount, so an extension
// route reads exactly like a core one at the call site.
//
// Every operation is a POST because a served extension operation IS a governed
// tool invocation and its arguments are the request body — the seam admits no
// GET or DELETE. "list", "add" and "remove" are three verbs, not three methods.

/** The RBAC object the unit's record operations gate on. */
const NOTE_OBJECT = "ext_crm_demo_note";

/** One poll interval for the heartbeat, in milliseconds. */
const HEARTBEAT_POLL_MS = 15_000;

export function CrmDemoScreen() {
  const t = useT();
  return (
    <div className="wrap narrow">
      <SectionHeader title={t("extDemo.title")} sub={t("extDemo.sub")} />
      <SigningCard />
      <NotesCard />
    </div>
  );
}

// ── secrets ──────────────────────────────────────────────────────────────────

function useSigningKeyStatus() {
  return useQuery({
    queryKey: ["ext", "crm-demo", "signing-key"],
    queryFn: async () => {
      const { data, error, response } = await api.POST(
        "/ext/crm-demo/signing-key/status",
        { body: {} },
      );
      if (error || !response.ok) {
        throwProblem(error);
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
  const status = useSigningKeyStatus();
  const [key, setKey] = useState("");
  const [payload, setPayload] = useState("");
  const [signature, setSignature] = useState("");

  const store = useMutation({
    mutationFn: async (value: string) => {
      const { error, response } = await api.POST("/ext/crm-demo/signing-key", {
        body: { key: value },
      });
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    onSuccess: async () => {
      setKey("");
      await queryClient.invalidateQueries({
        queryKey: ["ext", "crm-demo", "signing-key"],
      });
    },
  });

  const sign = useMutation({
    mutationFn: async (value: string) => {
      const { data, error, response } = await api.POST(
        "/ext/crm-demo/signature",
        { body: { payload: value } },
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => setSignature(`${data.algorithm} ${data.signature}`),
  });

  return (
    <Card>
      <SectionHeader
        title={t("extDemo.signing.title")}
        sub={t("extDemo.signing.sub")}
      />
      <p>
        {status.data ? (
          <Badge tone="success">{t("extDemo.signing.connected")}</Badge>
        ) : (
          <Badge tone="warn">{t("extDemo.signing.notConnected")}</Badge>
        )}
      </p>
      <Field label={t("extDemo.signing.keyLabel")}>
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
        {t("extDemo.signing.store")}
      </Button>
      <Field label={t("extDemo.signing.payloadLabel")}>
        {(control) => (
          <TextInput
            {...control}
            value={payload}
            onChange={(event) => setPayload(event.target.value)}
          />
        )}
      </Field>
      <Button
        disabled={payload === "" || sign.isPending}
        onClick={() => sign.mutate(payload)}
      >
        {t("extDemo.signing.sign")}
      </Button>
      {signature === "" ? null : <p>{signature}</p>}
      {sign.isError ? <p>{t("extDemo.signing.signFailed")}</p> : null}
    </Card>
  );
}

// ── migrations + api + jobs ──────────────────────────────────────────────────

function useNotes() {
  return useQuery({
    queryKey: ["ext", "crm-demo", "notes"],
    queryFn: async () => {
      const { data, error, response } = await api.POST(
        "/ext/crm-demo/notes/list",
        { body: {} },
      );
      if (error || !response.ok) {
        throwProblem(error);
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

/**
 * The notepad: the list, the Add control, and per-row removal.
 *
 * `Add` and the row's remove control are gated on the unit's OWN RBAC object,
 * registered into the vocabulary at boot from the fragment's `x-rbac-object`
 * and reported in /me. This is UX honesty, never enforcement — the server's
 * gate is the authority — but it is also the observable half of the demo's
 * read-only-seat step: the list renders, the controls do not.
 */
function NotesCard() {
  const t = useT();
  const { locale } = useLocale();
  // The reader's own zone: a note's timestamp is only useful next to the clock
  // on the wall behind them, and nothing about a demo note belongs to a
  // workspace-configured zone.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const queryClient = useQueryClient();
  const notes = useNotes();
  const canAdd = useCanWrite(NOTE_OBJECT, "create");
  const canRemove = useCanWrite(NOTE_OBJECT, "delete");
  // Not folded into canAdd: the read grant is what decides whether the card
  // has anything to say at all, and a seat that may read but not write must
  // still see the list.
  const canRead = useCan(NOTE_OBJECT, "read");
  const [body, setBody] = useState("");

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["ext", "crm-demo", "notes"] });

  const add = useMutation({
    mutationFn: async (value: string) => {
      const { error, response } = await api.POST("/ext/crm-demo/notes/add", {
        body: { body: value },
      });
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    onSuccess: async () => {
      setBody("");
      await invalidate();
    },
  });

  const remove = useMutation({
    mutationFn: async (id: string) => {
      const { error, response } = await api.POST("/ext/crm-demo/notes/remove", {
        body: { id },
      });
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    onSuccess: invalidate,
  });

  if (!canRead) {
    return (
      <Card>
        <EmptyState>{t("extDemo.notes.noGrant")}</EmptyState>
      </Card>
    );
  }
  return (
    <Card>
      <SectionHeader title={t("extDemo.notes.title")} />
      {canAdd ? (
        <>
          <Field label={t("extDemo.notes.bodyLabel")}>
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
            {t("extDemo.notes.add")}
          </Button>
        </>
      ) : null}
      <QueryStates query={notes}>
        {notes.data?.length === 0 ? (
          <EmptyState>{t("extDemo.notes.empty")}</EmptyState>
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
                    {t("extDemo.notes.remove")}
                  </Button>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </QueryStates>
    </Card>
  );
}
