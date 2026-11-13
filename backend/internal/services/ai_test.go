package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBuildOpenAIChatRequest(t *testing.T) {
	t.Run("plain text omits response_format", func(t *testing.T) {
		req := buildOpenAIChatRequest("gpt-4o-mini", "Retorne apenas o assunto em texto simples.", false)
		if req.ResponseFormat != nil {
			t.Fatalf("expected nil ResponseFormat for plain text, got %+v", req.ResponseFormat)
		}
		if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
			t.Fatalf("unexpected messages: %+v", req.Messages)
		}
	})

	t.Run("json mode injects json instruction when missing from prompt", func(t *testing.T) {
		req := buildOpenAIChatRequest("gpt-4o-mini", "List 3 items", true)
		if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" {
			t.Fatalf("expected json_object ResponseFormat, got %+v", req.ResponseFormat)
		}
		if len(req.Messages) != 2 || !strings.Contains(strings.ToLower(req.Messages[0].Content), "json") {
			t.Fatalf("expected system message mentioning json, got %+v", req.Messages)
		}
	})

	t.Run("json mode does not duplicate when prompt already mentions json", func(t *testing.T) {
		req := buildOpenAIChatRequest("gpt-4o-mini", "Retorne em formato JSON.", true)
		if len(req.Messages) != 1 {
			t.Fatalf("expected 1 message, got %d", len(req.Messages))
		}
	})
}

func TestCallOpenAICompatible(t *testing.T) {
	var seen OpenAIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer srv.Close()

	var pt, ct int
	out, err := callOpenAICompatible("openai", srv.URL, "gpt-4o-mini", "test-key", "Apenas texto simples", false, time.Second, func(p, c int) {
		pt, ct = p, c
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "ok" || pt != 3 || ct != 2 {
		t.Fatalf("unexpected output/usage: out=%q pt=%d ct=%d", out, pt, ct)
	}
	if seen.ResponseFormat != nil {
		t.Fatalf("expected nil response_format in HTTP payload, got %+v", seen.ResponseFormat)
	}
	if llmHTTPTimeout < 5*time.Minute {
		t.Fatalf("expected llmHTTPTimeout >= 5m for long script generation, got %v", llmHTTPTimeout)
	}
}

func TestBuildVideoScriptPromptOutroHandling(t *testing.T) {
	withOutro := buildVideoScriptPrompt("Canal Teste", "Mistérios", "Caso X", true)
	if !strings.Contains(withOutro, "SEM CTA") || strings.Contains(withOutro, "CALL TO ACTION NATURAL") {
		t.Fatalf("expected prompt with outro to forbid CTA and avoid natural CTA rule")
	}
	if !strings.Contains(scriptSupervisorRubric(true), "last_scene") {
		t.Fatalf("expected supervisor rubric with outro to check last_scene")
	}

	withoutOutro := buildVideoScriptPrompt("Canal Teste", "Mistérios", "Caso X", false)
	if !strings.Contains(withoutOutro, "CALL TO ACTION NATURAL") || strings.Contains(withoutOutro, "SEM CTA") {
		t.Fatalf("expected prompt without outro to allow natural CTA")
	}
	if strings.Contains(scriptSupervisorRubric(false), "last_scene") {
		t.Fatalf("did not expect supervisor rubric without outro to forbid CTA on last_scene")
	}
}

