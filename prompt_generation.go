package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultPromptLLMBaseURL = "https://api.x.ai/v1"
	defaultPromptLLMModel   = "grok-4-1-fast-reasoning"
	maxPromptRequestBytes   = 4 << 10
	maxPromptResponseBytes  = 2 << 20
)

// PromptGenerator is the provider-neutral boundary used by the application.
// Providers only need to turn a system instruction and a source prompt into text.
type PromptGenerator interface {
	Generate(ctx context.Context, systemPrompt, sourcePrompt string) (string, error)
}

type PromptProfile struct {
	ID           string
	Name         string
	SystemPrompt string
}

type promptProfileDefinition struct {
	Name             string
	SystemPromptPath string
}

var promptProfileDefinitions = map[string]promptProfileDefinition{
	"anima": {
		Name:             "Anima",
		SystemPromptPath: "prompt_systems/anima.md",
	},
	"krea-2": {
		Name:             "Krea 2",
		SystemPromptPath: "prompt_systems/krea-2.md",
	},
}

func getPromptProfile(id string) (PromptProfile, bool, error) {
	definition, ok := promptProfileDefinitions[id]
	if !ok {
		return PromptProfile{}, false, nil
	}

	content, err := os.ReadFile(definition.SystemPromptPath)
	if err != nil {
		return PromptProfile{}, true, fmt.Errorf("read system prompt %s: %w", definition.SystemPromptPath, err)
	}
	systemPrompt := strings.TrimSpace(string(content))
	if systemPrompt == "" {
		return PromptProfile{}, true, fmt.Errorf("system prompt %s is empty", definition.SystemPromptPath)
	}

	return PromptProfile{
		ID:           id,
		Name:         definition.Name,
		SystemPrompt: systemPrompt,
	}, true, nil
}

// OpenAICompatiblePromptGenerator works with xAI and other providers exposing
// the OpenAI chat-completions contract.
type OpenAICompatiblePromptGenerator struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

type chatCompletionRequest struct {
	Model               string                  `json:"model"`
	Messages            []chatCompletionMessage `json:"messages"`
	MaxCompletionTokens int                     `json:"max_completion_tokens,omitempty"`
}

type chatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatCompletionMessage `json:"message"`
	} `json:"choices"`
	Error json.RawMessage `json:"error,omitempty"`
}

func newPromptGeneratorFromEnv() (PromptGenerator, string) {
	apiKey := firstNonEmptyEnv("PROMPT_LLM_API_KEY", "XAI_API_KEY")
	if apiKey == "" {
		return nil, ""
	}

	baseURL := firstNonEmptyEnv("PROMPT_LLM_BASE_URL", "XAI_BASE_URL")
	if baseURL == "" {
		baseURL = defaultPromptLLMBaseURL
	}

	model := firstNonEmptyEnv("PROMPT_LLM_MODEL", "XAI_MODEL")
	if model == "" {
		model = defaultPromptLLMModel
	}

	return &OpenAICompatiblePromptGenerator{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: 90 * time.Second},
	}, fmt.Sprintf("model %q at %s", model, strings.TrimRight(baseURL, "/"))
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func (g *OpenAICompatiblePromptGenerator) Generate(ctx context.Context, systemPrompt, sourcePrompt string) (string, error) {
	requestBody := chatCompletionRequest{
		Model: g.model,
		Messages: []chatCompletionMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: sourcePrompt},
		},
		MaxCompletionTokens: 1200,
	}

	payload, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("encode chat completion request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create chat completion request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+g.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("send chat completion request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPromptResponseBytes))
	if err != nil {
		return "", fmt.Errorf("read chat completion response: %w", err)
	}

	var completion chatCompletionResponse
	if err := json.Unmarshal(body, &completion); err != nil {
		return "", fmt.Errorf("decode chat completion response (status %d): %w", resp.StatusCode, err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := http.StatusText(resp.StatusCode)
		if providerMessage := chatCompletionErrorMessage(completion.Error); providerMessage != "" {
			message = providerMessage
		}
		return "", fmt.Errorf("chat completion failed with status %d: %s", resp.StatusCode, message)
	}

	if len(completion.Choices) == 0 {
		return "", errors.New("chat completion response contains no choices")
	}

	generatedPrompt := strings.TrimSpace(completion.Choices[0].Message.Content)
	if generatedPrompt == "" {
		return "", errors.New("chat completion response contains an empty prompt")
	}

	return generatedPrompt, nil
}

func chatCompletionErrorMessage(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var message string
	if err := json.Unmarshal(raw, &message); err == nil {
		return strings.TrimSpace(message)
	}

	var structured struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &structured); err == nil {
		return strings.TrimSpace(structured.Message)
	}

	return ""
}

type generatePromptRequest struct {
	ImageID     int    `json:"image_id"`
	TargetModel string `json:"target_model"`
}

type generatePromptResponse struct {
	Prompt string `json:"prompt,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (app *App) handleGeneratePrompt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if app.promptGenerator == nil {
		writeGeneratePromptJSON(w, http.StatusServiceUnavailable, generatePromptResponse{
			Error: "Prompt generation is not configured on the server",
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPromptRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var request generatePromptRequest
	if err := decoder.Decode(&request); err != nil {
		writeGeneratePromptJSON(w, http.StatusBadRequest, generatePromptResponse{Error: "Invalid request body"})
		return
	}
	if request.ImageID <= 0 {
		writeGeneratePromptJSON(w, http.StatusBadRequest, generatePromptResponse{Error: "A valid image ID is required"})
		return
	}

	profile, ok, err := getPromptProfile(request.TargetModel)
	if !ok {
		writeGeneratePromptJSON(w, http.StatusBadRequest, generatePromptResponse{Error: "Unknown target model"})
		return
	}
	if err != nil {
		log.Printf("Failed to load prompt profile %s: %v", request.TargetModel, err)
		writeGeneratePromptJSON(w, http.StatusInternalServerError, generatePromptResponse{Error: "The target model prompt could not be loaded"})
		return
	}

	var sourcePrompt string
	if err := app.db.QueryRowContext(r.Context(), "SELECT prompt FROM images WHERE id = ?", request.ImageID).Scan(&sourcePrompt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeGeneratePromptJSON(w, http.StatusNotFound, generatePromptResponse{Error: "Image not found"})
			return
		}
		log.Printf("Failed to load prompt for image %d: %v", request.ImageID, err)
		writeGeneratePromptJSON(w, http.StatusInternalServerError, generatePromptResponse{Error: "The image prompt could not be loaded"})
		return
	}
	if strings.TrimSpace(sourcePrompt) == "" {
		writeGeneratePromptJSON(w, http.StatusUnprocessableEntity, generatePromptResponse{Error: "This image has no prompt to remix"})
		return
	}

	generatedPrompt, err := app.promptGenerator.Generate(r.Context(), profile.SystemPrompt, sourcePrompt)
	if err != nil {
		log.Printf("Prompt generation failed for image %d and profile %s: %v", request.ImageID, profile.ID, err)
		writeGeneratePromptJSON(w, http.StatusBadGateway, generatePromptResponse{Error: "The prompt provider could not generate a response"})
		return
	}

	writeGeneratePromptJSON(w, http.StatusOK, generatePromptResponse{Prompt: generatedPrompt})
}

func writeGeneratePromptJSON(w http.ResponseWriter, status int, response generatePromptResponse) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Failed to encode prompt generation response: %v", err)
	}
}
