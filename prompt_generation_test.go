package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		apiKey:          "test-key",
		baseURL:         server.URL + "/v1",
		model:           "test-model",
		reasoningEffort: "medium",
		client:          server.Client(),
	}

	imageData := []byte("test-image")
	got, err := generator.Generate(context.Background(), PromptGenerationInput{
		SystemPrompt: "system instructions",
		SourcePrompt: "source prompt",
		Image: &PromptImage{
			MediaType: "image/jpeg",
			Data:      imageData,
			Detail:    "auto",
		},
	})
	if err != nil {
		t.Fatalf("Generate returned an error: %v", err)
	}
	if got != "remixed prompt" {
		t.Fatalf("unexpected generated prompt: %q", got)
	}
	if received.Model != "test-model" {
		t.Errorf("unexpected model: %q", received.Model)
	}
	if received.ReasoningEffort != "medium" {
		t.Errorf("unexpected reasoning effort: %q", received.ReasoningEffort)
	}
	if len(received.Messages) != 2 || received.Messages[0].Role != "system" || received.Messages[0].Content != "system instructions" {
		t.Errorf("unexpected messages: %#v", received.Messages)
	}
	parts, ok := received.Messages[1].Content.([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("unexpected multimodal content: %#v", received.Messages[1].Content)
	}
	imagePart, ok := parts[0].(map[string]any)
	if !ok || imagePart["type"] != "image_url" {
		t.Fatalf("unexpected image part: %#v", parts[0])
	}
	imageURL, ok := imagePart["image_url"].(map[string]any)
	if !ok || imageURL["detail"] != "auto" {
		t.Fatalf("unexpected image URL: %#v", imagePart["image_url"])
	}
	encodedURL, _ := imageURL["url"].(string)
	encodedData := strings.TrimPrefix(encodedURL, "data:image/jpeg;base64,")
	decodedData, err := base64.StdEncoding.DecodeString(encodedData)
	if err != nil || !bytes.Equal(decodedData, imageData) {
		t.Errorf("unexpected encoded image: %q (%v)", encodedURL, err)
	}
	textPart, ok := parts[1].(map[string]any)
	if !ok || textPart["type"] != "text" || !strings.Contains(textPart["text"].(string), "source prompt") {
		t.Errorf("unexpected text part: %#v", parts[1])
	}
}

func TestNewPromptGeneratorFromEnvUsesGrok45MediumDefaults(t *testing.T) {
	t.Setenv("PROMPT_LLM_API_KEY", "")
	t.Setenv("PROMPT_LLM_BASE_URL", "")
	t.Setenv("PROMPT_LLM_MODEL", "")
	t.Setenv("PROMPT_LLM_REASONING_EFFORT", "")
	t.Setenv("XAI_API_KEY", "test-key")
	t.Setenv("XAI_BASE_URL", "")
	t.Setenv("XAI_MODEL", "")
	t.Setenv("XAI_REASONING_EFFORT", "")

	generator, description := newPromptGeneratorFromEnv()
	configured, ok := generator.(*OpenAICompatiblePromptGenerator)
	if !ok {
		t.Fatalf("unexpected generator type: %T", generator)
	}
	if configured.model != "grok-4.5" {
		t.Errorf("unexpected default model: %q", configured.model)
	}
	if configured.reasoningEffort != "medium" {
		t.Errorf("unexpected default reasoning effort: %q", configured.reasoningEffort)
	}
	if !strings.Contains(description, `model "grok-4.5" (reasoning effort: medium)`) {
		t.Errorf("unexpected description: %q", description)
	}
}

func TestPromptChatMessagesWithoutImageUsesPlainText(t *testing.T) {
	messages := promptChatMessages(PromptGenerationInput{
		SystemPrompt: "system",
		SourcePrompt: "source",
	})
	if len(messages) != 2 || messages[1].Content != "source" {
		t.Fatalf("unexpected text-only messages: %#v", messages)
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

			_, err := generator.Generate(context.Background(), PromptGenerationInput{
				SystemPrompt: "system",
				SourcePrompt: "source",
			})
			if err == nil || !strings.Contains(err.Error(), tt.expected) {
				t.Fatalf("expected provider error, got %v", err)
			}
		})
	}
}

type recordingPromptGenerator struct {
	input PromptGenerationInput
	calls int
}

func (g *recordingPromptGenerator) Generate(_ context.Context, input PromptGenerationInput) (string, error) {
	g.input = input
	g.calls++
	return "generated result", nil
}

func TestHandleGeneratePrompt(t *testing.T) {
	db := openPromptTestDB(t)
	if _, err := db.Exec(`INSERT INTO images (id, prompt, filename, is_nsfw) VALUES (42, 'original prompt', 'source.png', 0)`); err != nil {
		t.Fatalf("insert image: %v", err)
	}
	imageBaseDir := t.TempDir()
	imagePath := filepath.Join(imageBaseDir, "images", "source.png")
	writePromptTestImage(t, imagePath, 2048, 1024)

	generator := &recordingPromptGenerator{}
	app := &App{db: db, promptGenerator: generator, promptImageBaseDir: imageBaseDir}
	req := httptest.NewRequest(http.MethodPost, "/api/generate-prompt", strings.NewReader(`{"image_id":42,"target_model":"anima"}`))
	recorder := httptest.NewRecorder()

	app.handleGeneratePrompt(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if generator.input.SourcePrompt != "original prompt" {
		t.Errorf("unexpected source prompt: %q", generator.input.SourcePrompt)
	}
	if !strings.Contains(generator.input.SystemPrompt, "Anima") {
		t.Errorf("Anima profile was not selected: %q", generator.input.SystemPrompt)
	}
	if generator.input.Image == nil || generator.input.Image.MediaType != "image/jpeg" || generator.input.Image.Detail != "auto" {
		t.Fatalf("unexpected prompt image: %#v", generator.input.Image)
	}
	prepared, _, err := image.Decode(bytes.NewReader(generator.input.Image.Data))
	if err != nil {
		t.Fatalf("decode prepared image: %v", err)
	}
	if got := prepared.Bounds().Size(); got.X != 1536 || got.Y != 768 {
		t.Errorf("unexpected prepared image size: %v", got)
	}

	var response generatePromptResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Prompt != "generated result" {
		t.Errorf("unexpected response prompt: %q", response.Prompt)
	}
}

func TestPreparePromptImageKeepsSmallImagesWithinBounds(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "source.png")
	writePromptTestImage(t, imagePath, 640, 480)

	prepared, err := preparePromptImage(imagePath)
	if err != nil {
		t.Fatalf("preparePromptImage returned an error: %v", err)
	}
	if prepared.MediaType != "image/jpeg" || prepared.Detail != "auto" {
		t.Fatalf("unexpected prompt image metadata: %#v", prepared)
	}
	decoded, format, err := image.Decode(bytes.NewReader(prepared.Data))
	if err != nil {
		t.Fatalf("decode prepared image: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("unexpected prepared format: %q", format)
	}
	if got := decoded.Bounds().Size(); got.X != 640 || got.Y != 480 {
		t.Errorf("small image was unexpectedly resized: %v", got)
	}
}

func TestHandleGeneratePromptReturnsErrorWhenSourceImageIsMissing(t *testing.T) {
	db := openPromptTestDB(t)
	if _, err := db.Exec(`INSERT INTO images (id, prompt, filename, is_nsfw) VALUES (42, 'original prompt', 'missing.png', 0)`); err != nil {
		t.Fatalf("insert image: %v", err)
	}
	generator := &recordingPromptGenerator{}
	app := &App{db: db, promptGenerator: generator, promptImageBaseDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/api/generate-prompt", strings.NewReader(`{"image_id":42,"target_model":"anima"}`))
	recorder := httptest.NewRecorder()

	app.handleGeneratePrompt(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if generator.calls != 0 {
		t.Fatalf("generator was called %d times without a source image", generator.calls)
	}
}

func TestPromptImagePathRejectsNestedFilename(t *testing.T) {
	app := &App{promptImageBaseDir: t.TempDir()}
	if _, err := app.promptImagePath("../source.png", false); err == nil {
		t.Fatal("expected nested filename to be rejected")
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
	if _, err := db.Exec(`CREATE TABLE images (
		id INTEGER PRIMARY KEY,
		prompt TEXT NOT NULL,
		filename TEXT NOT NULL,
		is_nsfw BOOLEAN NOT NULL DEFAULT FALSE
	)`); err != nil {
		t.Fatalf("create images table: %v", err)
	}
	return db
}

func writePromptTestImage(t *testing.T, path string, width, height int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create image directory: %v", err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}
	defer file.Close()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	if err := png.Encode(file, img); err != nil {
		t.Fatalf("encode image: %v", err)
	}
}
