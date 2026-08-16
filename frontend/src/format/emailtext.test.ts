import { describe, expect, it } from "vitest";
import { emailSummaryText, splitEmailBody } from "./emailtext";

describe("splitEmailBody", () => {
  it("peels the From/To preamble capture folds into the body", () => {
    const parts = splitEmailBody(
      "From: anna@kunde.de\nTo: lars@gradion.com\n\nKönnen wir Dienstag sprechen?",
    );
    expect(parts.header).toBe("From: anna@kunde.de\nTo: lars@gradion.com");
    expect(parts.main).toBe("Können wir Dienstag sprechen?");
    expect(parts.trimmed).toBe("");
  });

  it("cuts at the RFC 3676 signature delimiter", () => {
    const parts = splitEmailBody(
      "Anbei das Angebot.\n\n-- \nAnna Berger\nKunde GmbH\n+49 89 123456",
    );
    expect(parts.main).toBe("Anbei das Angebot.");
    expect(parts.trimmed).toContain("Anna Berger");
    expect(parts.trimmed).toContain("+49 89 123456");
  });

  it("folds a German sign-off and everything under it", () => {
    const parts = splitEmailBody(
      "Danke für das Gespräch. Ich melde mich nächste Woche.\n\nMit freundlichen Grüßen\nAnna Berger\nGeschäftsführerin\nKunde GmbH",
    );
    expect(parts.main).toBe(
      "Danke für das Gespräch. Ich melde mich nächste Woche.",
    );
    expect(parts.trimmed).toContain("Mit freundlichen Grüßen");
    expect(parts.trimmed).toContain("Geschäftsführerin");
  });

  it.each([
    ["Viele Grüße", "Viele Grüße\nAnna"],
    ["Beste Grüße", "Beste Grüße\nAnna"],
    ["VG", "VG\nAnna"],
    ["LG", "LG\nAnna"],
    ["MfG", "MfG,\nAnna"],
    ["Best regards", "Best regards\nAnna"],
    ["Cheers", "Cheers\nAnna"],
    ["Von meinem iPhone gesendet", "Von meinem iPhone gesendet"],
    ["Sent from my iPhone", "Sent from my iPhone"],
  ])("folds the sign-off %s", (_name, tail) => {
    const parts = splitEmailBody(`Das Angebot passt so.\n\n${tail}`);
    expect(parts.main).toBe("Das Angebot passt so.");
    expect(parts.trimmed).not.toBe("");
  });

  it("leaves a short form alone when it opens a sentence", () => {
    const body =
      "LG Waschmaschinen sind auch im Angebot, die Lieferung dauert aber acht Wochen. Best of luck damit.";
    const parts = splitEmailBody(body);
    expect(parts.main).toBe(body);
    expect(parts.trimmed).toBe("");
  });

  it("does not fold a sign-off word that opens a long message", () => {
    const body = [
      "Danke für die schnelle Antwort.",
      "",
      ...Array.from({ length: 20 }, (_, i) => `Punkt ${i + 1} ist geklärt.`),
    ].join("\n");
    const parts = splitEmailBody(body);
    expect(parts.main).toBe(body.trim());
    expect(parts.trimmed).toBe("");
  });

  it.each([
    ["English attribution", "On Tue, 12 Aug 2026 at 09:14, Max Muster wrote:"],
    ["German attribution", "Am 12.08.2026 um 09:14 schrieb Max Muster:"],
    ["forwarded block", "---------- Forwarded message ----------"],
    ["German forward", "-------- Weitergeleitete Nachricht --------"],
    ["original message", "----- Original Message -----"],
  ])("cuts at a %s", (_name, marker) => {
    const parts = splitEmailBody(
      `Passt, machen wir so.\n\n${marker}\n> Wollen wir Dienstag sprechen?`,
    );
    expect(parts.main).toBe("Passt, machen wir so.");
    expect(parts.trimmed).toContain(marker);
    expect(parts.trimmed).toContain("> Wollen wir Dienstag sprechen?");
  });

  it("keeps an attribution line with the quote it introduces", () => {
    const parts = splitEmailBody(
      "Ja, gerne.\n\nAm 12.08.2026 schrieb Max:\n> Passt Dienstag?",
    );
    expect(parts.main).toBe("Ja, gerne.");
    expect(parts.trimmed.startsWith("Am 12.08.2026 schrieb Max:")).toBe(true);
  });

  it("cuts at a bare quoted block with no attribution", () => {
    const parts = splitEmailBody("Ja, passt.\n\n> Passt Dienstag?\n> Anna");
    expect(parts.main).toBe("Ja, passt.");
    expect(parts.trimmed).toBe("> Passt Dienstag?\n> Anna");
  });

  it("cuts at an Outlook reply header block", () => {
    const parts = splitEmailBody(
      "Sehe ich auch so.\n\nVon: Max Muster <max@kunde.de>\nGesendet: Dienstag, 12. August 2026 09:14\nAn: Lars\nBetreff: AW: Angebot\n\nPasst Dienstag?",
    );
    expect(parts.main).toBe("Sehe ich auch so.");
    expect(parts.trimmed).toContain("Von: Max Muster");
    expect(parts.trimmed).toContain("Betreff: AW: Angebot");
  });

  it("does not read a Von: line as a reply header without a sent date", () => {
    const body =
      "Von: der Messe habe ich drei Kontakte mitgebracht.\nAlle drei wollen ein Angebot.";
    const parts = splitEmailBody(body);
    expect(parts.main).toBe(body);
    expect(parts.trimmed).toBe("");
  });

  it("keeps a message that is nothing but a greeting", () => {
    const parts = splitEmailBody("Viele Grüße\nAnna");
    expect(parts.main).toBe("Viele Grüße\nAnna");
    expect(parts.trimmed).toBe("");
  });

  it("keeps a message that is only a quoted forward", () => {
    const parts = splitEmailBody("> Passt Dienstag?\n> Anna");
    expect(parts.main).toBe("> Passt Dienstag?\n> Anna");
    expect(parts.trimmed).toBe("");
  });

  it("keeps the message when the preamble is followed only by a signature", () => {
    const parts = splitEmailBody(
      "From: anna@kunde.de\nTo: lars@gradion.com\n\n-- \nAnna Berger",
    );
    expect(parts.header).toBe("From: anna@kunde.de\nTo: lars@gradion.com");
    expect(parts.main).toBe("-- \nAnna Berger");
    expect(parts.trimmed).toBe("");
  });

  it("returns empty parts for an empty body", () => {
    expect(splitEmailBody("")).toEqual({ header: "", main: "", trimmed: "" });
    expect(splitEmailBody("   \n  ")).toEqual({
      header: "",
      main: "",
      trimmed: "",
    });
  });

  it("cuts at the first boundary when a mail has both a signature and a quote", () => {
    const parts = splitEmailBody(
      "Kurz zur Rückfrage: ja.\n\nViele Grüße\nAnna\n\nAm 12.08.2026 schrieb Max:\n> Passt Dienstag?",
    );
    expect(parts.main).toBe("Kurz zur Rückfrage: ja.");
    expect(parts.trimmed).toContain("Viele Grüße");
    expect(parts.trimmed).toContain("> Passt Dienstag?");
  });
});

describe("emailSummaryText", () => {
  it("collapses the message to one line without the preamble or sign-off", () => {
    const summary = emailSummaryText(
      "From: anna@kunde.de\nTo: lars@gradion.com\n\nKönnen wir\nDienstag sprechen?\n\nMit freundlichen Grüßen\nAnna Berger",
    );
    expect(summary).toBe("Können wir Dienstag sprechen?");
  });

  it("falls back to the whole body rather than returning nothing", () => {
    expect(emailSummaryText("> Passt Dienstag?")).toBe("> Passt Dienstag?");
    expect(emailSummaryText("")).toBe("");
  });
});
