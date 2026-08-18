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

/**
 * The modifiers that mean vitest never runs the callback. A skipped body spends
 * nothing, so letting one into the population lets a test that cannot fail set
 * the ceiling for the thousands that do — or redden the guard while naming a
 * test nobody executes.
 *
 * `runIf` and `skipIf` are NOT here: they run depending on a condition, so a
 * test written either way really executes and really owes a budget. Treating
 * them as skipped left a live test under no guard at all, which is the more
 * expensive mistake of the two.
 */
const NOT_RUN = /^(skip|todo)$/;

/** Whether any link in the call chain says "do not run this". */
function isSkipped(node: ts.Node): boolean {
  let current: ts.Node = node;
  for (;;) {
    if (ts.isCallExpression(current)) current = current.expression;
    else if (ts.isTaggedTemplateExpression(current)) current = current.tag;
    else if (ts.isPropertyAccessExpression(current)) {
      if (NOT_RUN.test(current.name.text)) return true;
      current = current.expression;
    } else return false;
  }
}

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
  // Every value each name is ever given, not the last one. This reader has no
  // scope information, so two `describe` blocks that each declare their own
  // `SETTLE_MS`, or a module-level one shadowed by a function-local, were
  // collapsed to whichever came last in source order — the same last-wins
  // defect `exportedConsts` fixes across a module boundary, one hop closer.
  // A name given two different values resolves to nothing, and a waiter that
  // needed it is reported rather than folded to the wrong number.
  const seen = new Map<string, Set<number>>();
  for (const [name, resolved] of seeded) seen.set(name, new Set([resolved]));
  const single = (): Map<string, number> => {
    const settled = new Map<string, number>();
    for (const [name, values] of seen) {
      const only = values.size === 1 ? [...values][0] : undefined;
      if (only !== undefined) settled.set(name, only);
    }
    return settled;
  };
  const visit = (node: ts.Node): void => {
    if (
      ts.isVariableDeclaration(node) &&
      ts.isIdentifier(node.name) &&
      node.initializer
    ) {
      const resolved = resolver(single())(node.initializer);
      if (resolved !== undefined) {
        const values = seen.get(node.name.text);
        if (values) values.add(resolved);
        else seen.set(node.name.text, new Set([resolved]));
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return single();
}

function parse(file: string): ts.SourceFile {
  return ts.createSourceFile(
    file,
    readFileSync(file, "utf8"),
    ts.ScriptTarget.Latest,
    true,
  );
}

/**
 * The numbers a module EXPORTS at its top level. Not `declaredConsts`, which
 * walks a whole file flat and lets the last declaration of a name win: a
 * function-local `const POLL_MS = 1` in the imported module then overwrote the
 * exported `POLL_MS = 5000`, and a 10000ms budget folded to 2ms with nothing
 * reported.
 */
function exportedConsts(source: ts.SourceFile): Map<string, number> {
  const known = new Map<string, number>();
  const value = resolver(known);
  for (const statement of source.statements) {
    if (!ts.isVariableStatement(statement)) continue;
    const exported = statement.modifiers?.some(
      (modifier) => modifier.kind === ts.SyntaxKind.ExportKeyword,
    );
    if (!exported) continue;
    for (const declaration of statement.declarationList.declarations) {
      if (!ts.isIdentifier(declaration.name) || !declaration.initializer)
        continue;
      const resolved = value(declaration.initializer);
      if (resolved !== undefined) known.set(declaration.name.text, resolved);
    }
  }
  return known;
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
  const theirs = exportedConsts(parse(target));
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

/**
 * Whether a property key names the timeout, however it is written. Comparing
 * source text read `{ "timeout": 5000 }` and `{ ["timeout"]: 5000 }` as "states
 * no timeout", which is the silent-default path this file exists to close — and
 * it would have rested on a lint rule the reader knows nothing about.
 */
function isTimeoutKey(name: ts.PropertyName): boolean {
  if (ts.isIdentifier(name) || ts.isStringLiteralLike(name)) {
    return name.text === "timeout";
  }
  if (
    ts.isComputedPropertyName(name) &&
    ts.isStringLiteralLike(name.expression)
  ) {
    return name.expression.text === "timeout";
  }
  return false;
}

/**
 * The `timeout:` of one options object — `{ timeout: 4000 }` and the shorthand
 * `{ timeout }` alike, since the shorthand is the idiomatic spelling once the
 * value has a name. A spread carries properties this reader cannot see, so the
 * whole object is reported unreadable rather than searched and found wanting.
 */
function timeoutExpression(
  options: ts.ObjectLiteralExpression,
): ts.Expression | typeof OPAQUE | undefined {
  for (const property of options.properties) {
    if (ts.isSpreadAssignment(property)) return OPAQUE;
    if (ts.isShorthandPropertyAssignment(property)) {
      if (property.name.text === "timeout") return property.name;
      continue;
    }
    if (!ts.isPropertyAssignment(property)) continue;
    if (!isTimeoutKey(property.name)) continue;
    return property.initializer;
  }
  return undefined;
}

/**
 * The `timeout:` a waiter states, wherever among its arguments it sits — or
 * OPAQUE when an argument might carry one this reader cannot read. Only
 * arguments after the first are considered: a waiter's first argument is its
 * matcher or callback, and an options object never sits there.
 */
function statedTimeout(
  call: ts.CallExpression,
): ts.Expression | typeof OPAQUE | undefined {
  let found: ts.Expression | typeof OPAQUE | undefined;
  for (const argument of call.arguments.slice(1)) {
    if (cannotCarryOptions(argument)) continue;
    if (!ts.isObjectLiteralExpression(argument)) return OPAQUE;
    const stated = timeoutExpression(argument);
    if (stated === OPAQUE) return OPAQUE;
    if (stated !== undefined && found === undefined) found = stated;
  }
  return found;
}

/**
 * An options argument this reader cannot see inside — a spread, or an object
 * handed over by name. Distinguished from "states no timeout" because the two
 * mean opposite things: one is a default budget, the other is a budget nobody
 * checked, and collapsing them is what let an 11000ms test read as 4000.
 */
const OPAQUE = Symbol("an options argument this reader cannot read");

/**
 * Shapes that definitively CANNOT be an options object carrying a timeout — a
 * matcher, a placeholder, a callback. Everything else is treated as possibly
 * carrying one, because the default has to be "unreadable", not "absent".
 *
 * That direction is the whole lesson of this file. A whitelist of shapes the
 * reader understands sends every other shape down the same path as "states no
 * timeout", and that path ends at the 1000ms default — the smallest number,
 * which is the one that keeps the gate green. Property access, a call, a
 * ternary, an `as` cast and a `!` assertion all reached it that way.
 */
function cannotCarryOptions(argument: ts.Expression): boolean {
  if (ts.isIdentifier(argument)) return argument.text === "undefined";
  switch (argument.kind) {
    case ts.SyntaxKind.NullKeyword:
    case ts.SyntaxKind.TrueKeyword:
    case ts.SyntaxKind.FalseKeyword:
      return true;
    default:
      break;
  }
  return (
    ts.isStringLiteralLike(argument) ||
    ts.isNumericLiteral(argument) ||
    ts.isRegularExpressionLiteral(argument) ||
    ts.isArrayLiteralExpression(argument) ||
    ts.isArrowFunction(argument) ||
    ts.isFunctionExpression(argument)
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

/**
 * The immediate name a call uses. Good enough to spot a waiter, which is always
 * reached through an object (`screen.findByText`) or bare (`waitFor`) — but a
 * HELPER is only ever called by its bare name, so the caller checks that too.
 * Without it `userEvent.clear(input)` was charged the whole budget of a
 * file-declared `clear` helper, and phantom budget inflates the very
 * measurement that sets the suite's ceiling.
 */
function directCallee(call: ts.CallExpression): string {
  const expression = call.expression;
  if (ts.isPropertyAccessExpression(expression)) return expression.name.text;
  if (ts.isIdentifier(expression)) return expression.text;
  return "";
}

type Reading = { ms: number; unresolved: string[] };

/**
 * The helpers one file declares, EVERY declaration of each name. Two suites in
 * one file commonly declare their own `mount` or `settle`, and this reader has
 * no scope information to tell a call which one it meant. Keeping only the last
 * silently reported suite A's 8000ms helper as suite B's 2000ms one.
 *
 * Holding all of them lets the disagreement decide: when every declaration of a
 * name costs the same, which is the ordinary case, the answer is that cost
 * whichever one was meant. Only when they differ is the call genuinely
 * unreadable, and only then is it reported.
 */
type Helpers = Map<string, ts.Node[]>;

/**
 * Whether this node is the DEFINITION of a helper, rather than a call to one.
 * The initializer has to BE a function: a local variable that merely shares a
 * module-level helper's name — `const renderAs = await screen.findByText(…)` —
 * is not a definition, and skipping it dropped its waiter entirely.
 */
function declaresHelper(node: ts.Node, helpers: Helpers): boolean {
  if (
    ts.isVariableDeclaration(node) &&
    ts.isIdentifier(node.name) &&
    node.initializer &&
    (ts.isArrowFunction(node.initializer) ||
      ts.isFunctionExpression(node.initializer))
  ) {
    return helpers.has(node.name.text);
  }
  if (ts.isFunctionDeclaration(node) && node.name) {
    return helpers.has(node.name.text);
  }
  return false;
}

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
  helpers: Helpers,
  path: Set<string>,
  memo: Map<string, Reading>,
): Reading {
  const value = resolver(consts);
  let ms = 0;
  const unresolved: string[] = [];

  const helperCost = (name: string): Reading => {
    const cached = memo.get(name);
    if (cached) return cached;
    // Recursion guard. A helper reached while it is already being computed
    // contributes nothing to THIS reading, and that truncated answer is not
    // cached, which keeps a later DIRECT call from inheriting the under-count.
    //
    // It does not make mutual recursion order-independent, and saying so would
    // be claiming more than the code does: with `a()` calling `b()` and `b()`
    // calling `a()`, the outermost reading memoized still embeds a truncated
    // inner one, so `await b(); await a()` and `await a(); await b()` differ.
    // Both under-count, which is the direction that lets a violation through
    // rather than inventing one. Mutually recursive helpers do not occur in
    // this tree; if they arrive, the honest fix is to key the memo on the
    // path rather than on the name.
    if (path.has(name)) return { ms: 0, unresolved: [] };
    const bodies = helpers.get(name);
    if (!bodies || bodies.length === 0) return { ms: 0, unresolved: [] };
    const outermost = path.size === 0;
    path.add(name);
    const readings = bodies.map((body) =>
      budgetOf(body, consts, helpers, path, memo),
    );
    path.delete(name);
    const costs = new Set(readings.map((reading) => reading.ms));
    const reading: Reading =
      costs.size === 1
        ? {
            ms: readings[0]?.ms ?? 0,
            unresolved: readings.flatMap((one) => one.unresolved),
          }
        : {
            ms: 0,
            unresolved: [
              `${name}() is declared ${bodies.length} times in this file with different waiter budgets (${[...costs].sort((a, b) => a - b).join("ms, ")}ms)`,
            ],
          };
    if (outermost) memo.set(name, reading);
    return reading;
  };

  const takeWaiter = (call: ts.CallExpression): void => {
    const stated = statedTimeout(call);
    if (stated === undefined) {
      ms += DEFAULT_WAITER_MS;
      return;
    }
    if (stated === OPAQUE) {
      unresolved.push(call.getText().replace(/\s+/g, " ").slice(0, 80));
      return;
    }
    const folded = value(stated);
    if (folded === undefined) unresolved.push(stated.getText());
    else ms += folded;
  };

  const takeHelper = (name: string): void => {
    const reading = helperCost(name);
    ms += reading.ms;
    for (const item of reading.unresolved) {
      if (!unresolved.includes(item)) unresolved.push(item);
    }
  };

  const visit = (current: ts.Node): void => {
    // A helper DECLARED inside the body being read is counted at each call, so
    // descending into its definition as well would charge the test once more
    // for a waiter it may never reach.
    if (declaresHelper(current, helpers)) return;
    if (ts.isCallExpression(current)) {
      const name = directCallee(current);
      if (WAITER.test(name)) takeWaiter(current);
      else if (ts.isIdentifier(current.expression) && helpers.has(name))
        takeHelper(name);
    }
    ts.forEachChild(current, visit);
  };
  // The node itself, not only its children: a one-line arrow helper's body IS
  // the waiter call, and descending past it counts nothing.
  visit(node);
  return { ms, unresolved };
}

/**
 * The ceiling a test states for itself, in either form vitest accepts:
 * `it(name, fn, 500)` and `it(name, { timeout: 500 }, fn)`. The bare number is
 * only ever AFTER the callback — scanning from argument 0 reads a non-string
 * title as the ceiling, and then reports a test's own name as a timeout the
 * reader cannot fold.
 */
function statedCeiling(
  call: ts.CallExpression,
  body: ts.Expression,
): ts.Expression | typeof OPAQUE | undefined {
  // Any expression after the callback, not only a literal or a name: a ceiling
  // written as `SETTLE_MS * 4` was neither folded NOR reported, so the test
  // silently rejoined the population it had opted out of.
  const positional = call.arguments
    .slice(call.arguments.indexOf(body) + 1)
    .find(
      (argument) =>
        !ts.isObjectLiteralExpression(argument) &&
        !ts.isArrowFunction(argument) &&
        !ts.isFunctionExpression(argument),
    );
  if (positional) return positional;
  return call.arguments
    .filter((argument) => ts.isObjectLiteralExpression(argument))
    .map((argument) => timeoutExpression(argument))
    .find((expression) => expression !== undefined);
}

/** Named `function f() {}` and `const f = () => {}` bodies, by name, anywhere. */
function namedHelpers(source: ts.SourceFile): Helpers {
  const byName: Helpers = new Map();
  const remember = (name: string, body: ts.Node): void => {
    const bodies = byName.get(name);
    if (bodies) bodies.push(body);
    else byName.set(name, [body]);
  };
  const visit = (node: ts.Node): void => {
    if (ts.isFunctionDeclaration(node) && node.name && node.body) {
      remember(node.name.text, node.body);
    } else if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name)) {
      const initializer = node.initializer;
      if (
        initializer &&
        (ts.isArrowFunction(initializer) ||
          ts.isFunctionExpression(initializer))
      ) {
        remember(node.name.text, initializer.body);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return byName;
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
    // The bare number is only ever AFTER the callback: `it(name, fn, ms)`.
    // Scanning from argument 0 reads a non-string title as the ceiling, and
    // then reports a test's own name as a timeout it cannot fold.
    const stated = statedCeiling(call, body);
    if (stated === OPAQUE) {
      unresolved.push("an options object this reader cannot read");
    } else if (stated) {
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

  // `skipped` is carried DOWN the tree: `describe.skip` suppresses every test
  // inside it, and a reader that only inspected the `it` chain reddened the
  // guard naming a test vitest never runs — the same failure isSkipped was
  // added to prevent, one level up.
  const visit = (node: ts.Node, skipped: boolean): void => {
    let inherited = skipped;
    if (ts.isCallExpression(node)) {
      const root = rootCallee(node);
      const here = skipped || isSkipped(node);
      if (root === "describe") inherited = here;
      else if (TEST_CALLEE.test(root) && !here) record(node);
    }
    ts.forEachChild(node, (child) => visit(child, inherited));
  };
  visit(source, false);
  return found;
}
