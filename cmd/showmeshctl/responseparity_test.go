package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The write-parity test above (TestEveryWritePathHasACLIVerb) proves
// every write path has a CLI verb. It says nothing about whether the verbs
// this program already has actually decode everything the coordinator is
// contractually obligated to send back. A required response property that
// api/openapi.yaml grows and this package's types.go does not is silently
// dropped by encoding/json (no DisallowUnknownFields anywhere in this
// package -- see types.go's own doc comment) and every caller of that
// decoded value sees its Go zero value instead, indistinguishable from a
// coordinator that legitimately sent one. TestEveryGETResponseRequiredField
// below is that second half: for every GET path this program actually
// calls, every property api/openapi.yaml marks `required` on that
// response's schema -- recursively, through every `$ref`'d object schema
// and array-of-object schema reachable from it -- must correspond to a
// json-tagged Go struct field this program actually decodes into. Add a
// required response property with no matching field and this test fails
// naming the exact path (e.g. ".nodes[].evidence.hello.signal") and the Go
// type it expected to find it on.

// getResponseTypeOverrides names, for a GET operation ("GET
// /openapi/path" exactly as api/openapi.yaml writes the path) this test's
// own AST scan cannot resolve to a decoding Go type by itself, which type
// actually decodes it. The scan's ordinary path (see
// collectCLIGetPathShapes) reads the type straight off a `var IDENT
// SomeType` declaration standing next to the `getJSON(..., &IDENT)` call
// site -- reliable for the large majority of this package's GET calls,
// which follow exactly that shape. It cannot see through client.go's other
// GET primitive, getRaw: that returns raw bytes with the decode happening
// in a *different* function (sometimes a shared helper, sometimes further
// down the same one), so every getRaw-sourced GET path needs an entry here
// instead. An entry naming a stale operation, or a Go type this package
// does not declare, fails this test's own consistency check below --
// exactly dynamicWritePathCoverage's contract, mirrored for reads.
//
// cmd_nodes.go cmdNodeGet, cmd_fppconnect.go cmdFPPConnectStatus, and
// cmd_render.go cmdRenderStatus each call getRaw then decodeSingleNode(body),
// which json.Unmarshal(body, &wrapped)s against nodeResponse
// (cmd_nodes.go's own decodeSingleNode) -- the getJSON-adjacent-var-decl
// scan never sees this decode because it happens in a function
// decodeSingleNode's caller invokes on the returned []byte, not at the
// getRaw call site itself. cmd_fpp.go cmdFPPOne (decodeSingleFPPInstance),
// cmd_resolume_status.go cmdResolumeStatusOne
// (decodeSingleResolumeInstance), and cmd_resolume_composition.go
// cmdResolumeCompositionShow follow the identical getRaw-then-decode
// shape, the last one decoding directly in the same function rather than
// through a shared helper.
var getResponseTypeOverrides = map[string]string{
	"GET /nodes/{nodeId}":                  "nodeResponse",
	"GET /fpp/{instanceId}":                "fppInstanceResponse",
	"GET /resolume/instances/{instanceId}": "resolumeInstanceResponse",
	"GET /config/resolume/composition":     "resolumeCompositionResponse",
}

// getResponseFieldExemptions is exemptWritePaths' convention mirrored for
// reads, with one deliberate difference forced by what "exempt" has to mean
// here: exemptWritePaths exempts a whole PATH (an entire endpoint this
// program has a real, stated reason to never call). A required response
// FIELD has no equivalent "never call it" escape hatch -- the CLI already
// calls every path below, it just doesn't decode everything the contract
// now requires -- so exempting the whole response type would silently
// swallow every OTHER required field that type ever grows, defeating the
// fail-closed property this whole test exists for. This table is keyed
// "goTypeName.jsonPropertyName" instead, checked at the exact point
// checkResponseSchema finds that one property missing on that one type: a
// NEW required property on an already-exempted type (Snapshot, which has
// two entries below, is the case to picture) still has no entry of its own
// and still fails. See TestFieldExemptionsAreScopedToOneProperty for a
// standing regression check on exactly that property, and this file's own
// mutation-proof notes in the PR for a one-off demonstration against a live
// mutation.
//
// Every entry below is a gap found by this test's first run, not a design
// choice: the CLI was already failing to decode these before this test
// existed, discovered by running this test the day it was written. Each is
// a known, existing gap between api/openapi.yaml and cmd/showmeshctl,
// awaiting its own fix on its own branch with its own evidence -- this
// test's job is detection, not repair, so none of these are fixed here. A
// future PR that closes one removes its entry as part of that fix, the
// same way exemptWritePaths entries come out when their gap closes.
var getResponseFieldExemptions = map[string]string{
	"node.audio": "Node.audio is required (api/openapi.yaml's Node schema) -- whatever node.audio.* " +
		"observations this coordinator holds for a node, one ObservationEntry per signal, the same shape " +
		"Node.render and Node.fppConnect already use. The `node` struct (types.go) decodes Render and " +
		"FPPConnect but has no Audio field at all: GET /nodes, GET /nodes/{nodeId}, and GET /snapshot all " +
		"silently drop it. Known existing gap, found by this test; not fixed here.",
	"snapshot.macroRuns": "Snapshot.macroRuns is required (api/openapi.yaml's Snapshot schema), and its own " +
		"doc comment there calls it fatal to omit per ADR-020 decision 3: the change stream emits no id, so " +
		"a client connecting for the first time during an in-flight macro run has no other way to learn the " +
		"run exists. The `snapshot` struct (types.go) has no MacroRuns field. Known existing gap, found by " +
		"this test; not fixed here.",
	"snapshot.auditStore": "Snapshot.auditStore is required (api/openapi.yaml's Snapshot schema): whether " +
		"this coordinator can currently write to its audit store, probed fresh on every request. The " +
		"`snapshot` struct (types.go) has no AuditStore field. Known existing gap, found by this test; not " +
		"fixed here.",
	"showAction.idempotent": "ConfigShowAction.idempotent is required (api/openapi.yaml, added by commit " +
		"9238f04, 'Let show.action declare whether it is idempotent (#247)'). That commit shipped the API " +
		"surface and the Operator UI control but missed showmeshctl: the `showAction` struct " +
		"(types_macro.go) still has no Idempotent field, so GET /config/show.action/{id} silently drops it. " +
		"Known existing gap, found by this test; not fixed here -- being raised and fixed separately.",
	"nightSessionStateWire.authorization": "NightSessionState.authorization is required (api/openapi.yaml's " +
		"NightSessionState schema): who authorized this night session, for provenance across a coordinator " +
		"restart. The `nightSessionStateWire` struct (types_night_lifecycle.go) has no Authorization field, " +
		"so GET /night/session silently drops it. Known existing gap, found by this test; not fixed here.",
	"resolumeRecoveryResponse.resolumeConfigured": "ResolumeRecoveryResponse.resolumeConfigured is required " +
		"(api/openapi.yaml): whether this coordinator has any Resolume instance configured at all, distinct " +
		"from autoRestoreConfigured -- the schema's own doc comment says a client should render 'not " +
		"configured' rather than the toggle's default-ON value when this is false, since an operator who " +
		"believes recovery is armed and is wrong is worse off than one who knows it is unavailable. The " +
		"`resolumeRecoveryResponse` struct (cmd_resolume_recovery.go) has no ResolumeConfigured field, so " +
		"GET /resolume/recovery silently drops a safety-relevant signal. Known existing gap, found by this " +
		"test; not fixed here.",
	"evidence.resource": "ObservationEntry.resource is required (api/openapi.yaml's ObservationEntry schema, " +
		"the item type of GET /observations): which resource (kind + id) an entry concerns, since a flat " +
		"list spanning every resource can't rely on an enclosing object to imply the subject the way " +
		"Node.evidence.* can. observationsResponse.Observations (cmd_render_transport.go) decodes each item " +
		"as a bare `evidence`, which has no Resource field at all -- every observation GET /observations " +
		"returns loses which resource it's about. Known existing gap, found by this test; not fixed here.",
}

// isExemptResponseField is getResponseFieldExemptions' only reader: a
// required property missing from typeName's decoded fields is exempt only
// when THIS EXACT "type.property" pair has its own entry. Deliberately a
// small pure function (no *testing.T, no schema, no struct table) so the
// scoping property it enforces -- one exemption never covers more than the
// one property it names -- has a standing unit test
// (TestFieldExemptionsAreScopedToOneProperty) independent of whatever
// api/openapi.yaml and cmd/showmeshctl's structs currently look like.
func isExemptResponseField(typeName, propName string) (exempted bool, reason string) {
	reason, exempted = getResponseFieldExemptions[typeName+"."+propName]
	return exempted, reason
}

// TestFieldExemptionsAreScopedToOneProperty is the standing regression
// check for getResponseFieldExemptions' own critical property (see its doc
// comment above): an exemption must name a specific type AND a specific
// property, never just a type. It asserts that against the real table
// (snapshot.macroRuns and snapshot.auditStore are both real entries today)
// rather than a fixture, so the moment either real entry is removed (its
// gap fixed) this test is updated along with it -- it cannot go stale
// pointing at an exemption nobody maintains any more. The critical
// assertion is the third one: a made-up property on the SAME already-
// exempted "snapshot" type must NOT be swept in by the type name alone. If
// this test ever fails there, the exemption mechanism has regressed from
// per-property to per-type and every required field snapshot grows from
// here on is silently unchecked.
func TestFieldExemptionsAreScopedToOneProperty(t *testing.T) {
	for _, known := range []string{"macroRuns", "auditStore"} {
		exempted, reason := isExemptResponseField("snapshot", known)
		if !exempted || reason == "" {
			t.Fatalf("expected snapshot.%s to be a known, reasoned entry in getResponseFieldExemptions; "+
				"got exempted=%v reason=%q -- update this test if that gap has been fixed and its entry removed",
				known, exempted, reason)
		}
	}

	const probeProp = "exemptionScopeProbeProperty"
	if exempted, reason := isExemptResponseField("snapshot", probeProp); exempted {
		t.Fatalf("a required property (%q) with no entry of its own must not be exempted just because "+
			"\"snapshot\" has OTHER exemptions -- got exempted=true reason=%q; exemption granularity has "+
			"regressed from per-property to per-type", probeProp, reason)
	}
}

// goTypeIdentName returns the base type name a Go type expression names --
// unwrapping at most one leading pointer -- or "" for anything else (a
// slice, map, interface, or a name qualified by another package, none of
// which this test can resolve against a local struct declaration).
func goTypeIdentName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return goTypeIdentName(e.X)
	default:
		return ""
	}
}

// goElemTypeName reports the base named type of a Go field's type
// expression, and whether that expression is a slice of it. It looks
// through at most one pointer and at most one slice ([]T or *[]T are both
// "T", the second additionally reported as a slice) -- the two shapes this
// package's own response structs actually use for a nested object
// (types.go's own doc comment: "optional/absent fields are pointers").
// Anything else -- a map, a bare interface (any/interface{}), a
// package-qualified name like time.Time -- returns "", meaning this test
// cannot check further and stops rather than guessing.
func goElemTypeName(expr ast.Expr) (name string, isSlice bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name, false
	case *ast.StarExpr:
		return goElemTypeName(e.X)
	case *ast.ArrayType:
		n, _ := goElemTypeName(e.Elt)
		return n, true
	default:
		return "", false
	}
}

// fieldJSONTag reads a struct field's `json:"..."` tag. ok is false when
// the field carries no json tag at all (encoding/json then uses the Go
// field name verbatim, the same default this function's caller applies).
// excluded is true for the `json:"-"` convention (field never serialized).
func fieldJSONTag(f *ast.Field) (name string, ok bool, excluded bool) {
	if f.Tag == nil {
		return "", false, false
	}
	raw, err := strconv.Unquote(f.Tag.Value)
	if err != nil {
		return "", false, false
	}
	jsonTag, has := reflect.StructTag(raw).Lookup("json")
	if !has {
		return "", false, false
	}
	name, _, _ = strings.Cut(jsonTag, ",")
	if name == "-" {
		return "", true, true
	}
	return name, true, false
}

// jsonFieldsFor returns every JSON property name typeName's struct
// decodes, mapped to the ast.Expr of the Go field that holds it. An
// anonymous (embedded) field with no json tag of its own -- this package's
// own observationEntry embedding evidence being the load-bearing case,
// since ObservationEntry's openapi schema requires evidence's properties
// flattened directly onto it (contract section 6.3) -- has ITS fields
// promoted into the result exactly as encoding/json promotes them onto the
// wire, recursively. visiting guards against a self-referential type
// looping forever; none of this package's response types are
// self-referential today, but a future one being accidentally so should
// not hang this test. ok is false only when typeName names no struct this
// package declares.
func jsonFieldsFor(typeName string, structs map[string]*ast.StructType, visiting map[string]bool) (map[string]ast.Expr, bool) {
	st, declared := structs[typeName]
	if !declared {
		return nil, false
	}
	if visiting[typeName] {
		return map[string]ast.Expr{}, true
	}
	visiting[typeName] = true
	defer delete(visiting, typeName)

	out := map[string]ast.Expr{}
	for _, f := range st.Fields.List {
		jsonName, hasTag, excluded := fieldJSONTag(f)
		if excluded {
			continue
		}
		if len(f.Names) == 0 {
			// Anonymous field: an explicit named json tag makes it an
			// ordinary named property (encoding/json's own rule); no tag
			// (or a tag with no name half, jsonName=="") promotes its
			// fields onto this type instead.
			if hasTag && jsonName != "" {
				out[jsonName] = f.Type
				continue
			}
			embedded := goTypeIdentName(f.Type)
			if embedded == "" {
				continue
			}
			nested, _ := jsonFieldsFor(embedded, structs, visiting)
			for k, v := range nested {
				out[k] = v
			}
			continue
		}
		for _, n := range f.Names {
			name := jsonName
			if !hasTag || jsonName == "" {
				name = n.Name
			}
			out[name] = f.Type
		}
	}
	return out, true
}

// collectPackageStructs maps every `type NAME struct{...}` this package
// declares (any non-test .go file, any nesting depth -- mirrors
// collectFileLocalAssignments' own best-effort, whole-file ast.Inspect
// rather than top-level-only Decls, so a struct type declared inside a
// function is not silently invisible to this test) to its *ast.StructType.
func collectPackageStructs(t *testing.T) map[string]*ast.StructType {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}

	out := map[string]*ast.StructType{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if st, ok := ts.Type.(*ast.StructType); ok {
				out[ts.Name.Name] = st
			}
			return true
		})
	}
	return out
}

// cliGetCall is one getJSON/getRaw call site this package's own source
// makes, reduced to the path shape it addresses (see pathShapeFromFlat)
// and, when this scan could determine it, the Go type decoding the
// response.
type cliGetCall struct {
	segs     []pathSegment
	typeName string
}

var getCallFuncNames = map[string]bool{"getJSON": true, "getRaw": true}

// collectCLIGetPathShapes parses every non-test .go file in this package's
// directory (the same universe collectCLIWritePathShapes scans) and
// returns one cliGetCall per getJSON/getRaw call site. typeName is filled
// in when the call's own function body declares `var IDENT SomeType`
// somewhere earlier and the call passes &IDENT as getJSON's out argument --
// the shape the large majority of this package's GET call sites actually
// use. This tracking is function-scoped (reset per *ast.FuncDecl, covering
// a nested func literal's body too, since ast.Inspect descends into it) and
// deliberately best-effort/not block-scoped beyond that, the same posture
// collectFileLocalAssignments documents for path-value resolution: real
// call sites in this package never shadow their own response var within a
// nested block in a way that would produce a wrong answer, only ever an
// if/else declaring it independently in each branch (cmd_assets.go's asset
// manifest command being the case in point), which this still resolves
// correctly because ast.Inspect visits each branch's declaration before
// that branch's own call site.
func collectCLIGetPathShapes(t *testing.T) []cliGetCall {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}

	var calls []cliGetCall
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		resolve := collectFileLocalAssignments(f)

		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}

			localVarTypes := map[string]string{}
			ast.Inspect(fn.Body, func(n2 ast.Node) bool {
				switch x := n2.(type) {
				case *ast.DeclStmt:
					gd, ok := x.Decl.(*ast.GenDecl)
					if !ok || gd.Tok != token.VAR {
						return true
					}
					for _, spec := range gd.Specs {
						vs, ok := spec.(*ast.ValueSpec)
						if !ok || vs.Type == nil {
							continue
						}
						typeName := goTypeIdentName(vs.Type)
						if typeName == "" {
							continue
						}
						for _, nm := range vs.Names {
							localVarTypes[nm.Name] = typeName
						}
					}
				case *ast.CallExpr:
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if !ok || !getCallFuncNames[sel.Sel.Name] {
						return true
					}
					if len(x.Args) < 2 {
						return true
					}
					flat := flattenPathExpr(x.Args[1], resolve, 0)

					var typeName string
					if sel.Sel.Name == "getJSON" && len(x.Args) >= 4 {
						if unary, ok := x.Args[3].(*ast.UnaryExpr); ok && unary.Op == token.AND {
							if id, ok := unary.X.(*ast.Ident); ok {
								typeName = localVarTypes[id.Name]
							}
						}
					}
					calls = append(calls, cliGetCall{segs: pathShapeFromFlat(flat), typeName: typeName})
				}
				return true
			})
			return false
		})
	}
	return calls
}

// asMap type-asserts a YAML-decoded node (gopkg.in/yaml.v3 decodes a
// mapping into map[string]any when the target is `any`) to map[string]any,
// returning nil for anything else (a scalar, a sequence, an absent key).
func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// resolvePointer walks an internal "#/a/b/c" JSON-pointer-style openapi
// $ref against the root document. Only internal (same-document) refs
// appear anywhere in api/openapi.yaml's schemas.
func resolvePointer(root any, ref string) any {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	cur := root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
		m := asMap(cur)
		if m == nil {
			return nil
		}
		cur = m[part]
	}
	return cur
}

// resolveMapNode resolves node -- which may itself be a bare object, or an
// object whose only meaningful content is a "$ref" -- down to the concrete
// map it names, following at most 20 $ref hops (a safety bound against a
// cyclic document; every real chain in this file resolves in one or two).
func resolveMapNode(root any, node any, depth int) map[string]any {
	if depth > 20 {
		return nil
	}
	m := asMap(node)
	if m == nil {
		return nil
	}
	if ref, ok := m["$ref"].(string); ok {
		return resolveMapNode(root, resolvePointer(root, ref), depth+1)
	}
	return m
}

// schemaHasType reports whether an openapi schema's "type" is exactly want
// or a union (JSON Schema's `type: [string, "null"]` form, used throughout
// this file for nullable scalars) containing it.
func schemaHasType(schema map[string]any, want string) bool {
	switch t := schema["type"].(type) {
	case string:
		return t == want
	case []any:
		for _, v := range t {
			if s, _ := v.(string); s == want {
				return true
			}
		}
	}
	return false
}

func isObjectSchemaNode(schema map[string]any) bool {
	return schemaHasType(schema, "object") || schema["properties"] != nil
}

func isArraySchemaNode(schema map[string]any) bool {
	return schemaHasType(schema, "array") || schema["items"] != nil
}

// responseSchemaNodeForGET returns the (unresolved -- may still be a $ref)
// application/json schema node for apiPath's 200 response, and whether one
// exists at all (a 204 or a path/method this document does not declare has
// none).
func responseSchemaNodeForGET(root any, apiPath string) (any, bool) {
	paths := asMap(asMap(root)["paths"])
	pathItem := asMap(paths[apiPath])
	getOp := asMap(pathItem["get"])
	if getOp == nil {
		return nil, false
	}
	responses := asMap(getOp["responses"])
	resp200 := resolveMapNode(root, responses["200"], 0)
	if resp200 == nil {
		return nil, false
	}
	content := asMap(resp200["content"])
	appJSON := asMap(content["application/json"])
	if appJSON == nil {
		return nil, false
	}
	schemaNode, ok := appJSON["schema"]
	if !ok {
		return nil, false
	}
	return schemaNode, true
}

func requiredPropertyNames(schema map[string]any) []string {
	raw, _ := schema["required"].([]any)
	names := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok {
			names = append(names, s)
		}
	}
	return names
}

// checkResponseSchema is TestEveryGETResponseRequiredField's own recursive
// core: schemaNode is an openapi schema (resolved here, possibly still
// behind a $ref); typeName is the Go type this test asserts decodes it;
// fieldPath is schemaNode's dotted position from the response root, for
// error messages only. Every property schemaNode's own "required" list
// names must correspond to a json-tagged field on typeName's Go struct
// (jsonFieldsFor, which already flattens promoted embedded fields); when
// such a property's own schema is itself a required object (directly, or
// as an array's items), and the matching Go field resolves to a named
// struct (goElemTypeName), the same check recurses into it. A property
// whose Go field is a slice/map/interface/pointer this scan cannot reduce
// to a named local struct (any, map[string]any, time.Time, ...) stops
// there without either checking or failing further -- see
// goElemTypeName's own doc comment for exactly which shapes those are;
// this only weakens the check for a property that is deliberately loosely
// typed on the Go side, never for one this package encodes as a concrete
// named struct.
func checkResponseSchema(root any, schemaNode any, typeName string, fieldPath string, structs map[string]*ast.StructType, stack map[string]bool, t *testing.T, opKey string) {
	schema := resolveMapNode(root, schemaNode, 0)
	if schema == nil {
		return
	}
	required := requiredPropertyNames(schema)
	if len(required) == 0 {
		return
	}
	if stack[typeName] {
		return
	}

	fields, declared := jsonFieldsFor(typeName, structs, map[string]bool{})
	if !declared {
		t.Errorf("%s: response schema at %s has required propert%s %v but the Go type %q this test expected to "+
			"decode it does not exist in cmd/showmeshctl", opKey, fieldPath, plural(len(required)), required, typeName)
		return
	}

	stack[typeName] = true
	defer delete(stack, typeName)

	properties := asMap(schema["properties"])
	for _, propName := range required {
		fieldExpr, has := fields[propName]
		if !has {
			exempted, reason := isExemptResponseField(typeName, propName)
			if exempted {
				if reason == "" {
					t.Errorf("getResponseFieldExemptions[%q] has no stated reason", typeName+"."+propName)
				}
				continue
			}
			t.Errorf("%s: required response property %q at %s has no matching json-tagged field on Go type %q -- "+
				"showmeshctl silently drops this field today; add it to the struct (ADR-039 decision 9, "+
				"CLAUDE.md's CLI-parity constraint) or add a reasoned entry to getResponseFieldExemptions",
				opKey, propName, fieldPath, typeName)
			continue
		}

		propSchemaNode, ok := properties[propName]
		if !ok {
			continue
		}
		resolvedProp := resolveMapNode(root, propSchemaNode, 0)
		if resolvedProp == nil {
			continue
		}

		elemType, isSlice := goElemTypeName(fieldExpr)
		if elemType == "" {
			continue
		}

		switch {
		case isArraySchemaNode(resolvedProp):
			if !isSlice {
				continue
			}
			items, ok := resolvedProp["items"]
			if !ok {
				continue
			}
			checkResponseSchema(root, items, elemType, fieldPath+"."+propName+"[]", structs, stack, t, opKey)
		case isObjectSchemaNode(resolvedProp):
			if isSlice {
				continue
			}
			checkResponseSchema(root, propSchemaNode, elemType, fieldPath+"."+propName, structs, stack, t, opKey)
		}
	}
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// loadOpenAPIDocGeneric parses api/openapi.yaml as a bare `any`, giving
// this test generic $ref-following access to the whole document
// (resolveMapNode/resolvePointer) rather than the narrow typed shape
// nonGETOpenAPIOperations uses for the write-parity test, which only ever
// needs path/method presence and never a response schema's own content.
func loadOpenAPIDocGeneric(t *testing.T) any {
	t.Helper()
	path := "../../api/openapi.yaml"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s as YAML: %v", path, err)
	}
	return doc
}

func getOpenAPIPaths(t *testing.T, root any) map[string]bool {
	t.Helper()
	paths := asMap(asMap(root)["paths"])
	out := map[string]bool{}
	for p, item := range paths {
		if asMap(item)["get"] != nil {
			out[p] = true
		}
	}
	return out
}

// TestEveryGETResponseRequiredField is this test's acceptance criterion: for
// every GET path showmeshctl actually calls (collectCLIGetPathShapes,
// matched against api/openapi.yaml the identical structural way
// TestEveryWritePathHasACLIVerb matches write paths -- see
// openAPISegments/segmentsCompatible), every property api/openapi.yaml's
// response schema marks required, recursively through every $ref'd nested
// object and array-of-object, must land on a json-tagged Go struct field
// this package actually decodes (checkResponseSchema). Fails closed on two
// independent axes: a GET path this program calls but whose decoding Go
// type this scan cannot determine (no getResponseTypeOverrides entry) FAILS
// naming the path, rather than being silently skipped; and a matched type
// missing a required field FAILS naming the exact dotted path and type.
func TestEveryGETResponseRequiredField(t *testing.T) {
	root := loadOpenAPIDocGeneric(t)

	getPaths := getOpenAPIPaths(t, root)
	if len(getPaths) < 20 {
		t.Fatalf("found only %d GET paths in api/openapi.yaml (expected at least 20) -- "+
			"the YAML scan is almost certainly broken", len(getPaths))
	}

	calls := collectCLIGetPathShapes(t)
	if len(calls) == 0 {
		t.Fatal("found zero GET call sites in cmd/showmeshctl -- the AST scan is almost certainly broken, " +
			"not that this program issues no reads")
	}

	structs := collectPackageStructs(t)

	// Attribute every call to its MOST SPECIFIC compatible openapi GET
	// path(s) only. segmentsCompatible alone is a boolean "could this be
	// it", which is all the write-parity test above needs (it only asks
	// whether SOME CLI call covers a path); this test needs to know WHICH
	// ONE decodes a given path's response, and a wildcard segment (a path
	// parameter) always structurally overlaps a same-depth literal
	// sibling -- e.g. /assets/{id} accepts the literal text "manifest" as
	// readily as any other id, even though /assets/manifest is a
	// distinct, more specific, separately-registered route. Standard
	// router-style resolution -- prefer the candidate with fewer
	// wildcard segments -- breaks that tie the same way a real HTTP
	// router would.
	bestPathsForCall := make([]map[string]bool, len(calls))
	for ci, call := range calls {
		minWildcards := -1
		best := map[string]bool{}
		for p := range getPaths {
			apiSegs := openAPISegments(p)
			if !segmentsCompatible(call.segs, apiSegs) {
				continue
			}
			wildcards := 0
			for _, s := range apiSegs {
				if s.variable {
					wildcards++
				}
			}
			switch {
			case minWildcards == -1 || wildcards < minWildcards:
				minWildcards = wildcards
				best = map[string]bool{p: true}
			case wildcards == minWildcards:
				best[p] = true
			}
		}
		bestPathsForCall[ci] = best
	}

	for p := range getPaths {
		matched := false
		var matchedTypes []string
		for ci, call := range calls {
			if !bestPathsForCall[ci][p] {
				continue
			}
			matched = true
			if call.typeName == "" {
				continue
			}
			found := false
			for _, existing := range matchedTypes {
				if existing == call.typeName {
					found = true
					break
				}
			}
			if !found {
				matchedTypes = append(matchedTypes, call.typeName)
			}
		}
		if !matched {
			continue
		}

		opKey := "GET " + p

		typeName := ""
		switch {
		case getResponseTypeOverrides[opKey] != "":
			typeName = getResponseTypeOverrides[opKey]
		case len(matchedTypes) == 1:
			typeName = matchedTypes[0]
		case len(matchedTypes) == 0:
			t.Errorf("%s: showmeshctl calls this GET path but this test could not determine which Go type "+
				"decodes its response (no getJSON call site with an inferable var type, and no entry in "+
				"getResponseTypeOverrides) -- add an override naming the decoding type and its call site", opKey)
			continue
		default:
			t.Errorf("%s: showmeshctl call sites decode this response into more than one different Go type %v "+
				"by this scan's own reckoning -- add an entry to getResponseTypeOverrides naming the correct one",
				opKey, matchedTypes)
			continue
		}

		schemaNode, hasContent := responseSchemaNodeForGET(root, p)
		if !hasContent {
			continue
		}
		checkResponseSchema(root, schemaNode, typeName, p, structs, map[string]bool{}, t, opKey)
	}

	for opKey, typeName := range getResponseTypeOverrides {
		method, p, ok := strings.Cut(opKey, " ")
		if !ok || method != "GET" {
			t.Errorf("getResponseTypeOverrides key %q is not \"GET /path\"", opKey)
			continue
		}
		if !getPaths[p] {
			t.Errorf("getResponseTypeOverrides names %q but api/openapi.yaml declares no GET operation at that "+
				"path -- remove the stale entry", opKey)
		}
		if typeName == "" {
			t.Errorf("getResponseTypeOverrides[%q] has no stated Go type", opKey)
			continue
		}
		if _, declared := structs[typeName]; !declared {
			t.Errorf("getResponseTypeOverrides[%q] names Go type %q which cmd/showmeshctl does not declare",
				opKey, typeName)
		}
	}
}
