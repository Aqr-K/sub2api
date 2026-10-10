package apicompat

import (
	"sort"
	"strconv"
	"strings"
)

// ResponsesEncryptedTool describes encryption declarations, not the contents
// of tool arguments. Discovery is independent of the plaintext adapters: new
// namespaces and tools are visible here without adding them to a name list.
type ResponsesEncryptedTool struct {
	Namespace string
	Name      string
	// Parameters contains JSON pointers relative to the parameters schema.
	Parameters []string
}

// DiscoverResponsesEncryptedTools inspects both ordinary and deferred/Lite
// declarations. It never descends into user messages, arguments, examples or
// tool results, where an "encrypted" key may be ordinary application data.
func DiscoverResponsesEncryptedTools(req map[string]any) []ResponsesEncryptedTool {
	var found []ResponsesEncryptedTool
	var visit func([]any, string)
	visit = func(tools []any, namespace string) {
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name := stringValue(tool["name"])
			switch stringValue(tool["type"]) {
			case "namespace":
				// Namespace nesting is not part of the Responses contract.
				if namespace == "" {
					visit(namespaceChildren(tool), name)
				}
			case "function":
				params, err := collaborationSchemaField(tool, "parameters")
				if err != nil || params == nil {
					continue
				}
				paths := encryptedResponseSchemaPaths(params)
				if len(paths) == 0 {
					continue
				}
				ns := namespace
				if ns == "" {
					ns = stringValue(tool["namespace"])
				}
				found = append(found, ResponsesEncryptedTool{Namespace: ns, Name: name, Parameters: paths})
			}
		}
	}
	for _, carrier := range collaborationPlaintextCarriers(req) {
		visit(carrier.tools, "")
	}
	return found
}

func encryptedResponseSchemaPaths(schema map[string]any) []string {
	var paths []string
	var visit func(any, string)
	visit = func(value any, path string) {
		obj, err := collaborationSchemaField(map[string]any{"schema": value}, "schema")
		if err != nil || obj == nil {
			return
		}
		if encrypted, _ := obj["encrypted"].(bool); encrypted {
			paths = append(paths, path)
		}
		for _, key := range []string{"properties", "$defs", "definitions", "patternProperties", "dependentSchemas", "dependencies"} {
			children, _ := collaborationSchemaField(obj, key)
			for name, child := range children {
				escaped := strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
				visit(child, path+"/"+key+"/"+escaped)
			}
		}
		for _, key := range []string{"items", "additionalItems", "additionalProperties", "unevaluatedItems", "unevaluatedProperties", "propertyNames", "contentSchema", "contains", "not", "if", "then", "else"} {
			visit(obj[key], path+"/"+key)
		}
		for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems", "items"} {
			children, _ := obj[key].([]any)
			for i, child := range children {
				visit(child, path+"/"+key+"/"+strconv.Itoa(i))
			}
		}
	}
	visit(schema, "")
	sort.Strings(paths)
	return paths
}

// UnadaptedResponsesEncryptedTools reports declarations not covered by this
// request's adapter. They retain their original protocol; an empty result
// still says nothing about future encrypted outputs from MCP/backend tools.
func UnadaptedResponsesEncryptedTools(tools []ResponsesEncryptedTool, mapping ResponsesCollaborationPlaintextMapping) []ResponsesEncryptedTool {
	var unadapted []ResponsesEncryptedTool
	for _, tool := range tools {
		entry, ok := mapping.Calls[flattenNamespaceToolName(tool.Namespace, tool.Name)]
		if ok && entry.Namespace == tool.Namespace && entry.Name == tool.Name &&
			len(tool.Parameters) == 1 && tool.Parameters[0] == "/properties/message" {
			continue
		}
		unadapted = append(unadapted, tool)
	}
	return unadapted
}
