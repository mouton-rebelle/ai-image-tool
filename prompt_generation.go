package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
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
	defaultPromptLLMBaseURL      = "https://api.x.ai/v1"
	defaultPromptLLMModel        = "grok-4.5"
	defaultPromptReasoningEffort = "medium"
	maxPromptRequestBytes        = 8 << 10
	maxPromptResponseBytes       = 2 << 20
	maxPromptSteeringCharacters  = 2000
	promptLLMRequestTimeout      = 6 * time.Minute
)

// PromptGenerator is the provider-neutral boundary used by the application.
// Providers turn a system instruction, source prompt, and optional visual
// reference into text.
type PromptGenerator interface {
	Generate(ctx context.Context, input PromptGenerationInput) (string, error)
}

type PromptGenerationInput struct {
	SystemPrompt string
	SourcePrompt string
	Steering     string
	Image        *PromptImage
}

type PromptProfile struct {
	ID           string
	Name         string
	TargetModel  string
	Concept      string
	SystemPrompt string
}

type promptProfileDefinition struct {
	Name             string
	SystemPromptPath string
}

var promptModelDefinitions = map[string]promptProfileDefinition{
	"anima": {
		Name:             "Anima",
		SystemPromptPath: "prompt_systems/anima.md",
	},
	"krea-2": {
		Name:             "Krea 2",
		SystemPromptPath: "prompt_systems/krea-2.md",
	},
}

var promptConceptDefinitions = map[string]promptProfileDefinition{
	"describe": {
		Name:             "Describe",
		SystemPromptPath: "prompt_systems/concepts/describe.md",
	},
	"remix": {
		Name:             "Remix",
		SystemPromptPath: "prompt_systems/concepts/remix.md",
	},
	"next": {
		Name:             "Next",
		SystemPromptPath: "prompt_systems/concepts/next.md",
	},
	"before": {
		Name:             "Before",
		SystemPromptPath: "prompt_systems/concepts/before.md",
	},
}

func normalizePromptSelection(targetModel, concept string) (string, string) {
	targetModel = strings.TrimSpace(targetModel)
	concept = strings.TrimSpace(concept)

	switch targetModel {
	case "anima-next":
		targetModel = "anima"
		if concept == "" {
			concept = "next"
		}
	case "krea-2-next":
		targetModel = "krea-2"
		if concept == "" {
			concept = "next"
		}
	}

	if concept == "" {
		concept = "remix"
	}

	return targetModel, concept
}

func readPromptInstructions(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read system prompt %s: %w", path, err)
	}
	instructions := strings.TrimSpace(string(content))
	if instructions == "" {
		return "", fmt.Errorf("system prompt %s is empty", path)
	}
	return instructions, nil
}

func getPromptProfile(targetModel, concept string) (PromptProfile, bool, error) {
	targetModel, concept = normalizePromptSelection(targetModel, concept)

	modelDefinition, ok := promptModelDefinitions[targetModel]
	if !ok {
		return PromptProfile{}, false, nil
	}
	conceptDefinition, ok := promptConceptDefinitions[concept]
	if !ok {
		return PromptProfile{}, false, nil
	}

	modelInstructions, err := readPromptInstructions(modelDefinition.SystemPromptPath)
	if err != nil {
		return PromptProfile{}, true, err
	}
	conceptInstructions, err := readPromptInstructions(conceptDefinition.SystemPromptPath)
	if err != nil {
		return PromptProfile{}, true, err
	}

	return PromptProfile{
		ID:           targetModel + ":" + concept,
		Name:         modelDefinition.Name + " · " + conceptDefinition.Name,
		TargetModel:  targetModel,
		Concept:      concept,
		SystemPrompt: modelInstructions + "\n\nCreative operation — " + conceptDefinition.Name + ":\n\n" + conceptInstructions,
	}, true, nil
}

// OpenAICompatiblePromptGenerator works with xAI and other providers exposing
// the OpenAI chat-completions contract.
type OpenAICompatiblePromptGenerator struct {
	apiKey          string
	baseURL         string
	model           string
	reasoningEffort string
	client          *http.Client
}

type chatCompletionRequest struct {
	Model               string                         `json:"model"`
	Messages            []chatCompletionRequestMessage `json:"messages"`
	ReasoningEffort     string                         `json:"reasoning_effort,omitempty"`
	MaxCompletionTokens int                            `json:"max_completion_tokens,omitempty"`
}

type chatCompletionRequestMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type chatCompletionContentPart struct {
	Type     string                  `json:"type"`
	Text     string                  `json:"text,omitempty"`
	ImageURL *chatCompletionImageURL `json:"image_url,omitempty"`
}

type chatCompletionImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type chatCompletionResponseMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatCompletionResponseMessage `json:"message"`
	} `json:"choices"`
	Error json.RawMessage      `json:"error,omitempty"`
	Usage *chatCompletionUsage `json:"usage,omitempty"`
}

type chatCompletionUsage struct {
	PromptTokens            int                               `json:"prompt_tokens"`
	CompletionTokens        int                               `json:"completion_tokens"`
	TotalTokens             int                               `json:"total_tokens"`
	PromptTokensDetails     chatCompletionPromptTokensDetails `json:"prompt_tokens_details"`
	CompletionTokensDetails chatCompletionOutputTokensDetails `json:"completion_tokens_details"`
}

type chatCompletionPromptTokensDetails struct {
	TextTokens   int `json:"text_tokens"`
	ImageTokens  int `json:"image_tokens"`
	CachedTokens int `json:"cached_tokens"`
}

type chatCompletionOutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
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

	reasoningEffort := firstNonEmptyEnv("PROMPT_LLM_REASONING_EFFORT", "XAI_REASONING_EFFORT")
	if reasoningEffort == "" {
		reasoningEffort = defaultPromptReasoningEffort
	}

	return &OpenAICompatiblePromptGenerator{
		apiKey:          apiKey,
		baseURL:         strings.TrimRight(baseURL, "/"),
		model:           model,
		reasoningEffort: reasoningEffort,
		client:          &http.Client{Timeout: promptLLMRequestTimeout},
	}, fmt.Sprintf("model %q (reasoning effort: %s) at %s", model, reasoningEffort, strings.TrimRight(baseURL, "/"))
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func (g *OpenAICompatiblePromptGenerator) Generate(ctx context.Context, input PromptGenerationInput) (string, error) {
	requestBody := chatCompletionRequest{
		Model:               g.model,
		ReasoningEffort:     g.reasoningEffort,
		Messages:            promptChatMessages(input),
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
	if completion.Usage != nil {
		log.Printf(
			"Prompt generation tokens: prompt=%d text=%d image=%d cached=%d completion=%d reasoning=%d total=%d",
			completion.Usage.PromptTokens,
			completion.Usage.PromptTokensDetails.TextTokens,
			completion.Usage.PromptTokensDetails.ImageTokens,
			completion.Usage.PromptTokensDetails.CachedTokens,
			completion.Usage.CompletionTokens,
			completion.Usage.CompletionTokensDetails.ReasoningTokens,
			completion.Usage.TotalTokens,
		)
	}

	return generatedPrompt, nil
}

func promptChatMessages(input PromptGenerationInput) []chatCompletionRequestMessage {
	messages := []chatCompletionRequestMessage{
		{Role: "system", Content: input.SystemPrompt},
	}
	userText := promptGenerationUserText(input.SourcePrompt, input.Steering)
	if input.Image == nil {
		return append(messages, chatCompletionRequestMessage{Role: "user", Content: userText})
	}

	mediaType := strings.TrimSpace(input.Image.MediaType)
	if mediaType == "" {
		mediaType = "image/jpeg"
	}
	detail := strings.TrimSpace(input.Image.Detail)
	if detail == "" {
		detail = promptImageDetail
	}
	dataURL := "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(input.Image.Data)
	content := []chatCompletionContentPart{
		{
			Type: "image_url",
			ImageURL: &chatCompletionImageURL{
				URL:    dataURL,
				Detail: detail,
			},
		},
		{
			Type: "text",
			Text: "Use the attached image as the visual reference and the following source prompt as semantic guidance. " +
				"Produce the requested prompt according to the system instructions.\n\n" + userText,
		},
	}

	return append(messages, chatCompletionRequestMessage{Role: "user", Content: content})
}

func promptGenerationUserText(sourcePrompt, steering string) string {
	text := "Source prompt:\n" + strings.TrimSpace(sourcePrompt)
	if steering = strings.TrimSpace(steering); steering != "" {
		text += "\n\nUser creative direction:\n" + steering
	}
	return text
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
	Concept     string `json:"concept"`
	Steering    string `json:"steering"`
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
	request.Steering = strings.TrimSpace(request.Steering)
	if len([]rune(request.Steering)) > maxPromptSteeringCharacters {
		writeGeneratePromptJSON(w, http.StatusBadRequest, generatePromptResponse{Error: "Creative direction is too long"})
		return
	}

	profile, ok, err := getPromptProfile(request.TargetModel, request.Concept)
	if !ok {
		writeGeneratePromptJSON(w, http.StatusBadRequest, generatePromptResponse{Error: "Unknown target model or concept"})
		return
	}
	if err != nil {
		log.Printf("Failed to load prompt profile %s/%s: %v", request.TargetModel, request.Concept, err)
		writeGeneratePromptJSON(w, http.StatusInternalServerError, generatePromptResponse{Error: "The prompt profile could not be loaded"})
		return
	}

	var sourcePrompt, filename string
	var isNSFW bool
	if err := app.db.QueryRowContext(r.Context(), "SELECT prompt, filename, is_nsfw FROM images WHERE id = ?", request.ImageID).Scan(&sourcePrompt, &filename, &isNSFW); err != nil {
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
	imagePath, err := app.promptImagePath(filename, isNSFW)
	if err != nil {
		log.Printf("Failed to resolve image %d for prompt generation: %v", request.ImageID, err)
		writeGeneratePromptJSON(w, http.StatusInternalServerError, generatePromptResponse{Error: "The source image could not be loaded"})
		return
	}
	promptImage, err := preparePromptImage(imagePath)
	if err != nil {
		log.Printf("Failed to prepare image %d for prompt generation: %v", request.ImageID, err)
		writeGeneratePromptJSON(w, http.StatusInternalServerError, generatePromptResponse{Error: "The source image could not be loaded"})
		return
	}

	generatedPrompt, err := app.promptGenerator.Generate(r.Context(), PromptGenerationInput{
		SystemPrompt: profile.SystemPrompt,
		SourcePrompt: sourcePrompt,
		Steering:     request.Steering,
		Image:        promptImage,
	})
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
