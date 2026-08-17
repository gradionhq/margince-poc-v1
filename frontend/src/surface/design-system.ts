// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The design-system surface a unit screen may build from.
//
// A re-export file rather than a deep import into atoms.tsx, because this list
// IS the promise: what is named here a unit may use and the core may not break
// casually, and what is not named here is core-internal however exported it
// happens to be. It is this side's `//margince:extension-surface` — the Go
// tier gets its boundary from the compiler and a marker test, and a bundler
// gives none at all, so the surface has to be a declared thing before a gate
// can hold anyone to it.
//
// Widening it is a reviewed act. Adding a name here says the core will keep
// rendering it for units it did not write, which is a different promise from
// "this component exists".
export {
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
// FactList, because a unit screen has NO other way to draw a label→value pair.
//
// No extension ships a stylesheet — nothing in extensions/*/frontend imports
// CSS and the bundler gives a unit no place to put any — so a unit that writes
// its own `<dl>` gets the browser's 40px `dd` indent and no alignment at all.
// Publishing the pre-styled primitive is the only way a unit can present a
// record back, and a surface that offered every control for COLLECTING input
// and none for that is a surface whose screens look unfinished for a reason
// their authors cannot fix.
//
// It is the read-only half of what those screens need, which is why this is the
// primitive that gets published rather than FieldGrid: a unit describing what it
// connected is not editing it.
export { type Fact, FactList } from "../design-system/factlist";
// RecordPicker, because the alternative is every unit that touches a core
// record asking a person to paste a UUID.
//
// Picking a record is the one interaction a unit cannot avoid the moment it
// writes anything the product owns, and it is not a control anybody should
// rebuild: the debounce, a late answer ignored rather than rendered, the
// candidates dropped when the search space changes, and the selected state are
// four decisions each, and a unit getting one of them wrong is a screen that
// looks like the product and behaves like a prototype.
//
// WHAT IT DOES NOT DO, because the difference costs a caller nothing to know
// and everything to assume:
//
//   - It ignores a stale answer; it does not ABORT the request. Rapid typing
//     still reaches the server, so a searchTargets that is expensive to answer
//     needs a bound of its own.
//   - Its failed-search line is the component's own English, from the caught
//     error. A unit that needs that line translated cannot supply it here yet
//     — the honest workaround is a searchTargets that resolves empty and says
//     so in the unit's own copy.
//   - It is a labelled field over a list of buttons, not a combobox: no
//     role=combobox, no aria-expanded, no arrow-key navigation.
//
// It carries NO transport of its own — the caller supplies searchTargets — so
// exporting it publishes a rendering promise and no data one. A unit reaches
// its candidates through the api surface, under the caller's own RBAC, exactly
// as a core screen does.
export {
  RecordPicker,
  type RecordPickerCandidate,
} from "../design-system/recordpicker";
// Select and its option type, because a unit offering a CLOSED choice has no
// other way to: check-native-controls refuses a bare <select> in
// extensions/*/frontend exactly as it does in core, and a unit left with only
// TextInput has to accept free text where the contract declares an enum.
export { Select, type SelectOption } from "../design-system/select";
