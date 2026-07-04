import { Info } from "lucide-react";
import { useState } from "react";
import { type ExplainedMoney, formatDate, formatMoney } from "../format/format";
import { useLocale, useT } from "../i18n";

// "Explain this number" (B-EP09.18, ADR-0004): a converted aggregate opens
// into its contributing rows — native amount + rate + rate date per row.
// The headline figure is the IR's base_value, rendered verbatim; the rows
// are lineage, not an alternative computation.

export function ExplainNumber({
  money,
  workspaceZone,
}: {
  money: ExplainedMoney;
  workspaceZone: string;
}) {
  const t = useT();
  const { locale } = useLocale();
  const [open, setOpen] = useState(false);

  return (
    <span className="explain">
      <strong data-testid="explained-base">
        {formatMoney(money.baseValueMinor, money.baseCurrency, locale)}
      </strong>
      <button
        type="button"
        className="iconbtn explain-toggle"
        aria-label={t("explain.open")}
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <Info aria-hidden />
      </button>
      {open && (
        <div className="explain-pop card" role="note">
          <p className="t-label">{t("explain.title")}</p>
          <ul className="explain-rows">
            {money.rows.map((row) => (
              <li key={`${row.label}-${row.rateDate}`}>
                <span className="t-caption">{row.label}</span>
                <span className="t-mono">
                  {formatMoney(
                    row.nativeAmountMinor,
                    row.nativeCurrency,
                    locale,
                  )}
                </span>
                <span className="t-small">
                  {t("explain.rate", {
                    rate: row.rate,
                    date: formatDate(row.rateDate, locale, workspaceZone),
                  })}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </span>
  );
}
