import { existsSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import ts from "typescript";
import { ASYNC_UTIL_TIMEOUT_MS } from "../vitest.budget";

// Reads a test file and works out, for every test in it, how long that test is
// ALLOWED to spend waiting — the sum of the budgets of the waiters it runs in
// sequence — and how long it is allowed to take. Issue 1144 is what happens
// when the first number exceeds the second: the test fails while every waiter
// in it is still inside its own budget, and the failure names the test rather
// than the waiter that was slow.
//
// Parsed with the TypeScript compiler rather than matched with a regex. A
// hand-rolled scanner over this tree over-counted a 688-line file as one test
// with 39 waiters, and a guard reporting a number nobody believes is worse than
// no guard: it would demand a ceiling ten times anything real.
//
// EVERY UNCERTAINTY IS REPORTED, NEVER ASSUMED AWAY. A waiter whose timeout
// this cannot fold, or a test shape it does not recognise, is the one case that
// matters — the budget it hides is exactly the budget nobody checked. An
// earlier version defaulted an unfoldable `{ timeout: … }` to the smallest
// possible value and walked straight past a live 11000ms violation.

/** A waiter that states no timeout takes Testing Library's default. */
const DEFAULT_WAITER_MS = ASYNC_UTIL_TIMEOUT_MS;

const WAITER = /^(findBy|findAllBy|waitFor|waitForElementToBeRemoved)/;

/** `it`, and the forms that wrap it: `it.each(cases)(…)`, `it.only`, `it.for`. */
const TEST_CALLEE = /^(it|test)$/;

export type TestBudget = {
  file: string;
  line: number;
  name: string;
  /** The sum of the waiter budgets this test can spend, in ms. */
  waiterBudgetMs: number;
  /** The per-test ceiling this test actually runs under, in ms. */
  ceilingMs: number;
  /** Whether that ceiling is the test's own, rather than the suite default. */
  ceilingIsStated: boolean;
  /** Timeouts this reader could not fold, each as written. Must be empty. */
  unresolved: string[];
};

/** `X * 4` and `X + 3000`, the two ways a budget here is built from another. */
function arithmetic(
  node: ts.BinaryExpression,
  value: (node: ts.Expression) => number | undefined,
): number | undefined {
  const left = value(node.left);
  const right = value(node.right);
  if (left === undefined || right === undefined) return undefined;
  if (node.operatorToken.kind === ts.SyntaxKind.AsteriskToken)
    return left * right;
  if (node.operatorToken.kind === ts.SyntaxKind.PlusToken) return left + right;
  return undefined;
}

/**
 * A resolver for numbers written in this file: a literal, a name it gave a
 * number to, or arithmetic over those. A name imported from elsewhere does not
 * resolve, and that is reported rather than guessed at.
 */
function resolver(consts: Map<string, number>) {
  const value = (node: ts.Expression): number | undefined => {
    if (ts.isNumericLiteral(node)) return Number(node.text);
    if (ts.isIdentifier(node)) return consts.get(node.text);
    if (ts.isParenthesizedExpression(node)) return value(node.expression);
    if (ts.isBinaryExpression(node)) return arithmetic(node, value);
    return undefined;
  };
  return value;
}

/** `const X = 5000` and `const Y = X * 4` anywhere in one file. */
function declaredConsts(
  source: ts.SourceFile,
  seeded: Map<string, number>,
): Map<string, number> {
  const known = new Map(seeded);
  const value = resolver(known);
  const visit = (node: ts.Node): void => {
    if (
      ts.isVariableDeclaration(node) &&
      ts.isIdentifier(node.name) &&
      node.initializer
    ) {
      const resolved = value(node.initializer);
      if (resolved !== undefined) known.set(node.name.text, resolved);
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return known;
}

function parse(file: string): ts.SourceFile {
  return ts.createSourceFile(
    file,
    readFileSync(file, "utf8"),
    ts.ScriptTarget.Latest,
    true,
  );
}

/** `./use-company-read` next to the importer, with the extension it actually has. */
function resolveModule(
  fromFile: string,
  specifier: string,
): string | undefined {
  if (!specifier.startsWith(".")) return undefined;
  const base = resolve(dirname(fromFile), specifier);
  for (const candidate of [
    `${base}.ts`,
    `${base}.tsx`,
    `${base}/index.ts`,
    `${base}/index.tsx`,
  ]) {
    if (existsSync(candidate)) return candidate;
  }
  return undefined;
}

/** The numeric constants ONE named import brings in, added to `into`. */
function takeImport(
  statement: ts.ImportDeclaration,
  file: string,
  into: Map<string, number>,
): void {
  if (!ts.isStringLiteral(statement.moduleSpecifier)) return;
  const bindings = statement.importClause?.namedBindings;
  if (!bindings || !ts.isNamedImports(bindings)) return;
  const target = resolveModule(file, statement.moduleSpecifier.text);
  if (!target) return;
  const theirs = declaredConsts(parse(target), new Map());
  for (const element of bindings.elements) {
    const value = theirs.get((element.propertyName ?? element.name).text);
    if (value !== undefined) into.set(element.name.text, value);
  }
}

/**
 * A test's numeric constants, including the ones it imports from a sibling
 * module — `{ timeout: READ_POLL_MS * EXPECTED_READS * 5 }` is a real waiter
 * budget in this tree and READ_POLL_MS lives in the hook it drives. One hop
 * only: a budget written two modules away is reported unresolved rather than
 * chased, and reported is the safe end of that trade.
 */
function numericConsts(
  source: ts.SourceFile,
  file: string,
): Map<string, number> {
  const imported = new Map<string, number>();
  for (const statement of source.statements) {
    if (ts.isImportDeclaration(statement))
      takeImport(statement, file, imported);
  }
  return declaredConsts(source, imported);
}

/** The `timeout:` property of one options object, if it has one. */
function timeoutExpression(
  options: ts.ObjectLiteralExpression,
): ts.Expression | undefined {
  for (const property of options.properties) {
    if (!ts.isPropertyAssignment(property)) continue;
    if (property.name.getText() !== "timeout") continue;
    return property.initializer;
  }
  return undefined;
}

/** The `timeout:` a call states, wherever among its options objects it sits. */
function statedTimeout(call: ts.CallExpression): ts.Expression | undefined {
  return optionArguments(call)
    .map(timeoutExpression)
    .find((expression) => expression !== undefined);
}

/** Every options object among a call's arguments, in order. */
function optionArguments(
  call: ts.CallExpression,
): ts.ObjectLiteralExpression[] {
  return call.arguments.filter(
    (argument): argument is ts.ObjectLiteralExpression =>
      ts.isObjectLiteralExpression(argument),
  );
}

/**
 * The root name a call is made through: `it` for `it`, `it.only`, `it.each(…)`
 * and `it.each(…)(…)` alike. Walking to the root is what keeps the table-driven
 * and focused forms visible — matching only the immediate callee dropped 17
 * call sites in this tree, silently, which is the shape of hole this reader
 * exists to close.
 */
function rootCallee(node: ts.Node): string {
  let current: ts.Node = node;
  for (;;) {
    if (ts.isCallExpression(current)) current = current.expression;
    else if (ts.isPropertyAccessExpression(current))
      current = current.expression;
    else if (ts.isTaggedTemplateExpression(current)) current = current.tag;
    else break;
  }
  return ts.isIdentifier(current) ? current.text : "";
}

/** The immediate name a call uses, for spotting a waiter or a helper. */
function directCallee(call: ts.CallExpression): string {
  const expression = call.expression;
  if (ts.isPropertyAccessExpression(expression)) return expression.name.text;
  if (ts.isIdentifier(expression)) return expression.text;
  return "";
}

type Reading = { ms: number; unresolved: string[] };

/**
 * The waiter budget of one node, following awaited calls into the helpers that
 * do the waiting — a suite's render helper is usually where they live, and a
 * reader that stops at the test body cannot see them.
 *
 * `path` guards against recursion only, and is unwound on the way out: a helper
 * awaited four times costs four times, which is the whole point. Costs are
 * memoized per helper so that repetition is cheap rather than quadratic.
 */
function budgetOf(
  node: ts.Node,
  consts: Map<string, number>,
  helpers: Map<string, ts.Node>,
  path: Set<string>,
  memo: Map<string, Reading>,
): Reading {
  const value = resolver(consts);
  let ms = 0;
  const unresolved: string[] = [];

  const helperCost = (name: string): Reading => {
    const cached = memo.get(name);
    if (cached) return cached;
    if (path.has(name)) return { ms: 0, unresolved: [] };
    const body = helpers.get(name);
    if (!body) return { ms: 0, unresolved: [] };
    path.add(name);
    const reading = budgetOf(body, consts, helpers, path, memo);
    path.delete(name);
    memo.set(name, reading);
    return reading;
  };

  const takeWaiter = (call: ts.CallExpression): void => {
    const stated = statedTimeout(call);
    if (!stated) {
      ms += DEFAULT_WAITER_MS;
      return;
    }
    const folded = value(stated);
    if (folded === undefined) unresolved.push(stated.getText());
    else ms += folded;
  };

  const visit = (current: ts.Node): void => {
    if (ts.isCallExpression(current)) {
      const name = directCallee(current);
      if (WAITER.test(name)) takeWaiter(current);
      else if (helpers.has(name)) {
        const reading = helperCost(name);
        ms += reading.ms;
        unresolved.push(...reading.unresolved);
      }
    }
    ts.forEachChild(current, visit);
  };
  // The node itself, not only its children: a one-line arrow helper's body IS
  // the waiter call, and descending past it counts nothing.
  visit(node);
  return { ms, unresolved };
}

/** Named `function f() {}` and `const f = () => {}` bodies, by name, anywhere. */
function namedHelpers(source: ts.SourceFile): Map<string, ts.Node> {
  const helpers = new Map<string, ts.Node>();
  const visit = (node: ts.Node): void => {
    if (ts.isFunctionDeclaration(node) && node.name && node.body) {
      helpers.set(node.name.text, node.body);
    } else if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name)) {
      const initializer = node.initializer;
      if (
        initializer &&
        (ts.isArrowFunction(initializer) ||
          ts.isFunctionExpression(initializer))
      ) {
        helpers.set(node.name.text, initializer.body);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return helpers;
}

export function budgetsIn(file: string, globalCeilingMs: number): TestBudget[] {
  const source = parse(file);
  const consts = numericConsts(source, file);
  const value = resolver(consts);
  const helpers = namedHelpers(source);
  const found: TestBudget[] = [];

  const record = (call: ts.CallExpression): void => {
    // vitest takes the callback anywhere after the name, and the ceiling either
    // as a bare number or as `{ timeout }` — `it(name, fn, 500)` and
    // `it(name, { timeout: 500 }, fn)` are both supported, so neither position
    // may be assumed.
    const body = call.arguments.find(
      (argument) =>
        ts.isArrowFunction(argument) || ts.isFunctionExpression(argument),
    );
    if (!body) return;

    const unresolved: string[] = [];
    let ceilingMs = globalCeilingMs;
    let ceilingIsStated = false;
    const stated =
      call.arguments.find(
        (argument) =>
          ts.isNumericLiteral(argument) || ts.isIdentifier(argument),
      ) ??
      optionArguments(call)
        .map(timeoutExpression)
        .find((expression) => expression !== undefined);
    if (stated) {
      const folded = value(stated);
      if (folded === undefined) unresolved.push(stated.getText());
      else {
        ceilingMs = folded;
        ceilingIsStated = true;
      }
    }

    const title = call.arguments[0];
    const reading = budgetOf(body, consts, helpers, new Set(), new Map());
    found.push({
      file,
      line:
        source.getLineAndCharacterOfPosition(call.getStart(source)).line + 1,
      name: title && ts.isStringLiteralLike(title) ? title.text : "<computed>",
      waiterBudgetMs: reading.ms,
      ceilingMs,
      ceilingIsStated,
      unresolved: [...unresolved, ...reading.unresolved],
    });
  };

  const visit = (node: ts.Node): void => {
    if (ts.isCallExpression(node) && TEST_CALLEE.test(rootCallee(node)))
      record(node);
    ts.forEachChild(node, visit);
  };
  visit(source);
  return found;
}
