import { CornerDownLeft, Search, Sparkles } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
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
  type: "screen" | "action" | "record";
  route: Route;
};

export function useBuiltinCommands(): Command[] {
  const t = useT();
  return useMemo(() => {
    const screens: Command[] = NAV.map((item) => ({
      id: `screen:${item.screen}`,
      label: t(item.labelKey),
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
        route: { screen: "onboarding", id: "read" },
      },
      {
        id: "action:booking",
        label: t("action.booking"),
        type: "action",
        route: { screen: "book" },
      },
    ];
    // Settings-reached screens (not in NAV), added to the command surface
    // explicitly so ⌘K still reaches them.
    const settingsScreens: Command[] = [
      {
        id: "screen:custom-fields",
        label: t("nav.customFields"),
        type: "screen",
        route: { screen: "custom-fields" },
      },
    ];
    return [...screens, ...actions, ...settingsScreens];
  }, [t]);
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
        (command.subtitle ?? "").toLowerCase().includes(needle),
    );
  }, [commands, query]);

  // The run-as-NL row (AC-shell-4): appended last whenever there is a query.
  const askRow: Command | null = query.trim()
    ? {
        id: "ask-ai",
        label: t("palette.askAi", { query: query.trim() }),
        type: "action",
        route: { screen: "ai" },
      }
    : null;
  const rows = askRow ? [...filtered, askRow] : filtered;
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
    // NOSONAR: backdrop dismiss only; keyboard path (Esc) is handled on the input inside
    // biome-ignore lint/a11y/noStaticElementInteractions: backdrop dismiss; Esc is the keyboard path
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc handled on the input below
    <div
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
