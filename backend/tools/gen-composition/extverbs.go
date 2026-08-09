// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The contract → declaration direction, and the first consumer of the merged
// contracts contracts.go produces.
//
// Every governed operation an extension publishes is read back OUT of
// build/composition/api/*.yaml — the effective contract, base plus fragments —
// rather than out of the fragment files. That is deliberate and it is the whole
// point of the `make gen` ordering Task 9 inverted: what the manifest records
// and what the boot serves must be derived from the document a client will be
// handed, not from an input that a merge could still have refused, reordered or
// namespaced differently. Read the merged file and the two cannot disagree.
//
// What comes back is a set of extension.Verb values, which then go two places:
//   - the unit manifest (unitmanifest.go), as the risk tiers an operator resolves;
//   - extensions_gen.go (emit.go), re-emitted as Go LITERALS, because the boot
//     refusals in compose/extensiontools.go read Tier, RequestedScope and
//     Description and must keep refusing inside a bare binary that ships no
//     repository. See extension.Verb.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// mcpToolExtension is the operation-level annotation carrying the governance a
// contract operation requests. Core operations already use this key (its verb,
// tier and scope spellings are the same); an extension operation additionally
// declares the version, title and description that used to sit in Go.
const mcpToolExtension = "x-mcp-tool"

// declaredVerb is one governed extension operation plus the provenance the
// manifest digest covers but the running process has no use for.
type declaredVerb struct {
	verb extension.Verb
	// fragmentHash is the sha256 of the operation's own merged bytes — the
	// whole node under the method key, canonically re-encoded. It is the
	// SECURITY-RELEVANT part of the declaration that the four descriptor
	// fields do not cover: a fragment that keeps its id, route, method, tier
	// and scope while changing its request schema, its response shape or the
	// prose a model selects it by is a different published thing, and an
	// operator resolution recorded against the old one should not carry.
	fragmentHash string
}

// extensionVerbs reads every enabled unit's governed operations out of the
// merged contracts. contracts is keyed by base filename, exactly as
// composedContracts returns it, so this reads the same bytes that are written
// to build/composition/api/.
//
// Result order is (unit, contract, route, method) — deterministic and
// independent of map iteration, because it is emitted into generated Go and
// hashed into committed manifests.
func extensionVerbs(units []extensionUnit, contracts map[string][]byte) ([]declaredVerb, error) {
	var out []declaredVerb
	for _, base := range composedContractBases {
		raw, ok := contracts[base]
		if !ok {
			return nil, fmt.Errorf("no composed contract for %s", base)
		}
		found, err := verbsInContract(base, units, raw)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", apiLayer, base, err)
		}
		out = append(out, found...)
	}
	sort.Slice(out, func(i, j int) bool { return verbKey(out[i].verb) < verbKey(out[j].verb) })
	return out, nil
}

// verbKey is the total order verbs are emitted and compared in. Route and
// method are enough to be unique — one operation per method per path is
// OpenAPI's own rule — and the unit prefix keeps a unit's operations together
// for a reader.
func verbKey(v extension.Verb) string {
	return string(v.Unit) + "\x00" + v.Contract + "\x00" + v.Route + "\x00" + v.Method
}

// verbsInContract walks one merged contract's paths for the routes the enabled
// units own. A path under /v1/ext/ belonging to no enabled unit is an error
// rather than a skip: it can only mean the base contract itself declared one
// (the composer refuses a fragment outside its own namespace), and a core
// route in the extension namespace would be served by nothing and attributed
// to nobody.
func verbsInContract(base string, units []extensionUnit, raw []byte) ([]declaredVerb, error) {
	var doc struct {
		Paths yaml.Node `yaml:"paths"`
	}
	// Not KnownFields: a core contract carries dozens of top-level blocks this
	// reader has no business knowing about. The strict reads are on the
	// fragment (parseOverlay) and on the annotation below, which are the parts
	// a unit author writes.
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Paths.IsZero() {
		// A contract with no route surface at all (jobs.yaml, ai-tasks.yaml)
		// declares no extension route. Not an error: those contracts carry
		// kinds and tasks, which later capability seams read.
		return nil, nil
	}
	if doc.Paths.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("paths is present but is not a mapping — nothing here can be read as a route")
	}
	owners := make(map[string]bool, len(units))
	for _, u := range units {
		owners[u.Name] = true
	}
	var out []declaredVerb
	for i := 0; i+1 < len(doc.Paths.Content); i += 2 {
		route := doc.Paths.Content[i].Value
		if !strings.HasPrefix(route, extensionRoutePrefix) {
			continue
		}
		unit := routeUnit(route)
		if !owners[unit] {
			return nil, fmt.Errorf("route %s is in the extension namespace but no enabled unit owns it", route)
		}
		found, err := verbsInPathItem(base, unit, route, doc.Paths.Content[i+1])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", route, err)
		}
		out = append(out, found...)
	}
	return out, nil
}

// extensionRoutePrefix is the route namespace every extension operation lives
// under, spelled once. checkRouteNamespace in contractmerge.go enforces it on
// the way in; this is the same wall read from the other side.
const extensionRoutePrefix = extension.RoutePrefix

// routeUnit takes the unit name out of an extension route. The name is a
// single path segment, so this is exact, not a prefix guess.
func routeUnit(route string) string {
	rest := strings.TrimPrefix(route, extensionRoutePrefix)
	if cut := strings.IndexByte(rest, '/'); cut >= 0 {
		return rest[:cut]
	}
	return rest
}

// httpMethodKeys are the path-item keys that are OPERATIONS. A path item also
// carries non-operation keys (parameters, summary, servers), and a reader that
// treated those as operations would demand an x-mcp-tool on them.
var httpMethodKeys = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func verbsInPathItem(base, unit, route string, item *yaml.Node) ([]declaredVerb, error) {
	if item.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the path item is not a mapping")
	}
	var out []declaredVerb
	for i := 0; i+1 < len(item.Content); i += 2 {
		key := item.Content[i].Value
		if !slices.Contains(httpMethodKeys, key) {
			continue
		}
		v, err := readOperation(base, unit, route, key, item.Content[i+1])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the path item declares no operation — a route publishing nothing is a promise to a client that resolves to a 405")
	}
	return out, nil
}

// operationDoc is the subset of an OpenAPI operation this tier reads. A whole
// operation cannot be decoded strictly — it legitimately carries summary, tags,
// parameters, security, callbacks and more, none of which this reader has any
// business knowing about. What IS strict is every part a unit author writes to
// request authority: the x-mcp-tool annotation (decodeStrict, below) and the
// SET of x- keys on the operation (checkExtensionKeys).
type operationDoc struct {
	OperationID string    `yaml:"operationId"`
	Tool        yaml.Node `yaml:"x-mcp-tool"`
	RbacObject  string    `yaml:"x-rbac-object"` // key spelled once in rbacObjectExtension
	RbacAction  string    `yaml:"x-rbac-action"` // key spelled once in rbacActionExtension
	RequestBody yaml.Node `yaml:"requestBody"`
	Responses   yaml.Node `yaml:"responses"`
}

// toolAnnotation is the extension spelling of x-mcp-tool, read strictly: a
// misspelled key here is not a missing request but a DIFFERENT one — a typo'd
// `tier` would fall back to nothing and be refused, but a typo'd `scope` on a
// unit that meant `send` would look like a read.
//
// It carries no record_type (core's x-mcp-tool has one because a core verb is
// parameterised by record kind; an extension verb is its own operation).
type toolAnnotation struct {
	Verb        string `yaml:"verb"`
	Version     string `yaml:"version"`
	Title       string `yaml:"title"`
	Tier        string `yaml:"tier"`
	Scope       string `yaml:"scope"`
	Description string `yaml:"description"`
}

func readOperation(base, unit, route, method string, node *yaml.Node) (declaredVerb, error) {
	var op operationDoc
	if err := node.Decode(&op); err != nil {
		return declaredVerb{}, err
	}
	if err := checkExtensionKeys(node); err != nil {
		return declaredVerb{}, err
	}
	if op.Tool.IsZero() {
		// Fail closed. An extension operation with no x-mcp-tool would publish
		// a route this tier has no way to serve — routes are mounted onto tool
		// invocations (compose/extroutes.go) — so it would be a documented
		// endpoint answering 404 forever. When a non-tool extension route
		// becomes a thing, it needs a registration seam first, and this is
		// where the author is told so.
		return declaredVerb{}, fmt.Errorf("the operation declares no %s — an extension operation is served as a governed tool invocation, so a route with no tool verb can be registered by nothing", mcpToolExtension)
	}
	ann, err := decodeStrict[toolAnnotation](&op.Tool)
	if err != nil {
		return declaredVerb{}, fmt.Errorf("%s: %w", mcpToolExtension, err)
	}
	input, err := requestSchema(&op.RequestBody)
	if err != nil {
		return declaredVerb{}, err
	}
	output, err := responseSchema(&op.Responses)
	if err != nil {
		return declaredVerb{}, err
	}
	v := extension.Verb{
		Unit:           extension.Name(unit),
		Contract:       base,
		OperationID:    op.OperationID,
		Route:          route,
		Method:         strings.ToUpper(method),
		Tool:           ann.Verb,
		Title:          ann.Title,
		Description:    strings.TrimSpace(ann.Description),
		Version:        ann.Version,
		Tier:           extension.Tier(ann.Tier),
		RequestedScope: extension.Scope(ann.Scope),
		InputSchema:    input,
		OutputSchema:   output,
		RbacObject:     op.RbacObject,
		RbacAction:     extension.RbacAction(op.RbacAction),
	}
	// The SAME Validate the boot runs, so a fragment this generator accepts
	// can never be one the composed process then refuses to serve.
	if err := v.Validate(); err != nil {
		return declaredVerb{}, err
	}
	hash, err := operationHash(node)
	if err != nil {
		return declaredVerb{}, err
	}
	return declaredVerb{verb: v, fragmentHash: hash}, nil
}

// readExtensionKeys are the x- annotations an extension operation may carry.
// The set is closed and short on purpose; see checkExtensionKeys.
var readExtensionKeys = []string{mcpToolExtension, rbacObjectExtension, rbacActionExtension}

// rbacObjectExtension names the RBAC object an extension operation gates on.
const rbacObjectExtension = "x-rbac-object"

// rbacActionExtension names the verb the grant on that object must carry. It
// is a second key rather than a compound value because the two are checked
// against two different vocabularies — a unit-namespaced object name, and the
// closed four-verb action set — and one string holding both would have to be
// split before either could be validated.
const rbacActionExtension = "x-rbac-action"

// checkExtensionKeys refuses an x- key this reader does not act on.
//
// The same argument that makes x-mcp-tool a strict decode, applied one level
// out — and it has to be, because the failure mode here is worse. A fragment
// writing `x-rbac-objects` would decode to the empty string, register no object,
// and look fine at generation time. The damage lands later and somewhere else:
// a stored role document granting the object makes policy.Parse reject the
// document, which fails that user's ENTIRE identity resolution — not the one
// screen the unit shipped. A typo in a fragment must not be able to lock a
// person out of the product.
//
// Closed rather than "check the ones we know": an operation carrying, say,
// x-agent-access would be stating an authority posture this tier does not read,
// and silently publishing it is the same class of lie. When a unit needs another
// annotation, this list gains a reviewed line.
func checkExtensionKeys(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("the operation is not a mapping")
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !strings.HasPrefix(key, "x-") || slices.Contains(readExtensionKeys, key) {
			continue
		}
		return fmt.Errorf("the operation carries %s, which this generator does not read — an annotation nothing acts on is published and ignored (an extension operation may declare %s)",
			key, strings.Join(readExtensionKeys, ", "))
	}
	return nil
}

// decodeStrict reads a yaml.Node into T with KnownFields(true). node.Decode
// alone would NOT be strict: it builds a decoder that carries yaml.v3's
// uniqueKeys default but drops KnownFields, so an unrecognised key inside the
// annotation would be silently dropped. See gen-aitasks/strictdecode.go, which
// documents the same hole for the same reason.
func decodeStrict[T any](node *yaml.Node) (T, error) {
	var zero T
	raw, err := yaml.Marshal(node)
	if err != nil {
		return zero, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var out T
	if err := dec.Decode(&out); err != nil {
		return zero, err
	}
	return out, nil
}

// requestSchema reads the operation's inline JSON request schema. A $ref is
// refused BY NAME rather than resolved: this generator does not walk
// references, and a silently unresolved one would advertise `{"$ref": …}` to a
// model as the tool's argument shape.
func requestSchema(body *yaml.Node) (json.RawMessage, error) {
	if body.IsZero() {
		return nil, fmt.Errorf("the operation declares no requestBody — a served extension operation is a tool invocation and its arguments are the body")
	}
	schema := yamlChild(yamlChild(yamlChild(body, "content"), "application/json"), "schema")
	if schema == nil {
		return nil, fmt.Errorf("the operation's requestBody declares no application/json schema")
	}
	return jsonSchema("requestBody", schema)
}

// responseSchema reads the 200 response's inline JSON schema. Absent is
// allowed — a tool may return nothing describable — but a $ref is refused for
// the same reason as the request's.
func responseSchema(responses *yaml.Node) (json.RawMessage, error) {
	schema := yamlChild(yamlChild(yamlChild(yamlChild(responses, "200"), "content"), "application/json"), "schema")
	if schema == nil {
		return nil, nil
	}
	return jsonSchema("response 200", schema)
}

func jsonSchema(where string, node *yaml.Node) (json.RawMessage, error) {
	// Recursive, not a check on the root. The emitted literal IS the standalone
	// schema an MCP client hands a model, so a `$ref` anywhere inside it — one
	// property, one array's items, one branch of a oneOf — arrives as an
	// argument shape nothing can resolve: the client has no document to resolve
	// it against, and this generator does not walk references to inline it. The
	// root-only check refused the obvious spelling and passed every nested one,
	// which is the shape a real fragment is more likely to have.
	if path, found := findRef(node, ""); found {
		return nil, fmt.Errorf("%s schema declares a $ref at %s — declare an extension operation's schema inline; this generator does not resolve references, and an unresolved one would be advertised to a model as the argument shape", where, path)
	}
	var doc any
	if err := node.Decode(&doc); err != nil {
		return nil, err
	}
	// Marshalled with sorted keys by encoding/json's map handling, so the
	// emitted literal and the hashed bytes are stable across YAML key order.
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("%s schema is not expressible as JSON: %w", where, err)
	}
	return encoded, nil
}

// findRef walks a schema node for a `$ref` key at any depth, returning the
// path to the first one it finds so the refusal names a position rather than a
// document. The path is written the way a reader would say it out loud —
// `.properties.deal.$ref`, `[0].$ref` — because the point of naming it is that
// the author can go to the line.
func findRef(node *yaml.Node, path string) (string, bool) {
	if node == nil {
		return "", false
	}
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			if key == "$ref" {
				return path + ".$ref", true
			}
			if found, ok := findRef(node.Content[i+1], path+"."+key); ok {
				return found, true
			}
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			if found, ok := findRef(child, fmt.Sprintf("%s[%d]", path, i)); ok {
				return found, true
			}
		}
	}
	return "", false
}

// yamlChild returns a mapping's value for key, or nil. It tolerates a nil
// receiver so a missing path reads as one nil rather than four guards.
func yamlChild(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// operationHash canonicalises the operation node and hashes it. Canonical
// means re-encoded through yaml.v3 at a fixed indent: the hash must change
// when the DECLARATION changes and not when someone reflows a comment or a
// flow mapping in the fragment above it.
func operationHash(node *yaml.Node) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}
