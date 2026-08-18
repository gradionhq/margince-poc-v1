import { readFileSync } from "node:fs";
import ts from "typescript";
import { ASYNC_UTIL_TIMEOUT_MS } from "../vitest.budget";

// Reads a test file and works out, for every test in it, how long that test is
// ALLOWED to spend waiting — the sum of the budgets of the waiters it runs in
// sequence — and how long it is allowed to take. #1144 is what happens when the
// first number exceeds the second: the test fails while every waiter in it is
// still inside its own budget, and the failure names the test rather than the
// waiter that was slow.
//
// Parsed with the TypeScript compiler rather than matched with a regex. A
// hand-rolled scanner over this tree over-counted a 688-line file as one test
// with 39 waiters, and a guard that reports a number nobody believes is worse
// than no guard: it would demand a ceiling ten times larger than anything real.

/** A waiter whose budget is not stated takes Testing Library's default. */
const DEFAULT_WAITER_MS = ASYNC_UTIL_TIMEOUT_MS;

const WAITER = /^(findBy|findAllBy|waitFor|waitForElementToBeRemoved)/;

export type TestBudget = {
  file: string;
  line: number;
  name: string;
  /** The sum of the waiter budgets this test can spend, in ms. */
  waiterBudgetMs: number;
  /** The per-test ceiling this test actually runs under, in ms. */
  ceilingMs: number;
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

/** `const X = 5000` and `const Y = X * 4` anywhere, so a named budget resolves. */
function numericConsts(source: ts.SourceFile): Map<string, number> {
  const known = new Map<string, number>();
  const value = (node: ts.Expression): number | undefined => {
    if (ts.isNumericLiteral(node)) return Number(node.text);
    if (ts.isIdentifier(node)) return known.get(node.text);
    if (ts.isBinaryExpression(node)) return arithmetic(node, value);
    return undefined;
  };
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

/** A number written as a literal, or as a name this file gave a number to. */
function asNumber(
  node: ts.Expression,
  consts: Map<string, number>,
): number | undefined {
  if (ts.isNumericLiteral(node)) return Number(node.text);
  if (ts.isIdentifier(node)) return consts.get(node.text);
  return undefined;
}

/** `timeout:` in one options object, when it resolves to a number. */
function timeoutProperty(
  options: ts.ObjectLiteralExpression,
  consts: Map<string, number>,
): number | undefined {
  for (const property of options.properties) {
    if (!ts.isPropertyAssignment(property)) continue;
    if (property.name.getText() !== "timeout") continue;
    return asNumber(property.initializer, consts);
  }
  return undefined;
}

/** `{ timeout: SETTLE_MS }` in a waiter's options, wherever it sits. */
function statedTimeout(
  call: ts.CallExpression,
  consts: Map<string, number>,
): number | undefined {
  for (const argument of call.arguments) {
    if (!ts.isObjectLiteralExpression(argument)) continue;
    const stated = timeoutProperty(argument, consts);
    if (stated !== undefined) return stated;
  }
  return undefined;
}

function calleeName(call: ts.CallExpression): string {
  const expression = call.expression;
  if (ts.isPropertyAccessExpression(expression)) return expression.name.text;
  if (ts.isIdentifier(expression)) return expression.text;
  return "";
}

/**
 * The waiter budget of one node, following awaited calls into module-level
 * helpers: `renderAs()` runs waiters of its own, and a test that awaits it
 * spends them. A helper that is not module-level, or that this cannot resolve,
 * contributes nothing — the count is then an UNDER-estimate, which is the
 * direction that lets a real violation through rather than inventing one.
 */
function budgetOf(
  node: ts.Node,
  consts: Map<string, number>,
  helpers: Map<string, ts.Node>,
  seen: Set<string>,
): number {
  let total = 0;
  const visit = (current: ts.Node): void => {
    if (ts.isCallExpression(current)) {
      const name = calleeName(current);
      if (WAITER.test(name)) {
        total += statedTimeout(current, consts) ?? DEFAULT_WAITER_MS;
      } else if (helpers.has(name) && !seen.has(name)) {
        seen.add(name);
        const body = helpers.get(name);
        if (body) total += budgetOf(body, consts, helpers, seen);
      }
    }
    ts.forEachChild(current, visit);
  };
  ts.forEachChild(node, visit);
  return total;
}

/**
 * Named `function f() {}` and `const f = () => {}` bodies, by name, from
 * ANYWHERE in the file — a suite's render helper usually sits inside its own
 * `describe`, and one collected only from the top level misses exactly the
 * helpers that do the waiting.
 */
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

/** The per-test ceiling: vitest takes it as the third argument to `it`. */
function statedCeiling(
  call: ts.CallExpression,
  consts: Map<string, number>,
): number | undefined {
  const third = call.arguments[2];
  if (!third) return undefined;
  if (ts.isObjectLiteralExpression(third))
    return timeoutProperty(third, consts);
  return asNumber(third, consts);
}

export function budgetsIn(file: string, globalCeilingMs: number): TestBudget[] {
  const source = ts.createSourceFile(
    file,
    readFileSync(file, "utf8"),
    ts.ScriptTarget.Latest,
    true,
  );
  const consts = numericConsts(source);
  const helpers = namedHelpers(source);
  const found: TestBudget[] = [];

  const record = (call: ts.CallExpression): void => {
    const title = call.arguments[0];
    const body = call.arguments[1];
    if (!body || !(ts.isArrowFunction(body) || ts.isFunctionExpression(body)))
      return;
    found.push({
      file,
      line:
        source.getLineAndCharacterOfPosition(call.getStart(source)).line + 1,
      name: title && ts.isStringLiteralLike(title) ? title.text : "<computed>",
      waiterBudgetMs: budgetOf(body, consts, helpers, new Set()),
      ceilingMs: statedCeiling(call, consts) ?? globalCeilingMs,
    });
  };

  const visit = (node: ts.Node): void => {
    if (ts.isCallExpression(node) && node.arguments.length >= 2) {
      const name = calleeName(node);
      if (name === "it" || name === "test") record(node);
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return found;
}
