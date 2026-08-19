import { api, throwProblem } from "@margince/frontend/api";
import { useQuery } from "@tanstack/react-query";

// What both cards on this screen need to know about the member's connection, and
// nothing either card owns alone.
//
// It exists for ONE reason above the others: both cards decide "is there an
// account here", and they have to agree. The connection card gates the withdraw
// verb on it and the capture card gates its whole existence on it, and two copies
// of that judgement are two chances to disagree — with the `!= null` null-safety
// below being exactly the sort of detail one copy grows and the other does not.
//
// The status READ lives here for the same reason: one query key, so the two cards
// share a single request rather than polling the same row twice, and a save in
// one card invalidates the read the other renders from.
//
// WHAT DOES NOT BELONG HERE: anything only one card uses. The roster read, the
// capture vocabulary and the QR handshake all stayed with their own lane, and a
// module that starts collecting them has stopped being a seam.

/** The RBAC object every operation on this screen gates on. */
export const CONNECTION_OBJECT = "ext_zalo_personal_connection";

export const STATUS_KEY = ["ext", "zalo-personal", "status"];

/**
 * How often the connection status re-reads while the screen is open.
 *
 * The row it reads changes when a capture run finds a dead session, which is
 * minutes apart at best; faster would spend requests on a fact that does not
 * move.
 */
const STATUS_POLL_MS = 20_000;

/**
 * `enabled` is the caller's read grant rather than a convenience: without it an
 * ungranted seat fires a request the server answers 403 — and then fires it
 * again every {@link STATUS_POLL_MS}, because this query polls. What that seat
 * should see is "you were not granted this", not a failed read on a timer.
 */
export function useConnectionStatus(enabled: boolean) {
  return useQuery({
    enabled,
    refetchInterval: STATUS_POLL_MS,
    queryKey: STATUS_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/ext/zalo-personal/status",
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      // The declared field or an error. `data.connected` absent is undefined,
      // which is falsey — so a body this screen could not read would render
      // "not connected", which is a claim about the member's own account made
      // from a read that produced nothing, and what it invites is a second scan
      // over a connection that is already working.
      if (typeof data?.connected !== "boolean") {
        throw new Error("the connection status carried no `connected` field");
      }
      // `session_deposited` is judged as strictly, and for a sharper reason: it
      // is how this screen learns that a credential is on deposit with no
      // usable connection behind it. Read as false when the body did not carry
      // it, a member with a STRANDED session would be shown "not connected" and
      // offered no way to withdraw the access this installation is holding —
      // which is the one state where being wrong costs them their privacy
      // rather than a click.
      if (typeof data.session_deposited !== "boolean") {
        throw new Error(
          "the connection status carried no `session_deposited` field",
        );
      }
      // The count is judged for its TYPE and not for its presence, which is the
      // one place this read is deliberately less strict than the two above.
      // Absent, it is a fact the server did not report and the chooser draws no
      // row for it; defaulted to zero it would be a claim — "nothing of yours is
      // armed" — made from a read that established nothing, and a member who
      // believes it goes looking for the choices they already saved. A count
      // that arrives as something other than a number is a malformed body, and
      // failing the read is how that gets noticed rather than rendered.
      // Read through `unknown` on purpose: the contract makes the field
      // required, so the generated type says `number` and nothing here would
      // narrow — while a real body can still arrive without it, and this is the
      // one field whose absence the screen survives.
      const count: unknown = data.allowed_count;
      if (count !== undefined && typeof count !== "number") {
        throw new Error("the connection status carried a non-numeric count");
      }
      return {
        connected: data.connected,
        sessionDeposited: data.session_deposited,
        allowedCount: count,
        connection: data.connection,
      };
    },
  });
}

/**
 * One member's connection, as the MERGED contract declares it.
 *
 * DERIVED from the read rather than restated. A hand-written copy of the schema
 * is a second spelling that drifts in silence — a renamed field still compiles
 * against the copy and renders undefined — and it invites exactly what the
 * first draft of this file did: declaring `last_polled_at` because the contract
 * has one, while nothing on the screen ever read it.
 */
export type Connection = NonNullable<
  NonNullable<ReturnType<typeof useConnectionStatus>["data"]>["connection"]
>;

/**
 * Whether this installation holds a connection worth acting on.
 *
 * `!= null` rather than `!== undefined`, and it is the difference between a
 * screen that reports and a screen that dies: a body carrying an explicit
 * `"connection": null` passes an undefined-only guard, and `.status` then throws
 * mid-render — so the WHOLE card fails, taking the withdraw verb with it, in a
 * state whose honest reading is simply "not connected". The handler omits the
 * field today; resting on that is a producer detail, not a property, and the
 * rest of the read validates the body rather than trusting it.
 *
 * Shared by both cards rather than spelled twice: the chooser and the withdraw
 * verb have to agree about what "there is an account here" means, and two copies
 * of that judgement are two chances to disagree.
 */
export function isHeld(
  connection: Connection | null | undefined,
): connection is Connection {
  return connection != null && connection.status !== "disconnected";
}
