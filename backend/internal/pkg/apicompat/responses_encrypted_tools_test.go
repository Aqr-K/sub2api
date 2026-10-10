package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoverResponsesEncryptedTools(t *testing.T) {
	var req map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{
	  "tools":[
	    {"type":"namespace","name":"future_mcp","tools":[{"type":"function","name":"custom_secret","parameters":{"type":"object","properties":{"a/b~c":{"type":"array","items":{"type":"string","encrypted":true}}},"examples":[{"encrypted":true}]}}]},
	    {"type":"function","name":"external_function","parameters":{"type":"object","properties":{"text":{"anyOf":[{"type":"string","encrypted":true},{"type":"null"}]}}}},
	    {"type":"function","name":"not_encrypted","parameters":{"type":"object","properties":{"encrypted":{"type":"boolean"}},"default":{"encrypted":true}}}
	  ],
	  "input":[
	    {"type":"additional_tools","tools":[{"type":"namespace","name":"notes","tools":[{"type":"function","name":"write_file","parameters":{"properties":{"text":{"type":"string","encrypted":true}}}}]}]},
	    {"type":"tool_search_output","tools":[{"type":"function","name":"loaded_later","parameters":{"$defs":{"secret":{"type":"string","encrypted":true}}}}]},
	    {"type":"message","content":[{"type":"input_text","text":"encrypted:true"}],"tools":[{"type":"function","name":"user_data","parameters":{"encrypted":true}}]}
	  ]
	}`), &req))
	before, err := json.Marshal(req)
	require.NoError(t, err)
	inventory := DiscoverResponsesEncryptedTools(req)
	require.Equal(t, []ResponsesEncryptedTool{
		{Namespace: "future_mcp", Name: "custom_secret", Parameters: []string{"/properties/a~1b~0c/items"}},
		{Name: "external_function", Parameters: []string{"/properties/text/anyOf/0"}},
		{Namespace: "notes", Name: "write_file", Parameters: []string{"/properties/text"}},
		{Name: "loaded_later", Parameters: []string{"/$defs/secret"}},
	}, inventory)
	mapping, changed, err := AdaptResponsesCollaborationPlaintext(req)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, inventory, mapping.Unadapted)
	after, err := json.Marshal(req)
	require.NoError(t, err)
	require.Equal(t, before, after, "unhandled tools must retain their complete protocol")
}

func TestPlaintextCollaborationRejectsAdditionalEncryptedParametersAtomically(t *testing.T) {
	for _, extra := range []string{
		`"secret":{"type":"string","encrypted":true}`,
		`"nested":{"type":"array","items":{"type":"object","properties":{"secret":{"type":"string","encrypted":true}}}}`,
	} {
		var req map[string]any
		require.NoError(t, json.Unmarshal([]byte(`{"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true},`+extra+`}}}]}]}`), &req))
		before, err := json.Marshal(req)
		require.NoError(t, err)
		mapping, changed, err := AdaptResponsesCollaborationPlaintext(req)
		require.ErrorContains(t, err, "outside its supported message parameter")
		require.False(t, changed)
		require.Empty(t, mapping.Calls)
		after, marshalErr := json.Marshal(req)
		require.NoError(t, marshalErr)
		require.Equal(t, before, after)
	}
}

func TestPlaintextCollaborationDeferredDiscoveryAndHistory(t *testing.T) {
	var req map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{
	 "input":[
	  {"type":"tool_search_output","tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}}]}]},
	  {"type":"function_call","namespace":"collaboration","name":"send_message","call_id":"old","arguments":"opaque","encrypted_function_args":["message"]},
	  {"type":"function_call","namespace":"collaboration","name":"send_message","call_id":"new","arguments":"{}","encrypted_function_args":[]},
	  {"type":"function_call_output","namespace":"collaboration","name":"send_message","call_id":"new","output":[{"type":"encrypted_content","encrypted_content":"opaque-result"}]}
	 ]
	}`), &req))
	items, ok := req["input"].([]any)
	require.True(t, ok)
	require.Len(t, items, 4)
	old := items[1]
	before, err := json.Marshal(old)
	require.NoError(t, err)
	mapping, changed, err := AdaptResponsesCollaborationPlaintext(req)
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, mapping.Calls, 1)
	require.Empty(t, mapping.Unadapted)
	after, err := json.Marshal(old)
	require.NoError(t, err)
	require.Equal(t, before, after, "old ciphertext is not a plaintext call")
	for _, index := range []int{2, 3} {
		item, ok := items[index].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "collaboration__send_message", item["name"])
		require.Equal(t, "new", item["call_id"])
	}
	outputItem, ok := items[3].(map[string]any)
	require.True(t, ok)
	output, ok := outputItem["output"].([]any)
	require.True(t, ok)
	require.Len(t, output, 1)
	content, ok := output[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "opaque-result", content["encrypted_content"])
}

func TestPlaintextCollaborationDoesNotReinterpretOtherNamespaceCalls(t *testing.T) {
	mapping := ResponsesCollaborationPlaintextMapping{Calls: map[string]ResponsesNamespaceName{
		"collaboration__send_message": {Namespace: "collaboration", Name: "send_message"},
	}}
	body := []byte(`{"output":[{"type":"function_call","namespace":"external","name":"collaboration__send_message","arguments":"opaque","encrypted_function_args":["text"]}]}`)
	result, changed, err := RestoreResponsesCollaborationPlaintextPayload(body, mapping)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, result)
}

func TestPlaintextCollaborationPreservesExternalAliasReferences(t *testing.T) {
	mapping := ResponsesCollaborationPlaintextMapping{Calls: map[string]ResponsesNamespaceName{
		"collaboration__send_message": {Namespace: "collaboration", Name: "send_message"},
	}}
	for _, body := range []string{
		`{"type":"response.function_call_arguments.done","namespace":"external","name":"collaboration__send_message","arguments":"{}"}`,
		`{"tools":[{"type":"function","namespace":"external","name":"collaboration__send_message"}],"tool_choice":{"type":"function","namespace":"external","name":"collaboration__send_message"}}`,
	} {
		out, changed, err := RestoreResponsesCollaborationPlaintextPayload([]byte(body), mapping)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, body, string(out))
	}
}
