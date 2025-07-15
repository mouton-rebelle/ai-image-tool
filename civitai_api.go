package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

func (app *App) fetchModelFromCivitai(hash string) (*Model, error) {
	// Clean the hash - remove any extra characters
	cleanHash := strings.TrimSpace(hash)
	if cleanHash == "" {
		return nil, fmt.Errorf("empty hash")
	}

	client := &http.Client{Timeout: 30 * time.Second}

	// Try the version-specific endpoint first (best for getting both model and version names)
	versionURL := fmt.Sprintf("https://civitai.com/api/v1/model-versions/by-hash/%s", cleanHash)
	log.Printf("Fetching model info from: %s", versionURL)

	resp, err := client.Get(versionURL)
	if err == nil && resp.StatusCode == 200 {
		defer resp.Body.Close()

		var versionResp CivitaiVersionResponse
		if err := json.NewDecoder(resp.Body).Decode(&versionResp); err == nil {
			// Create model from version response (preferred - has both names)
			model := &Model{
				Hash:        cleanHash,
				Name:        versionResp.Model.Name, // Model name (base checkpoint)
				VersionName: versionResp.Name,       // Version name
				Type:        versionResp.Model.Type,
				NSFW:        versionResp.Model.NSFW,
				Description: versionResp.Model.Description,
				BaseModel:   versionResp.BaseModel,
				CreatedAt:   versionResp.CreatedAt,
			}

			return model, nil
		}
	}
	if resp != nil {
		resp.Body.Close()
	}

	// Fallback to models endpoint
	modelsURL := fmt.Sprintf("https://civitai.com/api/v1/models?hash=%s", cleanHash)
	log.Printf("Fallback: Fetching model info from: %s", modelsURL)

	resp, err = client.Get(modelsURL)
	if err != nil {
		return nil, fmt.Errorf("error fetching from API: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var modelResp CivitaiModelResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelResp); err != nil {
		return nil, fmt.Errorf("error decoding response: %v", err)
	}

	// Create model from models response (less preferred - may not have version name)
	model := &Model{
		Hash:        cleanHash,
		Name:        modelResp.Name,
		Type:        modelResp.Type,
		NSFW:        modelResp.NSFW,
		Description: modelResp.Description,
	}

	// Get version info from first version if available
	if len(modelResp.ModelVersions) > 0 {
		model.VersionName = modelResp.ModelVersions[0].Name
		model.BaseModel = modelResp.ModelVersions[0].BaseModel
		model.CreatedAt = modelResp.ModelVersions[0].CreatedAt
	}

	return model, nil
}