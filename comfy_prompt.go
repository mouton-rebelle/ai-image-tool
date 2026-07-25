package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

const (
	// ComfyUI sends the picture itself instead of an image ID, so this endpoint
	// needs a much larger budget than the in-app prompt generation route.
	maxComfyPromptRequestBytes = 24 << 20
	maxComfySourcePromptRunes  = 20000
)

type comfyGeneratePromptRequest struct {
	Prompt      string `json:"prompt"`
	ImageBase64 string `json:"image_base64"`
	TargetModel string `json:"target_model"`
	Concept     string `json:"concept"`
	Steering    string `json:"steering"`
}

// handleComfyGeneratePrompt generates a prompt for an image that lives outside
// the library: the ComfyUI bridge node posts the freshly rendered picture and
// the prompt that produced it.
func (app *App) handleComfyGeneratePrompt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if app.promptGenerator == nil {
		writeGeneratePromptJSON(w, http.StatusServiceUnavailable, generatePromptResponse{
			Error: "Prompt generation is not configured on the server",
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxComfyPromptRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var request comfyGeneratePromptRequest
	if err := decoder.Decode(&request); err != nil {
		writeGeneratePromptJSON(w, http.StatusBadRequest, generatePromptResponse{Error: "Invalid request body"})
		return
	}

	request.Prompt = strings.TrimSpace(request.Prompt)
	request.Steering = strings.TrimSpace(request.Steering)
	request.ImageBase64 = strings.TrimSpace(request.ImageBase64)

	if request.Prompt == "" && request.ImageBase64 == "" {
		writeGeneratePromptJSON(w, http.StatusBadRequest, generatePromptResponse{Error: "A source prompt or an image is required"})
		return
	}
	if len([]rune(request.Prompt)) > maxComfySourcePromptRunes {
		writeGeneratePromptJSON(w, http.StatusBadRequest, generatePromptResponse{Error: "The source prompt is too long"})
		return
	}
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

	var promptImage *PromptImage
	if request.ImageBase64 != "" {
		imageData, err := decodeBase64Image(request.ImageBase64)
		if err != nil {
			writeGeneratePromptJSON(w, http.StatusBadRequest, generatePromptResponse{Error: "The image could not be decoded"})
			return
		}
		promptImage, err = preparePromptImageFromBytes(imageData)
		if err != nil {
			log.Printf("Failed to prepare the ComfyUI image for prompt generation: %v", err)
			writeGeneratePromptJSON(w, http.StatusBadRequest, generatePromptResponse{Error: "The image could not be decoded"})
			return
		}
	}

	generatedPrompt, err := app.promptGenerator.Generate(r.Context(), PromptGenerationInput{
		SystemPrompt: profile.SystemPrompt,
		SourcePrompt: request.Prompt,
		Steering:     request.Steering,
		Image:        promptImage,
	})
	if err != nil {
		log.Printf("ComfyUI prompt generation failed for profile %s: %v", profile.ID, err)
		writeGeneratePromptJSON(w, http.StatusBadGateway, generatePromptResponse{Error: "The prompt provider could not generate a response"})
		return
	}

	log.Printf("Generated a ComfyUI prompt with profile %s (image attached: %t)", profile.ID, promptImage != nil)
	writeGeneratePromptJSON(w, http.StatusOK, generatePromptResponse{Prompt: generatedPrompt})
}

// decodeBase64Image accepts both a bare base64 payload and a data URL, and
// tolerates the line breaks some encoders insert.
func decodeBase64Image(encoded string) ([]byte, error) {
	if strings.HasPrefix(encoded, "data:") {
		separator := strings.Index(encoded, ",")
		if separator < 0 {
			return nil, fmt.Errorf("malformed image data URL")
		}
		encoded = encoded[separator+1:]
	}
	encoded = strings.Join(strings.Fields(encoded), "")
	return base64.StdEncoding.DecodeString(encoded)
}
