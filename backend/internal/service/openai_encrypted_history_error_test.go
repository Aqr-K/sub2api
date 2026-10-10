package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIEncryptedHistoryErrorClassification(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI}
	for _, tc := range []struct {
		name, payload string
		want          bool
	}{
		{"nested", `{"type":"error","error":{"code":"invalid_encrypted_content"}}`, true},
		{"native", `{"type":"error","code":"invalid_encrypted_content","param":null}`, true},
		{"failed", `{"type":"response.failed","response":{"error":{"code":"invalid_encrypted_content"}}}`, true},
		{"known signature", `{"error":{"code":"thinking_signature_invalid","message":"The encrypted content could not be verified. Reason: Encrypted content could not be decrypted or parsed."}}`, true},
		{"unrelated signature", `{"error":{"code":"thinking_signature_invalid","message":"Invalid signature"}}`, false},
		{"message only", `{"error":{"message":"invalid_encrypted_content"}}`, false},
		{"case sensitive", `{"error":{"code":"INVALID_ENCRYPTED_CONTENT"}}`, false},
		{"invalid JSON", `{"error":{"code":"invalid_encrypted_content"}`, false},
		{"server error", `{"error":{"code":"server_error","message":"try again"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := openAIEncryptedHistoryError(account, []byte(tc.payload))
			require.Equal(t, tc.want, err != nil)
			if err != nil {
				require.True(t, IsOpenAIEncryptedHistoryError(fmt.Errorf("read stream: %w", err)))
			}
			require.Nil(t, openAIEncryptedHistoryError(nil, []byte(tc.payload)))
			require.Nil(t, openAIEncryptedHistoryError(&Account{Platform: PlatformAnthropic}, []byte(tc.payload)))
		})
	}
}

func TestEncryptedHistoryErrorKeepsAccountHealthNeutral(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	svc := &OpenAIGatewayService{rateLimitService: newOpenAIAdvancedSchedulerRateLimitService("true")}
	account := openAIHealthPoolAccount()
	ttft := 120
	svc.ReportOpenAIAccountScheduleResult(account, "gpt-5", false, &ttft, fmt.Errorf("ordinary failure"))
	beforeRate, beforeTTFT, beforeHasTTFT := svc.openaiAccountStats.snapshot(account.ID)
	require.Greater(t, beforeRate, 0.0, "the scheduler must be active for this test")
	cache := &openAIAPIKeyHealthCacheStub{tripped: true}
	svc.rateLimitService.SetOpenAIAPIKeyHealthCache(cache)
	err := fmt.Errorf("stream: %w", &OpenAIEncryptedHistoryError{Code: "invalid_encrypted_content"})
	for i := 0; i < 5; i++ {
		require.False(t, svc.ReportOpenAIAccountScheduleResult(account, "gpt-5", false, nil, err))
		require.False(t, svc.ObserveOpenAIAccountHealthFailure(context.Background(), account, err))
	}
	afterRate, afterTTFT, afterHasTTFT := svc.openaiAccountStats.snapshot(account.ID)
	require.Equal(t, beforeRate, afterRate)
	require.Equal(t, beforeTTFT, afterTTFT)
	require.Equal(t, beforeHasTTFT, afterHasTTFT)
	require.Zero(t, cache.recordCalls)
	require.Zero(t, cache.setCalls)
}

func TestEncryptedHistoryErrorJSONAfterCompactKeepalive(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			c, rec := newCompactBridgeTestContext(t, true)
			stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
			defer stop()
			value, ok := c.Get(openAICompactSSEKeepaliveKey)
			require.True(t, ok)
			keepalive, ok := value.(*openAICompactSSEKeepalive)
			require.True(t, ok)
			require.True(t, keepalive.beat())
			svc := newNonStreamingFailoverService()
			payload := `{"type":"error","code":"invalid_encrypted_content","message":"cannot decrypt","param":null,"sequence_number":1}`
			body := sseTerminalBody("error", payload)
			var err error
			if passthrough {
				_, err = svc.handlePassthroughSSEToJSON(newNonStreamingSSEResponse(), c, newNonStreamingFailoverAccount(), body, "gpt-5", "gpt-5")
			} else {
				_, err = svc.handleSSEToJSON(newNonStreamingSSEResponse(), c, newNonStreamingFailoverAccount(), body, "gpt-5", "gpt-5")
			}
			require.True(t, IsOpenAIEncryptedHistoryError(err))
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
			require.Contains(t, rec.Body.String(), ": keepalive\n\n")
			event, data, ok := extractOpenAISSETerminalEvent(rec.Body.String())
			require.True(t, ok)
			require.Equal(t, "response.failed", event)
			require.Equal(t, "invalid_encrypted_content", gjson.GetBytes(data, "response.error.code").String())
			require.Equal(t, "null", gjson.GetBytes(data, "response.error.param").Raw)
			require.NotContains(t, rec.Body.String(), "\n{\"error\":")
		})
	}
}

// Existing Codex readers hold bare errors until EOF or a final response. A
// successful final response must clear the provisional history rejection.
func TestEncryptedHistoryErrorSuccessfulTerminalOverridesPendingError(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			c, rec := newNonStreamingFailoverContext(t)
			svc := newNonStreamingFailoverService()
			account := newNonStreamingFailoverAccount()
			account.Type = AccountTypeOAuth
			body := "event: error\ndata: " + `{"type":"error","code":"invalid_encrypted_content","message":"cannot decrypt","param":null}` + "\n\n" +
				"event: response.completed\ndata: " + `{"type":"response.completed","response":{"id":"resp_ok","status":"completed","output":[],"usage":{"input_tokens":19,"output_tokens":3}}}` + "\n\n"
			resp := newNonStreamingSSEResponse()
			resp.Body = io.NopCloser(strings.NewReader(body))
			var usage *OpenAIUsage
			if passthrough {
				result, err := svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "gpt-5", "gpt-5")
				require.NoError(t, err)
				usage = result.usage
			} else {
				result, err := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "gpt-5", "gpt-5")
				require.NoError(t, err)
				usage = result.usage
			}
			require.Equal(t, 19, usage.InputTokens)
			require.Equal(t, 3, usage.OutputTokens)
			require.NotContains(t, rec.Body.String(), "invalid_encrypted_content")
			require.Contains(t, rec.Body.String(), "response.completed")
			require.NotContains(t, rec.Body.String(), "response.failed")
		})
	}
}

func TestEncryptedHistoryErrorAfterOutputPreservesProgressiveUsage(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, message := range []string{"rate limit exceeded", "input exceeds the context window"} {
			t.Run(fmt.Sprintf("passthrough=%t/%s", passthrough, message), func(t *testing.T) {
				c, rec := newNonStreamingFailoverContext(t)
				svc := newNonStreamingFailoverService()
				// Any account mutation would reach an unimplemented repository method.
				svc.rateLimitService = NewRateLimitService(&openAIAPIKeyHealthAccountRepo{}, nil, &config.Config{}, nil, nil)
				account := newNonStreamingFailoverAccount()
				body := "event: response.output_text.delta\ndata: " + `{"type":"response.output_text.delta","delta":"partial","usage":{"input_tokens":11,"output_tokens":2}}` + "\n\n" +
					"event: response.failed\ndata: " + `{"type":"response.failed","response":{"id":"resp_failed","status":"failed","error":{"code":"invalid_encrypted_content","message":"` + message + `","param":null},"usage":{}}}` + "\n\n"
				resp := newNonStreamingSSEResponse()
				resp.Body = io.NopCloser(strings.NewReader(body))
				var usage *OpenAIUsage
				if passthrough {
					result, err := svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "gpt-5", "gpt-5")
					require.True(t, IsOpenAIEncryptedHistoryError(err), "err=%v", err)
					usage = result.usage
				} else {
					result, err := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "gpt-5", "gpt-5")
					require.True(t, IsOpenAIEncryptedHistoryError(err), "err=%v", err)
					usage = result.usage
				}
				require.Equal(t, 11, usage.InputTokens)
				require.Equal(t, 2, usage.OutputTokens)
				require.Contains(t, rec.Body.String(), "partial")
				_, payload, ok := extractOpenAISSETerminalEvent(rec.Body.String())
				require.True(t, ok)
				require.Equal(t, "invalid_encrypted_content", gjson.GetBytes(payload, "response.error.code").String())
				require.Equal(t, "null", gjson.GetBytes(payload, "response.error.param").Raw)
			})
		}
	}
}

// Exercise Forward, not just the parsers: a typed error must retain partial
// usage all the way to the handler and must never replay encrypted tool history.
func TestForwardEncryptedHistoryError(t *testing.T) {
	const errorObject = `{"code":"invalid_encrypted_content","message":"Encrypted function output content could not be decrypted or decoded.","param":"input[1]","type":"invalid_request_error"}`
	for _, passthrough := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name, event, payload string
				input, output        int
			}{
				{"issue7708", "error", `{"type":"error","error":` + errorObject + `,"sequence_number":1}`, 0, 0},
				{"native", "error", `{"type":"error","code":"invalid_encrypted_content","message":"Encrypted function output content could not be decrypted or decoded.","param":"input[1]","sequence_number":1}`, 0, 0},
				{"failed with usage", "response.failed", `{"type":"response.failed","response":{"id":"resp_failed","status":"failed","error":` + errorObject + `,"usage":{"input_tokens":19,"output_tokens":3}}}`, 19, 3},
				{"missing category transient wording", "response.failed", `{"type":"response.failed","response":{"status":"failed","error":{"code":"invalid_encrypted_content","message":"Could not decrypt, please try again","param":"input[1]"},"usage":{"input_tokens":19,"output_tokens":3}}}`, 19, 3},
			} {
				t.Run(fmt.Sprintf("passthrough=%t/stream=%t/%s", passthrough, stream, tc.name), func(t *testing.T) {
					body := "event: " + tc.event + "\ndata: " + tc.payload + "\n\n"
					upstream := &httpUpstreamRecorder{resp: &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"req_7708"}},
						Body:       io.NopCloser(strings.NewReader(body)),
					}}
					svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
					account := &Account{ID: 10, Name: "history-test", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
						Concurrency: 1, Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://example.com"},
						Extra: map[string]any{"use_responses_api": true, "openai_passthrough": passthrough}}
					c, rec := newNonStreamingFailoverContext(t)
					SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
					request := fmt.Sprintf(`{"model":"gpt-5","stream":%t,"input":[{"type":"reasoning","encrypted_content":"reasoning-cipher"},{"type":"function_call_output","call_id":"call_1","output":"tool-cipher"}]}`, stream)
					result, err := svc.Forward(context.Background(), c, account, []byte(request))
					require.True(t, IsOpenAIEncryptedHistoryError(err), "err=%v", err)
					require.NotNil(t, result)
					require.Equal(t, tc.input, result.Usage.InputTokens)
					require.Equal(t, tc.output, result.Usage.OutputTokens)
					require.Equal(t, "req_7708", result.RequestID)
					require.Len(t, upstream.bodies, 1)
					require.JSONEq(t, gjson.Get(request, "input").Raw, gjson.GetBytes(upstream.bodies[0], "input").Raw)
					require.Contains(t, rec.Body.String(), "invalid_encrypted_content")
					require.Contains(t, rec.Body.String(), "input[1]")
					if stream {
						require.Equal(t, http.StatusOK, rec.Code)
						require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
					} else {
						require.Equal(t, http.StatusBadRequest, rec.Code)
						require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
						require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
					}
				})
			}
		}
	}
}
