package handler

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Use the real Responses handler, Forward and HTTP upstream. A request-history
// rejection must end the account loop and submit the known usage exactly once.
func TestResponsesEncryptedHistoryErrorRecordsUsageOnce(t *testing.T) {
	testResponsesCompatibilityErrorRecordsUsageOnce(t, false)
}

func TestResponsesPlaintextCollaborationErrorRecordsUsageOnce(t *testing.T) {
	testResponsesCompatibilityErrorRecordsUsageOnce(t, true)
}

func testResponsesCompatibilityErrorRecordsUsageOnce(t *testing.T, plaintext bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough=%t/stream=%t", passthrough, stream), func(t *testing.T) {
				var upstreamCalls atomic.Int32
				requests := make(chan []byte, 10)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamCalls.Add(1)
					body, _ := io.ReadAll(r.Body)
					requests <- body
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("X-Request-Id", "req_7708_handler")
					if plaintext {
						_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"resp_bad","output":[{"type":"function_call","name":"collaboration__send_message","call_id":"bad","arguments":"opaque-test-argument","encrypted_function_args":["message"]}],"usage":{"input_tokens":19,"output_tokens":3}}}`+"\n\n")
						return
					}
					_, _ = io.WriteString(w, "event: response.failed\ndata: "+`{"type":"response.failed","response":{"id":"resp_7708","status":"failed","error":{"code":"invalid_encrypted_content","message":"Could not decrypt, please try again","param":"input[0]"},"usage":{"input_tokens":19,"output_tokens":3}}}`+"\n\n")
				}))
				defer upstream.Close()
				groupID := int64(7708)
				accounts := make([]service.Account, 2)
				for i := range accounts {
					accounts[i] = service.Account{
						ID: int64(77080 + i), Name: "encrypted-history", Platform: service.PlatformOpenAI,
						Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: i,
						Credentials: map[string]any{"api_key": "sk-test", "base_url": upstream.URL, "pool_mode": true},
						Extra:       map[string]any{"openai_passthrough": passthrough, "use_responses_api": true, "openai_responses_plaintext_collaboration": plaintext},
					}
				}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				cfg.Default.RateMultiplier = 1
				cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				cfg.Gateway.MaxAccountSwitches = 1
				accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}
				usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 10)}
				billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				defer billingCache.Stop()
				gateway := service.NewOpenAIGatewayService(
					accountRepo, usageRepo, nil, nil, nil, nil, nil, cfg, nil, nil,
					service.NewBillingService(cfg, nil), nil, billingCache, &compositeWSHTTPUpstream{},
					&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
				)
				h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billingCache,
					service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				body := fmt.Sprintf(`{"model":"gpt-5.2","stream":%t,"input":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"tool-cipher"}]}`, stream)
				if plaintext {
					body = fmt.Sprintf(`{"model":"gpt-5.2","stream":%t,"input":"go","tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true}}}}]}]}`, stream)
				}
				c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(body))
				if plaintext {
					c.Request.Header.Set("User-Agent", "codex_cli_rs/0.162.0")
				}
				c.Request.Header.Set("Content-Type", "application/json")
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
					ID: 1803, GroupID: &groupID, User: &service.User{ID: 1703, Status: service.StatusActive},
					Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
				})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1703, Concurrency: 0})
				h.Responses(c)
				require.Equal(t, int32(1), upstreamCalls.Load(), "response: %s", rec.Body.String())
				require.Len(t, requests, 1)
				request := <-requests
				if plaintext {
					require.True(t, gjson.GetBytes(request, `tools.#(name=="collaboration__send_message")`).Exists())
					require.NotContains(t, rec.Body.String(), "opaque-test-argument")
					require.NotContains(t, rec.Body.String(), "collaboration__send_message")
				} else {
					require.JSONEq(t, gjson.Get(body, "input").Raw, gjson.GetBytes(request, "input").Raw)
					require.Contains(t, rec.Body.String(), "invalid_encrypted_content")
				}
				if !plaintext {
					require.NotContains(t, rec.Body.String(), "Upstream service temporarily unavailable")
				}
				if plaintext {
					require.Contains(t, []int{http.StatusOK, http.StatusBadGateway}, rec.Code)
				} else if stream {
					require.Equal(t, http.StatusOK, rec.Code)
				} else {
					require.Equal(t, http.StatusBadRequest, rec.Code)
				}
				require.Len(t, usageRepo.created, 1, "the handler's synchronous usage fallback must submit once")
				usage := <-usageRepo.created
				require.Equal(t, 19, usage.InputTokens)
				require.Equal(t, 3, usage.OutputTokens)
				require.Equal(t, "req_7708_handler", usage.RequestID)
				require.Empty(t, accountRepo.rateLimitedIDs)
			})
		}
	}
}
