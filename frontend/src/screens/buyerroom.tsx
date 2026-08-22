import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Download, LogOut, Mail } from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, EmptyState, Field, TextInput } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Eyebrow } from "../design-system/eyebrow";
import { Panel, PanelBody } from "../design-system/panel";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryStates, throwProblem } from "./common";
import { DOCUMENT_GROUPS } from "./dealroomdocuments";
import { ThreadPanel } from "./dealroomthreads";
import "./buyerroom.css";

// The Deal Room as its BUYER sees it — the one screen an outside person ever
// reaches in this app. Anonymous: no seat, no cookie. The invitation link lands
// on `#/room?c=<credential>`; the credential is read once, scrubbed from the
// address bar, and exchanged for a room session the tab keeps in
// sessionStorage and presents as a Bearer on every call. A dead link, a paused
// room and an expired one each get their own honest screen and their own way
// back, and none of them names anything the link did not already name.

type BuyerRoomView = components["schemas"]["BuyerRoomView"];

const SESSION_KEY = "margince.room.session";
const ROOM_ROUTE = "room";

// credentialFromLocation reads `c` out of the hash's query and scrubs it —
// replaceState, not a hash assignment, so the credential does not come back
// through history. The identity module's reset link does the same.
function credentialFromLocation(): string | null {
  if (typeof globalThis.location === "undefined") {
    return null;
  }
  const hash = globalThis.location.hash.replace(/^#\/?/, "");
  if (hash.split("?")[0] !== ROOM_ROUTE || !hash.includes("?")) {
    return null;
  }
  const credential = new URLSearchParams(hash.slice(hash.indexOf("?") + 1)).get(
    "c",
  );
  if (credential) {
    globalThis.history?.replaceState?.(
      null,
      "",
      `${globalThis.location.pathname}${globalThis.location.search}#/${ROOM_ROUTE}`,
    );
  }
  return credential;
}

function readSession(): string | null {
  try {
    return globalThis.sessionStorage?.getItem(SESSION_KEY) ?? null;
  } catch {
    return null;
  }
}

function writeSession(token: string | null): void {
  try {
    if (token === null) {
      globalThis.sessionStorage?.removeItem(SESSION_KEY);
    } else {
      globalThis.sessionStorage?.setItem(SESSION_KEY, token);
    }
  } catch {
    // A browser refusing storage still gets this one page view: the token
    // lives in React state for the tab's lifetime and is simply not kept.
  }
}

function bearer(token: string): { headers: { Authorization: string } } {
  return { headers: { Authorization: `Bearer ${token}` } };
}

// The session stopped answering — revoked, lapsed, or signed out elsewhere.
class SessionRefusedError extends Error {}

// refuseOrThrow turns a failed public call into the right error: a 401 is the
// session ending (the caller retires it), anything else is the server's own
// explanation. Every buyer write goes through it so none can keep a dead token.
function refuseOrThrow(
  error: unknown,
  response: Response,
  t: ReturnType<typeof useT>,
): never {
  if (response.status === 401) {
    throw new SessionRefusedError();
  }
  throwProblem(error, t);
  throw new Error("unreachable");
}

function retireOnRefusal(onSessionLost: () => void) {
  return (error: unknown) => {
    if (error instanceof SessionRefusedError) {
      onSessionLost();
    }
  };
}

export function BuyerRoomScreen() {
  // Read once, at mount. The credential is gone from the address bar after
  // this, so a re-render must not look for it again.
  const [credential] = useState(credentialFromLocation);
  // A link in hand outranks a kept session from the first render: a tab that
  // still holds room A's session must not show room A for a breath while
  // room B's link is being exchanged.
  const [token, setToken] = useState(() => (credential ? null : readSession()));
  // What the exchange answered, held HERE rather than read off the mutation:
  // the replayed mount (StrictMode) swaps the observer that ran it for one that
  // never hears the result, so isSuccess/isError would stay false for ever.
  const [refusal, setRefusal] = useState<Error | null>(null);
  const t = useT();

  const exchange = useMutation({
    mutationKey: ["buyer-room-exchange"],
    mutationFn: async (raw: string) => {
      const { data, error, response } = await api.POST(
        "/public/rooms/exchange",
        { body: { credential: raw } },
      );
      if (error) {
        if (response.status === 404) {
          throw new SessionRefusedError();
        }
        throwProblem(error, t);
      }
      return data;
    },
  });

  // A fresh link outranks a kept session: the person clicked it on purpose.
  // Exchanged at most ONCE per mount, held in a ref rather than in state:
  // the credential is single-use, and an effect that runs twice (StrictMode
  // replays mount effects in development) would consume it on the first run
  // and be refused on the second, showing a dead-link page for a live link.
  //
  // The token is taken from the promise rather than from an onSuccess option:
  // the replayed mount unsubscribes the first observer, and an option callback
  // on an observer nobody listens to never runs.
  const exchangeAsync = exchange.mutateAsync;
  const exchanged = useRef(false);
  useEffect(() => {
    if (!credential || exchanged.current) {
      return;
    }
    exchanged.current = true;
    exchangeAsync(credential).then(
      (issued) => {
        if (issued) {
          writeSession(issued.session_token);
          setToken(issued.session_token);
        }
      },
      (error: unknown) => {
        setRefusal(error instanceof Error ? error : new Error(String(error)));
      },
    );
  }, [credential, exchangeAsync]);

  const signOut = () => {
    writeSession(null);
    setToken(null);
  };

  // "Opening" until the exchange has answered — not merely while the mutation
  // is in flight, because the first render happens before the effect fires it.
  if (credential && !token && !refusal) {
    return (
      <BuyerFrame>
        <EmptyState>
          <p className="t-small">{t("buyer.opening")}</p>
        </EmptyState>
      </BuyerFrame>
    );
  }
  if (credential && refusal) {
    return (
      <BuyerFrame>
        <DeadLink
          message={
            refusal instanceof SessionRefusedError
              ? t("buyer.linkDead")
              : problemMessageOf(refusal, t)
          }
        />
      </BuyerFrame>
    );
  }
  if (!token) {
    return (
      <BuyerFrame>
        <DeadLink message={t("buyer.noLink")} />
      </BuyerFrame>
    );
  }
  return (
    <BuyerFrame>
      <RoomBody token={token} onSessionLost={signOut} />
    </BuyerFrame>
  );
}

function BuyerFrame({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <div className="buyer-page">
      <div className="buyer-column">{children}</div>
    </div>
  );
}

// The link no longer admits anyone. Whatever the reason — used, lapsed,
// retired, never valid — the page says the same thing and offers the one
// recovery a buyer has: a fresh link to the address they were invited at.
function DeadLink({ message }: Readonly<{ message: string }>) {
  const t = useT();
  return (
    <Panel title={t("buyer.deadTitle")}>
      <PanelBody>
        <p>{message}</p>
      </PanelBody>
      <PanelBody>
        <LinkRequest />
      </PanelBody>
    </Panel>
  );
}

function LinkRequest() {
  const t = useT();
  const [email, setEmail] = useState("");
  const request = useMutation({
    mutationKey: ["buyer-room-link-request"],
    mutationFn: async (address: string) => {
      const { error } = await api.POST("/public/rooms/link-request", {
        body: { email: address },
      });
      if (error) {
        throwProblem(error, t);
      }
    },
  });
  if (request.isSuccess) {
    return (
      <Callout tone="success" live="status">
        {t("buyer.linkRequested")}
      </Callout>
    );
  }
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    request.mutate(email.trim());
  };
  return (
    <form className="buyer-linkrequest" onSubmit={submit}>
      <Field label={t("buyer.emailLabel")} hint={t("buyer.emailHint")}>
        {(field) => (
          <TextInput
            {...field}
            type="email"
            required
            value={email}
            onChange={(event) => setEmail(event.target.value)}
          />
        )}
      </Field>
      <Button
        type="submit"
        variant="primary"
        pending={request.isPending}
        disabled={email.trim() === ""}
      >
        <Mail aria-hidden />
        {t("buyer.requestLink")}
      </Button>
      {request.isError ? (
        <p className="t-small t-danger">{problemMessageOf(request.error, t)}</p>
      ) : null}
    </form>
  );
}

function useBuyerRoom(token: string, onSessionLost: () => void) {
  const t = useT();
  const query = useQuery({
    queryKey: ["buyer-room", token],
    retry: false,
    // Re-asked whenever the tab comes back: a revocation or a pause made while
    // the buyer was away must bind on their return, not on their next click.
    refetchOnWindowFocus: "always",
    queryFn: async () => {
      const { data, error, response } = await api.GET("/public/rooms/me", {
        ...bearer(token),
      });
      if (error) {
        if (response.status === 401) {
          throw new SessionRefusedError();
        }
        throwProblem(error, t);
      }
      return data;
    },
  });
  const lost = query.error instanceof SessionRefusedError;
  useEffect(() => {
    if (lost) {
      onSessionLost();
    }
  }, [lost, onSessionLost]);
  return query;
}

function RoomBody({
  token,
  onSessionLost,
}: Readonly<{ token: string; onSessionLost: () => void }>) {
  const t = useT();
  const room = useBuyerRoom(token, onSessionLost);
  const signOut = useMutation({
    mutationKey: ["buyer-room-sign-out"],
    mutationFn: async (session: string) => {
      const { error } = await api.POST("/public/rooms/sign-out", {
        ...bearer(session),
      });
      if (error) {
        throwProblem(error, t);
      }
    },
    // Whatever the server said, this tab is done with the token.
    onSettled: onSessionLost,
  });
  return (
    <QueryStates query={room} pendingLines={4}>
      {room.data ? (
        <>
          <RoomView
            view={room.data}
            token={token}
            onSessionLost={onSessionLost}
          />
          <div className="buyer-foot">
            <Button
              variant="ghost"
              pending={signOut.isPending}
              onClick={() => signOut.mutate(token)}
            >
              <LogOut aria-hidden />
              {t("buyer.signOut")}
            </Button>
          </div>
        </>
      ) : null}
    </QueryStates>
  );
}

type BuyerRoomDocument = components["schemas"]["BuyerRoomDocument"];

function BuyerDocuments({
  token,
  onSessionLost,
  reviewer,
  live,
}: Readonly<{
  token: string;
  onSessionLost: () => void;
  reviewer: boolean;
  live: boolean;
}>) {
  const t = useT();
  const docs = useQuery({
    queryKey: ["buyer-room-documents", token],
    retry: false,
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/public/rooms/documents",
        { ...bearer(token) },
      );
      if (error) {
        if (response.status === 401) {
          throw new SessionRefusedError();
        }
        throwProblem(error, t);
      }
      return data;
    },
  });
  const lost = docs.error instanceof SessionRefusedError;
  useEffect(() => {
    if (lost) {
      onSessionLost();
    }
  }, [lost, onSessionLost]);
  return (
    <Panel title={t("buyer.docs.title")} sub={t("buyer.docs.sub")}>
      <QueryStates query={docs} pendingLines={3}>
        {docs.data ? (
          docs.data.data.length === 0 ? (
            <PanelBody>
              <EmptyState>
                <p className="t-small">{t("buyer.docs.empty")}</p>
              </EmptyState>
            </PanelBody>
          ) : (
            DOCUMENT_GROUPS.map((group) => {
              const inGroup = docs.data.data.filter(
                (d) => d.group_key === group.key,
              );
              if (inGroup.length === 0) {
                return null;
              }
              return (
                <PanelBody key={group.key}>
                  <Eyebrow as="h3">{t(group.labelKey)}</Eyebrow>
                  {inGroup.map((doc) => (
                    <BuyerDocumentRow
                      key={doc.id}
                      token={token}
                      doc={doc}
                      mayDecide={reviewer && live}
                      onSessionLost={onSessionLost}
                    />
                  ))}
                </PanelBody>
              );
            })
          )
        ) : null}
      </QueryStates>
    </Panel>
  );
}

// The download carries the Bearer, which a plain link cannot, so it is a
// fetch whose bytes are handed to the browser as an object URL. The credential
// never lands in a URL this way either.
function BuyerDocumentRow({
  token,
  doc,
  mayDecide,
  onSessionLost,
}: Readonly<{
  token: string;
  doc: BuyerRoomDocument;
  mayDecide: boolean;
  onSessionLost: () => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const decide = useMutation({
    mutationKey: ["buyer-room-document-decide"],
    mutationFn: async (input: { documentId: string; kind: string }) => {
      const { data, error, response } = await api.POST(
        "/public/rooms/documents/{documentId}/decision",
        {
          params: { path: { documentId: input.documentId } },
          body: { kind: input.kind },
          ...bearer(token),
        },
      );
      if (error) {
        refuseOrThrow(error, response, t);
      }
      return data;
    },
    onError: retireOnRefusal(onSessionLost),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["buyer-room-threads", token],
      });
    },
  });
  const download = useMutation({
    mutationKey: ["buyer-room-document-download"],
    mutationFn: async (input: { documentId: string; filename: string }) => {
      const { data, error, response } = await api.GET(
        "/public/rooms/documents/{documentId}/file",
        {
          params: { path: { documentId: input.documentId } },
          parseAs: "blob",
          ...bearer(token),
        },
      );
      if (error || !data) {
        throw new Error(t("buyer.docs.downloadFailed"), {
          cause: response.status,
        });
      }
      const url = URL.createObjectURL(data);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = input.filename;
      anchor.click();
      URL.revokeObjectURL(url);
    },
  });
  return (
    <div className="room-doc buyer-docs-group">
      <div>
        <p>{doc.title}</p>
        <p className="t-small">{doc.filename}</p>
      </div>
      <Button
        small
        aria-label={t("buyer.docs.download", { title: doc.title })}
        pending={download.isPending}
        onClick={() =>
          download.mutate({ documentId: doc.id, filename: doc.filename })
        }
      >
        <Download aria-hidden />
      </Button>
      {download.isError ? (
        <p className="t-small t-danger">{download.error.message}</p>
      ) : null}
      {mayDecide ? (
        <div className="buyer-decide">
          {decide.isSuccess ? (
            <p className="t-small">
              {t(
                decide.data?.kind === "confirm_version"
                  ? "buyer.decide.confirmed"
                  : "buyer.decide.requested",
              )}
            </p>
          ) : (
            <>
              <Button
                small
                variant="ghost"
                pending={decide.isPending}
                onClick={() =>
                  decide.mutate({ documentId: doc.id, kind: "request_changes" })
                }
              >
                {t("buyer.decide.requestChanges")}
              </Button>
              <Button
                small
                variant="primary"
                pending={decide.isPending}
                onClick={() =>
                  decide.mutate({ documentId: doc.id, kind: "confirm_version" })
                }
              >
                {t("buyer.decide.confirm")}
              </Button>
            </>
          )}
          {decide.isError ? (
            <p className="t-small t-danger">
              {problemMessageOf(decide.error, t)}
            </p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function BuyerConversation({
  token,
  onSessionLost,
  mayWrite,
  documents,
  refusal,
}: Readonly<{
  token: string;
  onSessionLost: () => void;
  mayWrite: boolean;
  documents: readonly { id: string; title: string }[];
  refusal: string | undefined;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const threads = useQuery({
    queryKey: ["buyer-room-threads", token],
    retry: false,
    queryFn: async () => {
      const { data, error, response } = await api.GET("/public/rooms/threads", {
        ...bearer(token),
      });
      if (error) {
        if (response.status === 401) {
          throw new SessionRefusedError();
        }
        throwProblem(error, t);
      }
      return data;
    },
  });
  const lost = threads.error instanceof SessionRefusedError;
  useEffect(() => {
    if (lost) {
      onSessionLost();
    }
  }, [lost, onSessionLost]);
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: ["buyer-room-threads", token] });
  const open = useMutation({
    mutationKey: ["buyer-room-thread-open"],
    mutationFn: async (input: {
      documentId: string | null;
      body: string;
      requiredChange: boolean;
    }) => {
      const { data, error, response } = await api.POST(
        "/public/rooms/threads",
        {
          body: {
            document_id: input.documentId,
            body: input.body,
            required_change: input.requiredChange,
          },
          ...bearer(token),
        },
      );
      if (error) {
        refuseOrThrow(error, response, t);
      }
      return data;
    },
    onError: retireOnRefusal(onSessionLost),
    onSuccess: refresh,
  });
  const reply = useMutation({
    mutationKey: ["buyer-room-thread-reply"],
    mutationFn: async (input: { threadId: string; body: string }) => {
      const { data, error, response } = await api.POST(
        "/public/rooms/threads/{threadId}/comments",
        {
          params: { path: { threadId: input.threadId } },
          body: { body: input.body },
          ...bearer(token),
        },
      );
      if (error) {
        refuseOrThrow(error, response, t);
      }
      return data;
    },
    onError: retireOnRefusal(onSessionLost),
    onSuccess: refresh,
  });
  const titles = Object.fromEntries(documents.map((d) => [d.id, d.title]));
  return (
    <QueryStates query={threads} pendingLines={3}>
      {threads.data ? (
        <ThreadPanel
          threads={threads.data.data}
          documentTitles={titles}
          verbs={{
            documents,
            mayRequireChange: true,
            refusal,
            open: mayWrite ? (input) => open.mutateAsync(input) : undefined,
            reply: mayWrite
              ? (threadId, body) => reply.mutateAsync({ threadId, body })
              : undefined,
          }}
        />
      ) : null}
    </QueryStates>
  );
}

// The conversation needs the published document titles to label a thread;
// they come from the same query the documents panel runs, so React Query
// serves both from one request.
function BuyerConversationWithDocuments({
  token,
  onSessionLost,
  mayWrite,
  refusal,
}: Readonly<{
  token: string;
  onSessionLost: () => void;
  mayWrite: boolean;
  refusal: string | undefined;
}>) {
  const t = useT();
  const docs = useQuery({
    queryKey: ["buyer-room-documents", token],
    retry: false,
    queryFn: async () => {
      const { data, error } = await api.GET("/public/rooms/documents", {
        ...bearer(token),
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
  });
  const documents = (docs.data?.data ?? []).map((d) => ({
    id: d.id,
    title: d.title,
  }));
  return (
    <BuyerConversation
      token={token}
      onSessionLost={onSessionLost}
      mayWrite={mayWrite}
      documents={documents}
      refusal={refusal}
    />
  );
}

const ACCESS_TITLE: Record<string, MessageKey> = {
  paused: "buyer.pausedTitle",
  expired: "buyer.expiredTitle",
};

function RoomView({
  view,
  token,
  onSessionLost,
}: Readonly<{
  view: BuyerRoomView;
  token: string;
  onSessionLost: () => void;
}>) {
  const t = useT();
  const steward = view.steward_name ?? t("buyer.stewardUnknown");
  if (view.access === "paused" || view.access === "expired") {
    return (
      <Panel title={t(ACCESS_TITLE[view.access])}>
        <PanelBody>
          <p>
            {t(
              view.access === "paused"
                ? "buyer.pausedBody"
                : "buyer.expiredBody",
              { steward },
            )}
          </p>
        </PanelBody>
        {view.access === "expired" ? (
          <PanelBody>
            <LinkRequest />
          </PanelBody>
        ) : null}
      </Panel>
    );
  }
  if (!view.room) {
    return (
      <Panel title={t("buyer.notYetTitle")}>
        <PanelBody>
          <p>{t("buyer.notYetBody", { steward })}</p>
        </PanelBody>
      </Panel>
    );
  }
  return (
    <>
      {view.preview ? (
        <Callout tone="info">{t("buyer.previewBanner")}</Callout>
      ) : null}
      <header className="buyer-header">
        <Eyebrow as="span">{t("buyer.eyebrow")}</Eyebrow>
        <h1>{view.room.title}</h1>
        {view.room.welcome_message ? <p>{view.room.welcome_message}</p> : null}
        <p className="t-small buyer-meta">
          {t("buyer.contact", { steward })}
          {view.access === "closed" ? ` ${t("buyer.closedNote")}` : ""}
        </p>
      </header>
      <BuyerDocuments
        token={token}
        onSessionLost={onSessionLost}
        reviewer={view.participant.capability === "reviewer"}
        live={view.access === "live"}
      />
      <BuyerConversationWithDocuments
        token={token}
        onSessionLost={onSessionLost}
        mayWrite={
          view.access === "live" && view.participant.capability !== "view"
        }
        refusal={
          view.preview
            ? t("buyer.previewReadOnly")
            : view.access === "closed"
              ? t("buyer.closed")
              : view.participant.capability === "view"
                ? t("threads.readOnly")
                : undefined
        }
      />
    </>
  );
}
