package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestOpenAICompatiblePromptGenerator(t *testing.T) {
	var received chatCompletionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected authorization header: %s", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"  remixed prompt  "}}]}`))
	}))
	defer server.Close()

	generator := &OpenAICompatiblePromptGenerator{
		apiKey:  "test-key",
		baseURL: server.URL + "/v1",
		model:   "test-model",
		client:  server.Client(),
	}

	got, err := generator.Generate(context.Background(), "system instructions", "source prompt")
	if err != nil {
		t.Fatalf("Generate returned an error: %v", err)
	}
	if got != "remixed prompt" {
		t.Fatalf("unexpected generated prompt: %q", got)
	}
	if received.Model != "test-model" {
		t.Errorf("unexpected model: %q", received.Model)
	}
	if len(received.Messages) != 2 || received.Messages[0].Role != "system" || received.Messages[1].Content != "source prompt" {
		t.Errorf("unexpected messages: %#v", received.Messages)
	}
}

func TestOpenAICompatiblePromptGeneratorReturnsProviderError(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{name: "structured error", body: `{"error":{"message":"credits exhausted"}}`, expected: "credits exhausted"},
		{name: "string error", body: `{"error":"invalid API key"}`, expected: "invalid API key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			generator := &OpenAICompatiblePromptGenerator{
				apiKey:  "test-key",
				baseURL: server.URL,
				model:   "test-model",
				client:  server.Client(),
			}

			_, err := generator.Generate(context.Background(), "system", "source")
			if err == nil || !strings.Contains(err.Error(), tt.expected) {
				t.Fatalf("expected provider error, got %v", err)
			}
		})
	}
}

type recordingPromptGenerator struct {
	systemPrompt string
	sourcePrompt string
}

func (g *recordingPromptGenerator) Generate(_ context.Context, systemPrompt, sourcePrompt string) (string, error) {
	g.systemPrompt = systemPrompt
	g.sourcePrompt = sourcePrompt
	return "generated result", nil
}

func TestHandleGeneratePrompt(t *testing.T) {
	db := openPromptTestDB(t)
	if _, err := db.Exec(`INSERT INTO images (id, prompt) VALUES (42, 'original prompt')`); err != nil {
		t.Fatalf("insert image: %v", err)
	}

	generator := &recordingPromptGenerator{}
	app := &App{db: db, promptGenerator: generator}
	req := httptest.NewRequest(http.MethodPost, "/api/generate-prompt", strings.NewReader(`{"image_id":42,"target_model":"anima"}`))
	recorder := httptest.NewRecorder()

	app.handleGeneratePrompt(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if generator.sourcePrompt != "original prompt" {
		t.Errorf("unexpected source prompt: %q", generator.sourcePrompt)
	}
	if !strings.Contains(generator.systemPrompt, "Anima") {
		t.Errorf("Anima profile was not selected: %q", generator.systemPrompt)
	}

	var response generatePromptResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Prompt != "generated result" {
		t.Errorf("unexpected response prompt: %q", response.Prompt)
	}
}

func TestHandleGeneratePromptRejectsUnknownProfile(t *testing.T) {
	app := &App{promptGenerator: &recordingPromptGenerator{}}
	req := httptest.NewRequest(http.MethodPost, "/api/generate-prompt", strings.NewReader(`{"image_id":42,"target_model":"other"}`))
	recorder := httptest.NewRecorder()

	app.handleGeneratePrompt(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
}

func openPromptTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE images (id INTEGER PRIMARY KEY, prompt TEXT NOT NULL)`); err != nil {
		t.Fatalf("create images table: %v", err)
	}
	return db
}
