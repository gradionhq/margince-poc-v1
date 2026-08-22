import { useQueries, useQuery } from "@tanstack/react-query";
import { CornerDownLeft, Search, Sparkles } from "lucide-react";
import { useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api/client";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  settingsAddress,
  useSettingsEntryVisibility,
} from "../screens/settings";
import { ENTITY, ENTITY_KINDS, type EntityKind } from "./entity";
import { NAV } from "./nav";
import { navigate, type Route } from "./router";

// ⌘K command palette (B-EP09.5, AC-shell-3..7). The command set carries a
// type tag (screen / action / record); record entries are fed by the search
// seam once the data layer lands — the tagging and ranking mechanics are
// already here. The "Ask AI: …" run-as-NL row is always appended last.

export type Command = {
  id: string;
  label: string;
  subtitle?: string;
  // Extra terms the row answers to but does not display. A nav label is a
  // presentation choice and the domain vocabulary outlives it: Pipeline is still
  // the place deals live, Approvals is still the inbox, and someone typing the
  // older word must not be told the screen does not exist.
  keywords?: readonly string[];
  type: "screen" | "action" | "record";
  route: Route;
};

// The settings entries this palette offers a shortcut to. Narrower than the full
// entry set on purpose — only these two lost an address of their own.
type AdminEntry = "data-model" | "ai";

export function useBuiltinCommands(): Command[] {
  const t = useT();
  // Without the company rollout probe: the palette offers no shortcut to General,
  // and that probe is a network read this hook would otherwise fire on every
  // screen (see useSettingsEntryVisibility).
  const visible = useSettingsEntryVisibility(false);
  return useMemo(() => {
    const screens: Command[] = NAV.map((item) => ({
      id: `screen:${item.screen}`,
      label: t(item.labelKey),
      // The route id is the screen's stable English name and doubles as its
      // alias, so a relabeled destination stays findable under both words in
      // both locales without a hand-kept synonym list.
      keywords: [item.screen],
      type: "screen",
      route: { screen: item.screen },
    }));
    const actions: Command[] = [
      {
        id: "action:new-deal",
        label: t("action.newDeal"),
        type: "action",
        route: { screen: "deals", id: "new" },
      },
      {
        id: "action:read-company",
        label: t("action.readCompany"),
        type: "action",
        route: { screen: "onboarding", id: "company" },
      },
      {
        id: "action:booking",
        label: t("action.booking"),
        type: "action",
        route: { screen: "book" },
      },
    ];
    // Settings surfaces that are not rail destinations, added explicitly so ⌘K
    // still reaches them. Each carries the words it USED to be addressed by:
    // three screens of their own collapsed into the data-model page and the
    // automations editor into the AI page, and somebody who learned "custom
    // fields" or "automations" must not be told the product no longer has one.
    //
    // Gated on the SAME predicates the settings level uses, because the settings
    // screen falls back to Account for an entry the principal may not open — so an
    // ungated command here would be a shortcut that silently goes somewhere else.
    // Deliberately still not the whole level: only these two lost their own
    // address, and the rest are reachable by the rail door beside them.
    // Each carries the entry it opens, so the gate below reads the same answer the
    // settings level does rather than a second copy of the predicate.
    const settingsShortcuts: readonly (Command & { entry: AdminEntry })[] = [
      {
        entry: "data-model",
        id: "screen:settings-data-model",
        label: t("settings.tab.data-model"),
        keywords: ["custom-fields", "products", "offer-templates", "pipelines"],
        type: "screen",
        route: settingsAddress("data-model"),
      },
      {
        entry: "ai",
        id: "screen:settings-ai",
        label: t("settings.tab.ai"),
        keywords: ["automations"],
        type: "screen",
        route: settingsAddress("ai"),
      },
    ];
    const settingsScreens: Command[] = settingsShortcuts.filter(
      (shortcut) => visible[shortcut.entry],
    );
    return [...screens, ...actions, ...settingsScreens];
  }, [t, visible]);
}

// The record kinds a search hit can route to (activity is a valid
// SearchResult type but has no 360 to land on — see entity.ts).
const RECORD_KINDS = new Set<EntityKind>(ENTITY_KINDS);

// Live record hits for the palette (RS-1): debounced via useDeferredValue
// rather than a timer (craft: no real-clock waits in the render path), and
// gated on a 2-char floor so single keystrokes don't fire a query per key.
function useSearchCommands(query: string): Command[] {
  const deferred = useDeferredValue(query.trim());
  const result = useQuery({
    queryKey: ["palette-search", deferred],
    enabled: deferred.length >= 2,
    queryFn: async () => {
      const { data, error } = await api.GET("/search", {
        params: { query: { q: deferred, limit: 5 } },
      });
      if (error) {
        // A palette search failure must degrade to the builtin commands,
        // never break the palette itself — this is a deliberate fallback,
        // not a swallowed error (the search screen still surfaces it).
        return [];
      }
      return data.data;
    },
  });
  const hits = (result.data ?? []).filter((hit) =>
    RECORD_KINDS.has(hit.type as EntityKind),
  );
  const projectLines = useProjectHitLines(
    hits.filter((hit) => hit.type === "project").map((hit) => hit.id),
  );
  return hits.map((hit) => ({
    id: `record:${hit.type}:${hit.id}`,
    label: hit.title ?? hit.id,
    // A project's secondary line is its key or its company, not the word
    // "project": a search hit for one carries no snippet, and two projects
    // called "Rollout" are told apart by the key a rep already types into
    // subject lines. Every other kind keeps the kind.
    subtitle:
      hit.type === "project"
        ? (projectLines.get(hit.id) ?? hit.type)
        : hit.type,
    type: "record" as const,
    route: ENTITY[hit.type as EntityKind].route(hit.id),
  }));
}

/**
 * The secondary line for each project hit: the key when the project has one,
 * else the company's name. At most five hits are on screen, so the reads are
 * per record and share the cache entries the project page and the company
 * reference already fill.
 */
function useProjectHitLines(projectIds: string[]): Map<string, string> {
  const projects = useQueries({
    queries: projectIds.map((id) => ({
      queryKey: ["project", id, "ref"],
      staleTime: 60_000,
      queryFn: async () => {
        const { data, error } = await api.GET("/projects/{id}", {
          params: { path: { id } },
        });
        if (error) {
          // A palette line that cannot be resolved falls back to the kind;
          // the hit itself still routes. The project page reports the
          // failure in full.
          return null;
        }
        return data;
      },
    })),
  });
  const companyIds = projects.flatMap((query) =>
    query.data && !query.data.key && query.data.organization_id
      ? [query.data.organization_id]
      : [],
  );
  const companies = useQueries({
    queries: companyIds.map((id) => ({
      // The same entry EntityRef fills for a company reference.
      queryKey: ["organization", "ref", id],
      staleTime: 60_000,
      queryFn: async () => {
        const { data, error } = await api.GET("/organizations/{id}", {
          params: { path: { id } },
        });
        if (error) {
          return null;
        }
        return data.display_name ?? null;
      },
    })),
  });
  const companyName = new Map(
    companyIds.map((id, index) => [id, companies[index]?.data ?? null]),
  );
  const lines = new Map<string, string>();
  projects.forEach((query, index) => {
    const project = query.data;
    if (!project) {
      return;
    }
    const line =
      project.key ??
      (project.organization_id
        ? companyName.get(project.organization_id)
        : null);
    if (line) {
      lines.set(projectIds[index], line);
    }
  });
  return lines;
}

const TYPE_KEY: Record<Command["type"], MessageKey> = {
  screen: "palette.typeScreen",
  action: "palette.typeAction",
  record: "palette.typeRecord",
};

export const ASK_QUERY_KEY = "margince.askQuery";

export function CommandPalette({
  open,
  onClose,
  commands,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  commands: Command[];
}>) {
  const t = useT();
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  // AC-shell-3: opening focuses AND clears the input.
  useEffect(() => {
    if (open) {
      setQuery("");
      setSelected(0);
      inputRef.current?.focus();
    }
  }, [open]);

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) {
      return commands;
    }
    return commands.filter(
      (command) =>
        command.label.toLowerCase().includes(needle) ||
        (command.subtitle ?? "").toLowerCase().includes(needle) ||
        (command.keywords ?? []).some((word) =>
          word.toLowerCase().includes(needle),
        ),
    );
  }, [commands, query]);

  // RS-1: live record hits from /search, plus a "see all" row that lands
  // on the full results screen. Row order: builtin matches, then records,
  // then see-all, then the Ask-AI row last.
  const records = useSearchCommands(query);
  const seeAll: Command | null = query.trim()
    ? {
        id: "search:all",
        label: t("palette.seeAll", { query: query.trim() }),
        type: "action",
        route: { screen: "search", id: encodeURIComponent(query.trim()) },
      }
    : null;

  // The run-as-NL row (AC-shell-4): appended last whenever there is a query.
  const askRow: Command | null = query.trim()
    ? {
        id: "ask-ai",
        label: t("palette.askAi", { query: query.trim() }),
        type: "action",
        route: { screen: "ai" },
      }
    : null;
  const rows = [
    ...filtered,
    ...records,
    ...(seeAll ? [seeAll] : []),
    ...(askRow ? [askRow] : []),
  ];
  const clamp = (index: number) =>
    Math.max(0, Math.min(index, rows.length - 1));

  const run = (command: Command) => {
    if (command.id === "ask-ai") {
      // NOSONAR: persisted value is a trimmed plain string from a controlled input, consumed as text (never eval'd or rendered as HTML)
      sessionStorage.setItem(ASK_QUERY_KEY, query.trim());
    }
    onClose();
    navigate(command.route);
  };

  if (!open) {
    return null;
  }

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: backdrop dismiss; Esc is the keyboard path
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc handled on the input below
    <div // NOSONAR: backdrop dismiss only; keyboard path (Esc) is handled on the input inside
      className="overlay palette-overlay"
      onClick={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <div
        className="palette"
        // NOSONAR: styled overlay palette, not a native modal; conditional mount and layout don't map cleanly to <dialog>
        role="dialog"
        aria-modal="true"
        aria-label={t("palette.aria")}
      >
        <div className="palette-input">
          <Search aria-hidden />
          <input
            ref={inputRef}
            value={query}
            placeholder={t("palette.placeholder")}
            aria-label={t("palette.aria")}
            onChange={(event) => {
              setQuery(event.target.value);
              setSelected(0);
            }}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                onClose();
              } else if (event.key === "ArrowDown") {
                event.preventDefault();
                setSelected((index) => clamp(index + 1));
              } else if (event.key === "ArrowUp") {
                event.preventDefault();
                setSelected((index) => clamp(index - 1));
              } else if (event.key === "Enter" && rows[selected]) {
                run(rows[selected]);
              }
            }}
          />
          <span className="kbd">{"esc"}</span>
        </div>
        <div className="palette-list" ref={listRef}>
          {rows.length === 0 && (
            <div className="empty">{t("palette.empty")}</div>
          )}
          {rows.map((command, index) => (
            <button
              key={command.id}
              type="button"
              className={
                index === selected ? "palette-row selected" : "palette-row"
              }
              onClick={() => run(command)}
              ref={(element) => {
                if (index === selected) {
                  element?.scrollIntoView?.({ block: "nearest" });
                }
              }}
            >
              {command.id === "ask-ai" ? (
                <Sparkles aria-hidden />
              ) : (
                <CornerDownLeft aria-hidden />
              )}
              <span className="label">{command.label}</span>
              {command.subtitle && (
                <span className="sub">{command.subtitle}</span>
              )}
              <span className="type">{t(TYPE_KEY[command.type])}</span>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}

// Global ⌘K / Ctrl+K binding (AC-shell-3).
// The palette answers to Meta+K and Ctrl+K both, but an affordance may only
// advertise one, and it has to be the one the reader's keyboard has: a Windows
// user told to press ⌘K is being told to press a key that is not there. Pure in
// its argument so the call site passes `navigator.platform` and this stays
// testable without stubbing the platform.
/**
 * The chord as its KEYS, because the one surface that draws it draws a cap per
 * key (app/topbar.tsx).
 *
 * Keys rather than a joined string, and no regex: the top bar used to split a
 * "⌘K" label with a lookbehind, which is a parse-time SyntaxError on an engine
 * without lookbehind (Safari before 16.4) — a blank app rather than a
 * plain-looking shortcut.
 */
export function paletteHotkeyCaps(platform: string): readonly string[] {
  return /mac|iphone|ipad|ipod/i.test(platform) ? ["⌘", "K"] : ["Ctrl", "K"];
}

export function usePaletteHotkey(toggle: () => void) {
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        toggle();
      }
    };
    globalThis.addEventListener("keydown", onKey);
    return () => globalThis.removeEventListener("keydown", onKey);
  }, [toggle]);
}
