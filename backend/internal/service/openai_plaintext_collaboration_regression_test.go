package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestPlaintextCollaborationClientVersionGate(t *testing.T) {
	for _, ua := range []string{"codex_cli_rs/0.144.6", "codex_cli_rs/0.153.0", "codex_cli_rs/0.154.0-alpha.1", "", "curl/8.0", "codex_cli_rs/invalid"} {
		t.Run(ua, func(t *testing.T) {
			body := collabPlaintextRequestBody(false)
			c, recorder := collabPlaintextTestContext(body)
			c.Request.Header.Set("User-Agent", ua)
			// An outbound identity override must not masquerade as client support.
			c.Request.Header.Set("version", "0.162.0")
			upstream := collabPlaintextNonstreamUpstream("collaboration__send_message")
			svc := newOpenAIRejectedFieldTestService(upstream)
			account := collabPlaintextEnabledAccount()
			result, err := svc.Forward(context.Background(), c, account, body)
			require.Error(t, err)
			require.Nil(t, result)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Empty(t, upstream.bodies, "unsupported clients must be rejected before generation")
			require.True(t, IsOpenAIResponseCompatibilityError(err))
			require.False(t, svc.ReportOpenAIAccountScheduleResult(account, "gpt-5.5", false, nil, err))
		})
	}
	for _, ua := range []string{"codex_cli_rs/0.154.0", "codex_cli_rs/0.162.0", "codex_vscode/0.162.0 (linux)"} {
		t.Run(ua, func(t *testing.T) {
			c, _ := collabPlaintextTestContext(nil)
			c.Request.Header.Set("User-Agent", ua)
			require.NoError(t, validateOpenAIPlaintextCollaborationClient(c))
		})
	}
}

func TestPlaintextCollaborationUnknownEncryptedToolsRemainVisible(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","tools":[{"type":"function","name":"future_external_tool","parameters":{"type":"object","properties":{"text":{"type":"string","encrypted":true}}}}],"input":"go"}`)
	c, _ := collabPlaintextTestContext(body)
	c.Request.Header.Set("User-Agent", "another-client/1.0")
	out, err := adaptOpenAIResponsesCollabPlaintextBody(c, collabPlaintextEnabledAccount(), body)
	require.NoError(t, err)
	require.Equal(t, body, out)
	inventory, ok := c.Get(openAIResponsesUnadaptedEncryptedToolsContextKey)
	require.True(t, ok)
	require.Equal(t, []apicompat.ResponsesEncryptedTool{{Name: "future_external_tool", Parameters: []string{"/properties/text"}}}, inventory)
}

func TestPlaintextCollaborationUnexpectedCiphertextPreservesUsage(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, shape := range []string{"json", "sse_to_json", "sse"} {
			t.Run(fmt.Sprintf("passthrough=%v/%s", passthrough, shape), func(t *testing.T) {
				body := collabPlaintextRequestBody(shape == "sse")
				c, recorder := collabPlaintextTestContext(body)
				account := collabPlaintextEnabledAccount()
				account.Extra["openai_passthrough"] = passthrough
				account.Extra["openai_oauth_passthrough"] = passthrough
				// A server violating the ordinary function contract must not
				// trick the adapter into relabeling ciphertext as plaintext.
				response := `{"id":"resp_bad","status":"completed","output":[{"type":"function_call","name":"collaboration__send_message","call_id":"c1","arguments":"opaque-test-argument","encrypted_function_args":["message"]}],"usage":{"input_tokens":19,"output_tokens":3}}`
				contentType := "application/json"
				if shape != "json" {
					response = "data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
					contentType = "text/event-stream"
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(response))}}
				result, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), c, account, body)
				require.Error(t, err)
				require.True(t, IsOpenAIResponseCompatibilityError(err))
				require.NotNil(t, result)
				require.EqualValues(t, 19, result.Usage.InputTokens)
				require.EqualValues(t, 3, result.Usage.OutputTokens)
				require.Len(t, upstream.bodies, 1, "an incompatible response must not be retried")
				require.NotContains(t, recorder.Body.String(), "opaque-test-argument")
				require.NotContains(t, recorder.Body.String(), "collaboration__send_message")
			})
		}
	}
}

func TestPlaintextCollaborationWSRejectedClientDoesNotCommitTools(t *testing.T) {
	c, _ := collabPlaintextTestContext(nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.6")
	session, err := openAIWSCollabPlaintextSessionForAccount(c, collabPlaintextEnabledAccount())
	require.NoError(t, err)
	_, err = session.adaptPayload([]byte(`{"type":"response.create","tools":` + collabPlaintextToolsJSON + `}`))
	require.Error(t, err)
	require.Empty(t, session.declaredTools())
	require.Empty(t, session.mapping.Calls)
}

func TestPlaintextCollaborationIncompleteSSEToJSON(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			body := collabPlaintextRequestBody(false)
			c, recorder := collabPlaintextTestContext(body)
			account := collabPlaintextEnabledAccount()
			account.Extra["openai_passthrough"] = passthrough
			account.Extra["openai_oauth_passthrough"] = passthrough
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"name\":\"collaboration__send_message\",\"arguments\":\"{}\"},\"usage\":{\"input_tokens\":19,\"output_tokens\":3}}\n\n"))}}
			result, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), c, account, body)
			require.Error(t, err)
			require.True(t, IsOpenAIResponseCompatibilityError(err))
			require.NotNil(t, result)
			require.EqualValues(t, 19, result.Usage.InputTokens)
			require.EqualValues(t, 3, result.Usage.OutputTokens)
			require.NotContains(t, recorder.Body.String(), "collaboration__send_message")
			require.Len(t, upstream.bodies, 1)
		})
	}
}
