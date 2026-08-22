// The scale between an amount as a person types it and the integer this
// product stores.
//
// It is NOT presentation, and that is why it is its own module beside the
// formatters rather than inside them: the same table governs the write
// direction, where locale must never reach, and format.ts's own header says
// locale never flows back into storage. These functions take no locale
// deliberately — a currency's minor-unit count is a property of the currency,
// not of who is reading.
//
// It exists because the write direction had no owner at all. Eleven call sites
// spelled `Math.round(Number(amount) * 100)` and `valueMinor / 100`, which is
// right for the euro and wrong for every currency that is not two-decimal: VND,
// JPY and KRW have no minor unit, so a dong price typed as 18,000,000 was
// stored as 1,800,000,000, and a Kuwaiti dinar — three digits — was stored at a
// tenth.
//
// WHY NOT Intl. The first version of this module read the count from
// `Intl.NumberFormat(...).resolvedOptions().maximumFractionDigits`, on the
// grounds that the runtime already ships the answer and maintains it. It ships
// a DIFFERENT answer: Intl follows CLDR, which records how a currency is used,
// and the server follows ISO 4217, which records what the standard assigns.
// They disagree on ten codes, and two of them are ordinary spendable money —
// CLDR gives MGA and IRR zero digits where ISO gives two, and IQD zero where
// ISO gives three. A browser scaling by CLDR and a server scaling by ISO is the
// hundredfold disagreement this whole change exists to remove, reintroduced one
// currency over.
//
// So the table below MIRRORS shared/kernel/values/minorunits.go, exactly, and
// TestTheFrontendMinorUnitTableMatchesTheGoOne (backend/frontendminorunits_test.go)
// fails when the two drift apart in either direction. Intl is still the right
// tool for DISPLAY — grouping, symbol placement, the reader's own conventions —
// and format.ts uses it for that. It is the wrong tool for deciding an integer
// two systems must agree on.

// MINOR_UNIT_EXCEPTIONS lists the ISO 4217 codes whose minor unit is not the
// usual two digits. Most currencies, including EUR and USD, carry two, so this
// names the departures and the default below carries the rest.
//
// A code missing here renders at two digits and is wrong for that code — which
// is the tolerable failure, because refusing an unnamed code would suppress the
// amount for nearly every currency on earth while admitting the two dozen
// exceptions. Adding a code is a one-line change HERE AND IN THE GO TABLE; the
// gate refuses one without the other.
const MINOR_UNIT_EXCEPTIONS: Readonly<Record<string, number>> = {
  BIF: 0,
  CLP: 0,
  DJF: 0,
  GNF: 0,
  ISK: 0,
  JPY: 0,
  KMF: 0,
  KRW: 0,
  PYG: 0,
  RWF: 0,
  UGX: 0,
  UYI: 0,
  VND: 0,
  VUV: 0,
  XAF: 0,
  XAG: 0,
  XAU: 0,
  XDR: 0,
  XOF: 0,
  XPD: 0,
  XPF: 0,
  XPT: 0,
  XTS: 0,
  XXX: 0,
  BHD: 3,
  IQD: 3,
  JOD: 3,
  KWD: 3,
  LYD: 3,
  OMR: 3,
  TND: 3,
  CLF: 4,
  UYW: 4,
};

// minorUnitDigits reports how many minor-unit digits a currency carries.
//
// Two is ISO 4217's own default and what an unknown or malformed code answers,
// rather than throwing: a currency we cannot place is still one somebody typed
// an amount into, and refusing to scale it would store the raw digits as minor
// units.
export function minorUnitDigits(currency: string): number {
  return MINOR_UNIT_EXCEPTIONS[currency.trim().toUpperCase()] ?? 2;
}

// toMinorUnits converts a major-unit amount — what the person typed — into the
// integer the API stores, or NaN when it cannot do so exactly.
//
// It REFUSES a figure finer than its currency rather than rounding one. That is
// the rule documentextraction already stated for the same reason: "a figure
// with more decimals than the currency has is a misread, and silently dropping
// a digit is how an amount becomes wrong by an order of magnitude." A tenth of
// a cent is not a rounding question, it is a number somebody got wrong.
//
// Refusing is also what removes the double-rounding this function had. Rounding
// to a working precision and then rounding again gave answers that were wrong
// rather than merely approximate: 1.004951 EUR came out 101 cents where 100.4951
// rounds to 100, and 0.004951 came out a whole cent for less than half of one.
// With over-precise input refused there is one rounding left, and it only ever
// resolves a binary artefact.
//
// The scaling shifts a decimal STRING rather than multiplying a float, because
// `1.005 * 100` is 100.49999999999999 and a multiply would lose the cent on an
// amount stated exactly. Shifting the point in the text cannot.
//
// Halves round AWAY from zero, so a credit and a charge of the same size scale
// to the same magnitude — Math.round sends -0.5 to -0 and 0.5 to 1.
//
// NaN, rather than 0, is the refusal: a caller building a request body writes
// `amount_minor: 0` for a garbage input, and zero is a legal price. NaN
// serialises to null, which the nullable money fields take as unpriced and the
// non-nullable ones refuse — the API decides, not a silent default.
export function toMinorUnits(major: number, currency: string): number {
  if (!Number.isFinite(major)) {
    return Number.NaN;
  }
  const digits = minorUnitDigits(currency);
  // Finer than this currency can hold. Compared against the value's own
  // rounding rather than against the typed text, because the text is already
  // gone by the time a number arrives here.
  if (Number(major.toFixed(digits)) !== major) {
    return Number.NaN;
  }
  if (digits === 0) {
    return Number.isSafeInteger(major) ? major : Number.NaN;
  }
  // Enough places that the shift below never has to round a binary artefact
  // back into position; the value is already known to need no more than
  // `digits` of them.
  const text = major.toFixed(digits + 2);
  const negative = text.startsWith("-");
  const [whole, frac = ""] = (negative ? text.slice(1) : text).split(".");
  const shifted = Number(
    `${whole}${frac.slice(0, digits)}.${frac.slice(digits)}`,
  );
  const scaled = roundHalfAwayFromZero(shifted);
  const minor = negative ? -scaled : scaled;
  return Number.isSafeInteger(minor) ? minor : Number.NaN;
}

function roundHalfAwayFromZero(value: number): number {
  return value < 0 ? -Math.round(-value) : Math.round(value);
}

// toMajorUnits is the inverse, for seeding an input from a stored amount.
//
// It returns a number and not a string on purpose: the caller decides how many
// digits to show, and a currency with no minor unit must not be handed a ".00"
// that says it has one.
export function toMajorUnits(amountMinor: number, currency: string): number {
  return amountMinor / 10 ** minorUnitDigits(currency);
}
