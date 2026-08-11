// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The data surface: the typed client, and the two helpers that turn a failed
// read into the product's own error card.
//
// `api` is the SHARED client, not a per-unit one, and that is the point of
// exporting it rather than letting a unit build its own: it is parameterised
// by the MERGED contract in the composed lane, so a unit calls its own
// /ext/<unit>/… route with the same `api.POST` a core screen uses — no
// wrapper, no cast, and a route the installation does not serve is a type
// error rather than a 404 discovered by a person.
//
// QueryStates and throwProblem are here for the same reason a unit does not
// write its own loading spinner: an installation should not be able to tell
// which screens the core wrote from how they fail.
export { api } from "../api/client";
export { QueryStates, throwProblem } from "../screens/common";
