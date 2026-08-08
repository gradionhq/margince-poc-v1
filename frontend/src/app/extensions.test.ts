import { describe, expect, it } from "vitest";
import {
  composedExtensions,
  EXTENSION_SCREEN,
  type ExtensionDescriptor,
  extensionRbacObject,
  findExtension,
  isExtensionRbacObject,
} from "./extensions";
import { parseHash } from "./router";

// The registry is the ONE place the SPA learns what an installation composed.
// Two states have to hold at once, and they are not the same test: a composed
// tree must route into its unit, and the VANILLA tree — the empty registry the
// committed stub carries, which is what a bare `pnpm build` and every core
// developer's checkout compiles — must still build and answer cleanly for a
// unit route nobody enabled. A lookup that only worked against a populated
// registry would strand the empty lane on a crash or a blank frame, and the
// empty lane is the default one.
//
// The fixtures below stand in for a composed installation. They are NOT read
// from build/composition/: this suite runs in the vanilla lane, where that
// directory legitimately does not exist, so the composed case is exercised by
// handing the same shape the generator emits to the same function App.tsx
// calls. The generator's own output shape is pinned on the Go side
// (stubMatchesVanilla + emit_test.go), so the two halves cannot drift apart
// without one of them failing.

const CRM_DEMO: ExtensionDescriptor = {
  name: "crm-demo",
  verbs: [
    {
      operationId: "crmDemoListNotes",
      route: "/v1/ext/crm-demo/notes",
      method: "GET",
      title: "List demo notes",
      version: "1.0.0",
      rbacObject: "ext_crm_demo_note",
    },
  ],
};

const COMPOSED: readonly ExtensionDescriptor[] = [CRM_DEMO];

describe("the composed extension registry", () => {
  it("resolves an extension screen from the composed registry", () => {
    // The whole path a click takes: the hash the rail would set, parsed by the
    // router the shell already uses, then looked up in the registry. Asserting
    // on findExtension alone would leave the route TOKEN untested, and a
    // registry keyed under a screen name App.tsx never dispatches on resolves
    // nothing however correct its lookup is.
    const route = parseHash("#/ext/crm-demo");
    expect(route.screen).toBe(EXTENSION_SCREEN);

    const unit = findExtension(route.id, COMPOSED);
    expect(unit).not.toBeNull();
    expect(unit?.name).toBe("crm-demo");
    // The screen renders what the unit publishes, so the verbs have to survive
    // the lookup — a descriptor stripped to its name would resolve and then
    // render an empty page.
    expect(unit?.verbs.map((v) => v.route)).toEqual(["/v1/ext/crm-demo/notes"]);
  });

  it("404s cleanly when the registry is empty", () => {
    // Three ways the empty lane is reached, all of which must answer null
    // rather than throw or return a half-built descriptor.
    expect(findExtension("crm-demo", [])).toBeNull();
    // The LIVE vanilla registry — the committed stub, not a fixture. This is
    // the assertion that fails the moment the vanilla tree stops being the
    // empty-tree output.
    expect(composedExtensions).toEqual([]);
    expect(findExtension("crm-demo")).toBeNull();
  });

  it("answers null for a unit route that carries no name", () => {
    // `#/ext` with no segment: route.id is undefined, and a lookup that
    // coerced it to "undefined" would match a unit literally named that.
    expect(findExtension(parseHash("#/ext").id, COMPOSED)).toBeNull();
    expect(findExtension("", COMPOSED)).toBeNull();
  });

  it("does not resolve a unit the composed set does not carry", () => {
    expect(findExtension("crm-hello", COMPOSED)).toBeNull();
    // Case is not folded: the unit name is a directory name and Postgres
    // identifiers derived from it are lowercase, so an uppercase spelling is
    // a different (absent) unit, not the same one.
    expect(findExtension("CRM-DEMO", COMPOSED)).toBeNull();
  });

  it("narrows a declared RBAC object into the capability vocabulary", () => {
    // The reason capability.ts's RbacObject is widened at all: the descriptor
    // carries a plain string (the generator cannot type what a unit will
    // declare), and a unit screen has to hand it to useCan without a cast.
    const object = extensionRbacObject(CRM_DEMO.verbs[0]);
    expect(object).toBe("ext_crm_demo_note");
  });

  it("refuses an object outside the ext_ namespace", () => {
    // A verb declaring no object is the common case today (neither in-tree
    // unit owns records); a verb declaring a CORE object would be an
    // extension reaching into the closed vocabulary, and the client must not
    // hand it to a gate that would then read as core.
    expect(
      extensionRbacObject({ ...CRM_DEMO.verbs[0], rbacObject: "" }),
    ).toBeNull();
    expect(
      extensionRbacObject({ ...CRM_DEMO.verbs[0], rbacObject: "deal" }),
    ).toBeNull();
    expect(isExtensionRbacObject("ext_crm_demo_note")).toBe(true);
    expect(isExtensionRbacObject("deal")).toBe(false);
    expect(isExtensionRbacObject("ext_")).toBe(false);
  });
});
