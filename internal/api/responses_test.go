package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/provider"
)

func newResponsesTestRouter(
	responses func(c *gin.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error),
	stream func(c *gin.Context, req provider.ResponsesRequest) error,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerResponses(r.Group("/v1"), responses, stream)
	return r
}

func TestRegisterResponsesForwardsNonStream(t *testing.T) {
	var got provider.ResponsesRequest
	r := newResponsesTestRouter(func(c *gin.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error) {
		got = req
		return provider.ResponsesResult{Model: req.Model, Raw: []byte(`{"id":"resp_1"}`)}, nil
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"m1","input":"hi","custom":true}`))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != `{"id":"resp_1"}` || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected response: %q %q", w.Body.String(), w.Header().Get("Content-Type"))
	}
	if got.Model != "m1" || got.Stream || !strings.Contains(string(got.Raw), `"custom":true`) {
		t.Fatalf("unexpected request: %+v", got)
	}
}

func TestRegisterResponsesStreamsFramesVerbatim(t *testing.T) {
	r := newResponsesTestRouter(nil, func(c *gin.Context, req provider.ResponsesRequest) error {
		ch := make(chan provider.ResponsesChunk, 2)
		ch <- provider.ResponsesChunk{Raw: "id:1\nevent:response.created\ndata:{\"type\":\"response.created\"}\n\n"}
		ch <- provider.ResponsesChunk{Raw: "id:2\nevent:response.completed\ndata:{\"type\":\"response.completed\"}\n\n", Done: true}
		close(ch)
		_, _, err := writeResponsesSSE(c, ch)
		return err
	})

	w := newCloseNotifierRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"m1","input":"hi","stream":true}`))
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "event:response.created") || !strings.Contains(body, "event:response.completed") {
		t.Fatalf("frames not forwarded: %q", body)
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("responses stream must not append [DONE]: %q", body)
	}
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q", w.Header().Get("Content-Type"))
	}
	if w.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("x-accel-buffering = %q", w.Header().Get("X-Accel-Buffering"))
	}
}

func TestRegisterResponsesPassesUpstream4xxBody(t *testing.T) {
	r := newResponsesTestRouter(func(c *gin.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error) {
		return provider.ResponsesResult{}, provider.NewStatusError("upstream", http.StatusBadRequest, `{"error":{"message":"bad"}}`)
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"m1","input":"hi"}`))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest || w.Body.String() != `{"error":{"message":"bad"}}` {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestRegisterResponsesRejectsBadJSONAndMissingModel(t *testing.T) {
	r := newResponsesTestRouter(func(c *gin.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error) {
		t.Fatal("handler must not be called")
		return provider.ResponsesResult{}, nil
	}, nil)

	for _, body := range []string{`{`, `{"input":"hi"}`} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(body))
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d", body, w.Code)
		}
	}
}

func TestRegisterResponsesPassesThroughEmptyUpstream4xx(t *testing.T) {
	r := newResponsesTestRouter(func(c *gin.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error) {
		return provider.ResponsesResult{}, provider.NewStatusError("upstream", http.StatusTooManyRequests, "")
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"m1","input":"hi"}`))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body=%q", w.Code, w.Body.String())
	}
	if w.Body.String() != "" {
		t.Fatalf("body = %q, want empty", w.Body.String())
	}
}

func TestRegisterResponsesStreamErrorWritesSinglePayload(t *testing.T) {
	const errMsg = "stream broke"
	r := newResponsesTestRouter(nil, func(c *gin.Context, req provider.ResponsesRequest) error {
		ch := make(chan provider.ResponsesChunk, 1)
		ch <- provider.ResponsesChunk{Err: errors.New(errMsg)}
		close(ch)
		_, _, err := writeResponsesSSE(c, ch)
		return err
	})

	w := newCloseNotifierRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"m1","input":"hi","stream":true}`))
	r.ServeHTTP(w, req)

	body := w.Body.String()
	const payload = `data: {"type":"error","message":"` + errMsg + `"}`
	if got := strings.Count(body, payload); got != 1 {
		t.Fatalf("error payload count = %d, want 1, body=%q", got, body)
	}
	if strings.Contains(body, `"error":`) {
		t.Fatalf("error payload must use type/message shape, body=%q", body)
	}
	if got := strings.Count(body, "event: error"); got != 1 {
		t.Fatalf("error frame count = %d, body=%q", got, body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("JSON appended after SSE frame: %q", body)
	}
}
