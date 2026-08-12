// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";

// Fitness function for the one way a mutation can refuse work the screen was
// plainly offering: a `mutationFn` that reads the value it needs out of its
// CLOSURE instead of taking it as the mutation's variable.
//
// `useMutation` re-arms its options in a PASSIVE effect
// (@tanstack/react-query's useMutation.js: `useEffect(() =>
// observer.setOptions(options))`). Between the commit that first renders a
// control with loaded state and the effect that hands the observer that
// render's closure, the observer still holds the PREVIOUS render's closure —
// the one where the state was null. A click landing in that window runs the
// older function, reads nothing, and refuses. React yields between commit and
// passive effects, so the window is real in a browser and merely wider on a
// loaded machine.
//
// The tell is a zero-parameter `mutationFn` that throws on a falsy check of a
// value it closed over. Such a guard cannot fire on a real path — the control
// that triggers it only renders once the value exists — so the only state it
// can ever report is the stale-closure window, and what it reports there is
// false: it tells a reader with a filled form that there is nothing to submit.
// A variable cannot be older than the control that carries it, so the fix is
// always the same shape: pass the value to `mutate()` and let the parameter
// replace the guard.
//
// WHAT THIS CATCHES: the guarded form, which is the one that produces a false
// refusal a user can see and report.
//
// WHAT THIS DOES NOT CATCH, deliberately: an unguarded closure read. Deciding
// which free identifier is component state and which is a stable prop needs
// the type checker and a rule about what counts, and a gate whose rules are
// fiddly is a gate that gets worked around rather than fixed. The guard is the
// author's own admission that the value can be absent, which is the honest
// place to draw the line.

const screensDir = dirname(fileURLToPath(import.meta.url));
const srcRoot = join(screensDir, "..");

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      return sourceFiles(path);
    }
    return /\.tsx?$/.test(entry.name) ? [path] : [];
  });
}

/** Every name bound inside the function body, so a local cannot read as free. */
function localNames(body: ts.Node): Set<string> {
  const names = new Set<string>();
  const visit = (node: ts.Node) => {
    // A destructured `const { data, error } = await …` binds through a
    // BindingElement rather than a plain identifier; missing those reads every
    // destructured local as free and reports the awaited value as closed over.
    if (
      (ts.isVariableDeclaration(node) ||
        ts.isParameter(node) ||
        ts.isBindingElement(node)) &&
      ts.isIdentifier(node.name)
    ) {
      names.add(node.name.text);
    }
    ts.forEachChild(node, visit);
  };
  visit(body);
  return names;
}

/** The root identifier of `!x` / `!x.y.z`, for every negation in the test. */
function negatedRoots(expression: ts.Node): string[] {
  const roots: string[] = [];
  const visit = (node: ts.Node) => {
    if (
      ts.isPrefixUnaryExpression(node) &&
      node.operator === ts.SyntaxKind.ExclamationToken
    ) {
      let operand: ts.Node = node.operand;
      while (ts.isPropertyAccessExpression(operand)) {
        operand = operand.expression;
      }
      if (ts.isIdentifier(operand)) {
        roots.push(operand.text);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(expression);
  return roots;
}

function refuses(statement: ts.Node): boolean {
  let found = false;
  const visit = (node: ts.Node) => {
    if (ts.isThrowStatement(node)) {
      found = true;
    }
    if (
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      node.expression.text === "throwProblem"
    ) {
      found = true;
    }
    ts.forEachChild(node, visit);
  };
  visit(statement);
  return found;
}

function findingsIn(file: string): string[] {
  const text = readFileSync(file, "utf8");
  const source = ts.createSourceFile(
    file,
    text,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TSX,
  );
  const findings: string[] = [];

  const visit = (node: ts.Node) => {
    if (
      ts.isPropertyAssignment(node) &&
      ts.isIdentifier(node.name) &&
      node.name.text === "mutationFn" &&
      (ts.isArrowFunction(node.initializer) ||
        ts.isFunctionExpression(node.initializer)) &&
      node.initializer.parameters.length === 0 &&
      node.initializer.body
    ) {
      const body = node.initializer.body;
      const locals = localNames(body);
      const guards = (inner: ts.Node) => {
        if (ts.isIfStatement(inner) && refuses(inner.thenStatement)) {
          const free = negatedRoots(inner.expression).filter(
            (name) => !locals.has(name),
          );
          if (free.length > 0) {
            const { line } = source.getLineAndCharacterOfPosition(
              inner.getStart(source),
            );
            findings.push(
              `${relative(srcRoot, file)}:${line + 1} — mutationFn takes no variable and refuses on closed-over ${free.map((n) => `\`${n}\``).join(", ")}; pass it to mutate() instead`,
            );
          }
        }
        ts.forEachChild(inner, guards);
      };
      guards(body);
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return findings;
}

// Vitest's 5s per-test default is sized for an async UI test, not for a
// synchronous parse of every file that defines a mutation. The work below is
// bounded and deterministic — only the machine's speed varies — so it states a
// budget a loaded runner cannot exhaust rather than living on one the default
// happens to cover on a quiet one. The first version of this gate had no such
// budget and timed out in CI while passing locally.
const PARSE_MS = 60_000;

describe("mutation variables", () => {
  it("never refuses on state a mutationFn only closed over", () => {
    const files = sourceFiles(srcRoot);
    // A file with no `mutationFn` in its text cannot hold a finding, and
    // parsing it anyway is what this gate would otherwise cost: a whole-tree
    // parse on every run, growing with the SPA rather than with the number of
    // mutations. The first version did exactly that and timed out on a loaded
    // CI runner — a source check whose verdict depends on how busy the machine
    // is, which is the failure this whole change is about.
    const candidates = files.filter((file) =>
      readFileSync(file, "utf8").includes("mutationFn"),
    );
    // Both counts, because either one reaching zero passes this gate while
    // comparing nothing — a swept tree that finds no files and a prefilter that
    // admits none look identical from the outside, and both look like success.
    expect(files.length).toBeGreaterThan(0);
    expect(candidates.length).toBeGreaterThan(0);

    const findings = candidates.flatMap(findingsIn);
    expect(findings, `\n${findings.join("\n")}\n`).toEqual([]);
  }, PARSE_MS);
});
