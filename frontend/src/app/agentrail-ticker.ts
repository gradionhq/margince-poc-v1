// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { NAMED, SAID, WROTE } from "./agentrail-copy";

/**
 * The line under the orb, one item at a time.
 *
 * The agent's work is worth watching, and the way to make it worth watching is
 * to name each PIECE of it as it happens rather than to report a total. A bar
 * that says "reading 128 captured items" and then sits still for thirty seconds
 * reads as a hang. The same thirty seconds spent naming each item as it arrives
 * reads as speed, because a reader counts events and not seconds.
 *
 * TWO PROPERTIES ARE NOT NEGOTIABLE.
 *
 * It is THIS READER'S work and nobody else's. Everything here comes out of the
 * query cache of the tab it is running in, so it cannot report what another
 * person's session is doing even by accident. That is not a side effect of the
 * implementation, it is the reason for it: a status line that quietly narrated a
 * colleague's afternoon would be surveillance wearing a status light.
 *
 * It never invents a line. A cache key with no entry in `SAID` produces NOTHING
 * rather than a guess spelled out of its own key: "reading person-360" is not
 * language, and a surface whose job is to be believed cannot afford a sentence
 * nobody wrote.
 *
 * WHAT IT CANNOT SEE, and the reason is worth writing down. The overnight run:
 * it happens in the worker, hours before this tab existed, and the contract
 * serves no run-progress read and no stream, so nothing about it reaches a
 * browser. A write with no `mutationKey` is invisible for a much smaller reason,
 * which is that the cache cannot say which write it is. Both gaps show as the
 * state's own resting line, never as a fabricated event.
 */

/** One named thing the tool is doing, as the reader would say it. */
export type TickerLine = Readonly<{
  /** Distinct per event, so a repeated phrase still animates as a new line. */
  id: number;
  said: string;
}>;

/**
 * How long a line stays after its read settled.
 *
 * Short reads are the common case and they are the ones worth showing, so a line
 * has to outlive its own request or the fast ones would flicker past unread. Long
 * enough to read four words, short enough that the line is never stale.
 */
const LINGER_MS = 900;

/** The most lines held at once. Beyond this the oldest is dropped unread. */
const DEPTH = 4;

/** Returned for a cache event that is not a read starting or answering. */
const SKIP = Symbol("skip");

/**
 * The data a cache event carries, or `SKIP` when the event is not one of the two
 * moments a line can be drawn from.
 */
function settledData(action: { type: string }): unknown {
  if (action.type === "fetch") {
    return undefined;
  }
  if (action.type === "success" && "data" in action) {
    return (action as { data: unknown }).data;
  }
  return SKIP;
}

/** The first segment of a cache key, which is the only part that names a thing. */
function headOf(key: readonly unknown[]): string | null {
  const head = key[0];
  return typeof head === "string" ? head : null;
}

/** The id in a record key, which is what a name can be looked up against. */
function idOf(key: readonly unknown[]): string | null {
  const id = key[1];
  return typeof id === "string" && id.length > 0 ? id : null;
}

/**
 * The name fields a record answers with, in the order a reader would say them.
 *
 * Written out rather than "the first string property": a record carries a dozen
 * strings, and picking one by position would eventually print a domain, a
 * lifecycle stage or an id into the status line.
 */
const NAME_FIELDS = ["display_name", "full_name", "name", "title"] as const;

function nameIn(value: unknown): string | null {
  if (typeof value !== "object" || value === null) {
    return null;
  }
  const row = value as Record<string, unknown>;
  for (const field of NAME_FIELDS) {
    const found = row[field];
    if (typeof found === "string" && found.trim().length > 0) {
      return found;
    }
  }
  return null;
}

/** The rows a list read answers with, if this is one. */
function rowsIn(value: unknown): readonly unknown[] {
  if (typeof value !== "object" || value === null) {
    return [];
  }
  const held = (value as Record<string, unknown>).data;
  return Array.isArray(held) ? held : [];
}

/**
 * What the thing being read is CALLED, from what this tab already knows.
 *
 * Never a request of its own: a status line that fetched in order to describe a
 * fetch would double every read the reader made, and the name is almost always
 * already here anyway, because a reader arrives at a record from a list that
 * carried its name. Three places, cheapest first: the record's own cached
 * answer, the entity-name cache the record header fills, and the list reads.
 *
 * Null when nothing knows it yet, and the caller then says the unnamed line
 * rather than an id.
 */
function nameFor(client: QueryClient, id: string): string | null {
  for (const query of client.getQueryCache().getAll()) {
    const found = nameInQuery(query.queryKey, query.state.data, id);
    if (found !== null) {
      return found;
    }
  }
  return null;
}

/** What one cached answer knows about this id, in the three shapes it can be. */
function nameInQuery(
  key: readonly unknown[],
  data: unknown,
  id: string,
): string | null {
  if (data === undefined) {
    return null;
  }
  // The record's own answer, under any key that holds one for this id.
  if (idOf(key) === id) {
    const direct = nameIn(data);
    if (direct !== null) {
      return direct;
    }
  }
  // `useEntityName`'s cache, which exists to answer exactly this question.
  if (key[1] === "ref" && key[2] === id && typeof data === "string") {
    return data;
  }
  return nameInRows(rowsIn(data), id);
}

/** The row a list read already carried, which is usually where the name is. */
function nameInRows(rows: readonly unknown[], id: string): string | null {
  for (const row of rows) {
    if (typeof row !== "object" || row === null) {
      continue;
    }
    if ((row as Record<string, unknown>).id === id) {
      return nameIn(row);
    }
  }
  return null;
}

/**
 * What to say about a read, or nothing at all.
 *
 * Named when this tab can name the record: "Reading zenloop" is the whole point
 * of the line, and the unnamed phrase is what it falls back to rather than
 * printing an id at somebody. A key in neither table says nothing, which is how
 * the plumbing reads stay out of the line.
 */
function lineFor(
  client: QueryClient,
  key: readonly unknown[],
  settled: unknown,
): string | null {
  const head = headOf(key);
  if (head === null) {
    return null;
  }
  const template = NAMED[head];
  if (template === undefined) {
    // A list, a queue, a catalog: there is no one record to name, so the phrase
    // is the whole line.
    return SAID[head] ?? null;
  }
  // A single record, and the NAME is the line. There is no unnamed version of
  // this sentence: "Reading a contact" tells a reader nothing they cannot see
  // from the page they are standing on, and printing it while the name is one
  // moment away is worse than waiting that moment. So a record read says
  // nothing until it can say who.
  const id = idOf(key);
  const name = nameIn(settled) ?? (id === null ? null : nameFor(client, id));
  return name === null ? null : template.replace("%s", name);
}

/**
 * What to say about a write.
 *
 * A write is the interesting half: it is the tool doing something rather than
 * looking something up, and it is the half a reader is actually waiting on. Same
 * tables, same refusal to invent: a key that is not in `WROTE` says nothing.
 */
function wroteFor(
  client: QueryClient,
  key: readonly unknown[] | undefined,
): string | null {
  if (key === undefined) {
    return null;
  }
  const head = headOf(key);
  const phrases = head === null ? undefined : WROTE[head];
  if (phrases === undefined) {
    return null;
  }
  const id = idOf(key);
  if (id === null) {
    // Nothing to name: the write is not about one record, so the plain phrase IS
    // the whole truth about it.
    return phrases[1];
  }
  const name = nameFor(client, id);
  return name === null ? phrases[1] : phrases[0].replace("%s", name);
}

/**
 * Watches this tab's reads and writes and reports the named ones, newest first.
 *
 * Subscribed to the cache rather than polling: a fetch that starts is an event
 * the cache already publishes, and a poll would either miss the short reads
 * entirely or run all day to catch them.
 */
export function useAgentTicker(): readonly TickerLine[] {
  const client = useQueryClient();
  const [lines, setLines] = useState<readonly TickerLine[]>([]);

  useEffect(() => {
    let next = 0;
    const timers = new Set<ReturnType<typeof setTimeout>>();
    // What has already been said, so one read is one line however many times the
    // cache publishes about it.
    const announced = new Map<string, boolean>();

    const push = (said: string) => {
      const id = ++next;
      setLines((current) => [{ id, said }, ...current].slice(0, DEPTH));
      const timer = setTimeout(() => {
        timers.delete(timer);
        setLines((current) => current.filter((line) => line.id !== id));
      }, LINGER_MS);
      timers.add(timer);
    };

    // One read is one line however many times the cache publishes about it: the
    // same phrase inside its own lifetime is the same event.
    const sayOnce = (said: string, key: readonly unknown[]) => {
      const stamp = `${JSON.stringify(key)}|${said}`;
      if (announced.get(stamp) === true) {
        return;
      }
      announced.set(stamp, true);
      const timer = setTimeout(() => {
        timers.delete(timer);
        announced.delete(stamp);
      }, LINGER_MS * 2);
      timers.add(timer);
      push(said);
    };

    const stop = client.getQueryCache().subscribe((event) => {
      // Two moments, and which one carries the line depends on whether the tab
      // can name the record yet. A read the reader arrived at from a list is
      // named the instant it STARTS, because the list already carried the name.
      // A read of something nothing has seen before can only be named once it
      // ANSWERS, and that is what keeps the unnamed sentence out of the line.
      if (event.type !== "updated") {
        return;
      }
      const settled = settledData(event.action);
      if (settled === SKIP) {
        return;
      }
      const key = event.query.queryKey;
      const said = lineFor(client, key, settled);
      if (said !== null) {
        sayOnce(said, key);
      }
    });

    // Writes announce themselves the same way, and they are the events a reader
    // is actually waiting on: a read is the tool looking something up, a write is
    // the tool doing the thing they asked for.
    const stopWrites = client.getMutationCache().subscribe((event) => {
      if (
        event.type !== "updated" ||
        event.mutation.state.status !== "pending"
      ) {
        return;
      }
      // Through `sayOnce`, not `push`: the cache publishes an `updated` event
      // for `pause`, `continue` and `failed` while the mutation is still
      // pending, so one write that retries would otherwise announce itself
      // several times over.
      const key = event.mutation.options.mutationKey;
      const said = wroteFor(client, key);
      if (said !== null && key !== undefined) {
        sayOnce(said, key);
      }
    });

    return () => {
      announced.clear();
      stop();
      stopWrites();
      for (const timer of timers) {
        clearTimeout(timer);
      }
    };
  }, [client]);

  return lines;
}
