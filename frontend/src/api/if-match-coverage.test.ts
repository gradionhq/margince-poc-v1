import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";

// Fitness function for the lost update: a write to an endpoint that takes an
// `If-Match` precondition, sent without one.
//
// Two people move the same deal at the same moment. Both requests carry a stage
// and no precondition, both succeed, and the second silently replaces a change
// its author never saw — no conflict, no 409, no trace. The server offers the
// guard on every mutating endpoint that returns a versioned entity; omitting it
// is last-write-wins, and last-write-wins is not something a UI gets to choose
// by accident.
//
// The obligation is DERIVED, not listed: which endpoints take the precondition
// comes out of `schema.d.ts`, generated from the contract. Add a versioned
// endpoint upstream, call it from a screen, forget the header, and this fails —
// nobody has to remember to extend a list.
//
// WHAT THIS DOES NOT CATCH, deliberately: a call whose path is an expression
// rather than a literal (`api.POST(path, …)`, as the approvals inbox does to
// pick approve-vs-reject). Resolving those needs the type checker, and the inbox
// is also the one place a missing precondition is correct — the server re-admits
// the edited payload from scratch, and its endpoints declare no `If-Match` at
// all, so nothing here has an opinion about them.

const apiDir = dirname(fileURLToPath(import.meta.url));
const srcRoot = join(apiDir, "..");

// The writes that do not pin the row they overwrite yet — every one of them the
// same defect the deal board had, in a screen this change did not open. Listed
// so the class stays visible and the count can only fall: a NEW unpinned write
// fails the first test below, and a fixed one fails the second until its line
// here is deleted. Nothing keeps a stale entry alive.
const UNPINNED_WRITES: readonly string[] = [
  "screens/automations.tsx PATCH /automations/{id}",
  "screens/contractform.tsx PATCH /contracts/{id}",
  "screens/customfields.tsx PATCH /custom-fields/{id}",
  "screens/evidenceverdict.tsx PATCH /organizations/{id}/facts/{factKey}",
  "screens/evidenceverdict.tsx PATCH /organizations/{id}/profile-fields/{field}",
  "screens/evidenceverdict.tsx POST /organizations/{id}/facts/{factKey}/confirm",
  "screens/evidenceverdict.tsx POST /organizations/{id}/profile-fields/{field}/confirm",
  "screens/extension-access.tsx PATCH /roles/{key}/objects/{object}",
  "screens/integrations-provider.tsx PATCH /provider-connections/{provider}",
  "screens/settings.tsx DELETE /stages/{id}",
  "screens/share.tsx DELETE /record-grants/{id}",
  "screens/taskactions.tsx PATCH /activities/{id}",
  "screens/tasks.tsx PATCH /activities/{id}",
  "screens/voice-dna.tsx DELETE /voice-profiles/{id}/sources/{sourceId}",
  "screens/voice-dna.tsx PATCH /voice-profiles/{id}",
];

const HTTP_METHODS: readonly string[] = [
  "get",
  "put",
  "post",
  "patch",
  "delete",
];

const schemaSource = ts.createSourceFile(
  "schema.d.ts",
  readFileSync(join(apiDir, "schema.d.ts"), "utf8"),
  ts.ScriptTarget.Latest,
  true,
  ts.ScriptKind.TS,
);

function declaration(name: string): ts.InterfaceDeclaration {
  let found: ts.InterfaceDeclaration | undefined;
  schemaSource.forEachChild((node) => {
    if (ts.isInterfaceDeclaration(node) && node.name.text === name) {
      found = node;
    }
  });
  if (!found) {
    throw new Error(`schema.d.ts declares no \`${name}\` interface`);
  }
  return found;
}

function memberName(member: ts.TypeElement): string | null {
  const name = member.name;
  if (!name) {
    return null;
  }
  return ts.isIdentifier(name) || ts.isStringLiteral(name) ? name.text : null;
}

function memberType(
  member: ts.TypeElement | undefined,
): ts.TypeNode | undefined {
  return member && ts.isPropertySignature(member) ? member.type : undefined;
}

/** The members of `<member>: { … }`, and none where it is not an object type. */
function fields(member: ts.TypeElement | undefined): readonly ts.TypeElement[] {
  const type = memberType(member);
  return type && ts.isTypeLiteralNode(type) ? type.members : [];
}

function field(
  member: ts.TypeElement | undefined,
  name: string,
): ts.TypeElement | undefined {
  return fields(member).find((inner) => memberName(inner) === name);
}

/** The operation ids whose contract declares the `If-Match` request header. */
function conditionalOperations(): ReadonlySet<string> {
  const ids = new Set<string>();
  for (const operation of declaration("operations").members) {
    const name = memberName(operation);
    const header = field(field(operation, "parameters"), "header");
    if (name && field(header, "If-Match")) {
      ids.add(name);
    }
  }
  return ids;
}

/** `METHOD /path` for every route whose operation takes the precondition. */
function conditionalEndpoints(): ReadonlySet<string> {
  const operations = conditionalOperations();
  const endpoints = new Set<string>();
  for (const route of declaration("paths").members) {
    const path = memberName(route);
    for (const verb of fields(route)) {
      const method = memberName(verb);
      const type = memberType(verb);
      if (!path || !method || !type || !ts.isIndexedAccessTypeNode(type)) {
        continue;
      }
      const index = type.indexType;
      if (!ts.isLiteralTypeNode(index) || !ts.isStringLiteral(index.literal)) {
        continue;
      }
      if (operations.has(index.literal.text)) {
        endpoints.add(`${method.toUpperCase()} ${path}`);
      }
    }
  }
  return endpoints;
}

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      return sourceFiles(path);
    }
    return /\.tsx?$/.test(entry.name) ? [path] : [];
  });
}

/**
 * `METHOD /path` when this node is an `api.<METHOD>("<literal>", …)` call that
 * spreads no `ifMatch(…)` into its parameters, and null otherwise.
 */
function calledEndpoint(node: ts.Node, source: ts.SourceFile): string | null {
  if (!ts.isCallExpression(node) || node.arguments.length === 0) {
    return null;
  }
  const callee = node.expression;
  if (
    !ts.isPropertyAccessExpression(callee) ||
    !ts.isIdentifier(callee.expression) ||
    callee.expression.text !== "api"
  ) {
    return null;
  }
  const [path] = node.arguments;
  const method = callee.name.text;
  if (
    !ts.isStringLiteral(path) ||
    !HTTP_METHODS.includes(method.toLowerCase())
  ) {
    return null;
  }
  return /\bifMatch\(/.test(node.getText(source))
    ? null
    : `${method.toUpperCase()} ${path.text}`;
}

/** `<file> METHOD /path` for every call in one file that sends no precondition. */
function unpinnedIn(file: string, conditional: ReadonlySet<string>): string[] {
  const source = ts.createSourceFile(
    file,
    readFileSync(file, "utf8"),
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TSX,
  );
  const found: string[] = [];
  const visit = (node: ts.Node) => {
    const endpoint = calledEndpoint(node, source);
    if (endpoint && conditional.has(endpoint)) {
      found.push(`${relative(srcRoot, file)} ${endpoint}`);
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return found;
}

function unpinnedWrites(): string[] {
  const conditional = conditionalEndpoints();
  const files = sourceFiles(srcRoot).filter((file) =>
    // A file that never names the client cannot call it, and parsing it anyway
    // is what this gate would otherwise cost: a whole-tree parse that grows with
    // the SPA rather than with the number of writes.
    readFileSync(file, "utf8").includes("api."),
  );
  // Both counts, because either one reaching zero passes every assertion below
  // while comparing nothing — a contract that yielded no endpoints and a sweep
  // that found no files look identical from the outside, and both look green.
  expect(conditional.size).toBeGreaterThan(0);
  expect(files.length).toBeGreaterThan(0);
  return files.flatMap((file) => unpinnedIn(file, conditional)).sort();
}

// A synchronous parse of the contract types plus every file that names the
// client: bounded and deterministic, so it states a budget a loaded runner
// cannot exhaust rather than living on vitest's 5s default, which is sized for
// an async UI test.
const PARSE_MS = 60_000;

describe("conditional writes", () => {
  it(
    "send If-Match on every endpoint that takes one",
    () => {
      const unlisted = unpinnedWrites().filter(
        (write) => !UNPINNED_WRITES.includes(write),
      );
      expect(unlisted, `\n${unlisted.join("\n")}\n`).toEqual([]);
    },
    PARSE_MS,
  );

  it(
    "keep no entry for a write that now pins its row",
    () => {
      const found = unpinnedWrites();
      const fixed = UNPINNED_WRITES.filter((write) => !found.includes(write));
      expect(fixed, `\n${fixed.join("\n")}\n`).toEqual([]);
    },
    PARSE_MS,
  );
});
