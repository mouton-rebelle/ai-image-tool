package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CivitaiImageResponse represents the API response structure
type CivitaiImageResponse struct {
	Items []CivitaiImage `json:"items"`
	Meta  struct {
		NextPage string `json:"nextPage"`
	} `json:"metadata"`
}

// CivitaiImage represents a single image from the API
type CivitaiImage struct {
	ID     int    `json:"id"`
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	NSFW   bool   `json:"nsfw"`
	Meta   struct {
		Prompt    string `json:"prompt"`
		NegPrompt string `json:"negativePrompt"`
		Steps     int    `json:"steps"`
		CFGScale  float64 `json:"cfgScale"`
		Sampler   string `json:"sampler"`
		Scheduler string `json:"scheduler"`
		Seed      int64  `json:"seed"`
		Model     string `json:"model"`
	} `json:"meta"`
	Stats struct {
		LikeCount    int `json:"likeCount"`
		HeartCount   int `json:"heartCount"`
		CommentCount int `json:"commentCount"`
	} `json:"stats"`
}

// ImportConfig holds the configuration for Civitai import
type ImportConfig struct {
	Token              string
	Username           string
	AutoImportOnStartup bool
}

// getImportConfig reads configuration from config file or environment variables
func getImportConfig() *ImportConfig {
	// Try to load from config file first
	if config := loadConfigFromFile(); config != nil {
		return config
	}
	
	// Fallback to environment variables
	config := &ImportConfig{
		Token:              os.Getenv("CIVITAI_TOKEN"),
		Username:           getEnvOrDefault("CIVITAI_USERNAME", "moutonrebelle"),
		AutoImportOnStartup: getEnvOrDefault("AUTO_IMPORT_ON_STARTUP", "false") == "true",
	}
	return config
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// loadConfigFromFile loads configuration from civitai.config file
func loadConfigFromFile() *ImportConfig {
	file, err := os.Open("civitai.config")
	if err != nil {
		return nil // Config file doesn't exist, fallback to env vars
	}
	defer file.Close()

	config := &ImportConfig{}

	content, err := io.ReadAll(file)
	if err != nil {
		fmt.Printf("Warning: Could not read civitai.config: %v\n", err)
		return nil
	}

	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue // Skip empty lines and comments
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "CIVITAI_TOKEN":
			config.Token = value
		case "CIVITAI_USERNAME":
			config.Username = value
		case "AUTO_IMPORT_ON_STARTUP":
			config.AutoImportOnStartup = strings.ToLower(value) == "true"
		}
	}

	// Only return config if username is provided
	if config.Username != "" {
		return config
	}

	return nil
}

// importFromCivitai downloads images and prompts from Civitai API
func (app *App) importFromCivitai() error {
	config := getImportConfig()
	
	// Validate configuration
	if config.Username == "" {
		return fmt.Errorf("CIVITAI_USERNAME is required for import")
	}
	
	fmt.Printf("Starting Civitai import with config:\n")
	fmt.Printf("  Username: %s\n", config.Username)
	fmt.Printf("  Sort: Newest\n")
	fmt.Printf("  Period: AllTime\n")
	fmt.Printf("  Content: All images (SFW + NSFW)\n")
	fmt.Printf("  Token: %s\n", func() string {
		if config.Token != "" {
			return "provided"
		}
		return "not provided"
	}())
	fmt.Println()

	// Create directories
	if err := os.MkdirAll("images", 0755); err != nil {
		return fmt.Errorf("failed to create images directory: %v", err)
	}
	if err := os.MkdirAll("images_nsfw", 0755); err != nil {
		return fmt.Errorf("failed to create images_nsfw directory: %v", err)
	}

	// Load excluded words
	excludedWords := loadExcludedWords()
	fmt.Printf("Loaded %d excluded words\n", len(excludedWords))

	// Start API pagination
	page := 1
	nextPage := ""
	totalImages := 0
	totalDownloaded := 0

	for {
		fmt.Printf("\n=== Fetching page %d ===\n", page)
		
		images, nextPageURL, err := app.fetchCivitaiImages(config, nextPage)
		if err != nil {
			return fmt.Errorf("failed to fetch images: %v", err)
		}

		if len(images) == 0 {
			fmt.Println("No more images found.")
			break
		}

		fmt.Printf("Found %d images on page %d\n", len(images), page)
		totalImages += len(images)

		// Process each image
		for i, img := range images {
			fmt.Printf("Processing image %d/%d: %d\n", i+1, len(images), img.ID)

			// Download image
			downloaded, err := app.downloadImage(img)
			if err != nil {
				fmt.Printf("  Error downloading image %d: %v\n", img.ID, err)
				continue
			}

			if downloaded {
				totalDownloaded++
				fmt.Printf("  Downloaded image %d\n", img.ID)
			} else {
				fmt.Printf("  Skipped image %d (already exists)\n", img.ID)
			}

		}

		// Check if we have more pages
		if nextPageURL == "" {
			fmt.Println("No more pages available.")
			break
		}

		nextPage = nextPageURL
		page++

		// Rate limiting
		time.Sleep(1 * time.Second)
	}

	fmt.Printf("\n=== Import Summary ===\n")
	fmt.Printf("Total images processed: %d\n", totalImages)
	fmt.Printf("Total images downloaded: %d\n", totalDownloaded)

	return nil
}

// fetchCivitaiImages fetches images from the Civitai API
func (app *App) fetchCivitaiImages(config *ImportConfig, nextPage string) ([]CivitaiImage, string, error) {
	var requestURL string
	if nextPage != "" {
		requestURL = nextPage
	} else {
		// Build initial URL with proper encoding
		params := url.Values{}
		params.Set("username", config.Username)
		params.Set("sort", "Newest")
		params.Set("nsfw", "X")
		params.Set("period", "AllTime")
		params.Set("limit", "100")
		requestURL = "https://civitai.com/api/v1/images?" + params.Encode()
	}

	// Create request
	req, err := http.NewRequest("GET", requestURL, nil)
	if err != nil {
		return nil, "", err
	}

	// Add authorization header if token is provided
	if config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+config.Token)
	}

	// Make request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		// Read response body for more details
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("API returned status %d for URL %s. Response: %s", resp.StatusCode, requestURL, string(body))
	}

	// Parse response
	var apiResponse CivitaiImageResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResponse); err != nil {
		return nil, "", err
	}

	return apiResponse.Items, apiResponse.Meta.NextPage, nil
}

// downloadImage downloads an image if it doesn't already exist
func (app *App) downloadImage(img CivitaiImage) (bool, error) {
	// Determine file extension from URL
	ext := filepath.Ext(img.URL)
	if ext == "" {
		ext = ".jpg" // Default extension
	}

	// Determine directory based on NSFW flag
	dir := "images"
	if img.NSFW {
		dir = "images_nsfw"
	}

	filename := fmt.Sprintf("%d%s", img.ID, ext)
	filepath := filepath.Join(dir, filename)

	// Check if file already exists
	if _, err := os.Stat(filepath); err == nil {
		return false, nil // File already exists
	}

	// Download the image
	resp, err := http.Get(img.URL)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false, fmt.Errorf("failed to download image: status %d", resp.StatusCode)
	}

	// Create the file
	file, err := os.Create(filepath)
	if err != nil {
		return false, err
	}
	defer file.Close()

	// Copy the image data
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return false, err
	}

	return true, nil
}


// checkForNewCivitaiImages checks for new images on startup and stops as soon as it finds an already-imported image
func (app *App) checkForNewCivitaiImages() error {
	config := getImportConfig()
	
	// Check if auto-import is enabled
	if !config.AutoImportOnStartup {
		return nil
	}
	
	// Validate configuration
	if config.Username == "" {
		fmt.Println("Auto-import skipped: CIVITAI_USERNAME not configured")
		return nil
	}
	
	fmt.Printf("Checking for new Civitai images for user: %s\n", config.Username)
	
	// Create directories if they don't exist
	if err := os.MkdirAll("images", 0755); err != nil {
		return fmt.Errorf("failed to create images directory: %v", err)
	}
	if err := os.MkdirAll("images_nsfw", 0755); err != nil {
		return fmt.Errorf("failed to create images_nsfw directory: %v", err)
	}
	
	// Load excluded words
	excludedWords := loadExcludedWords()
	_ = excludedWords // Will be used by database insertion
	
	// Fetch first page of images
	images, _, err := app.fetchCivitaiImages(config, "")
	if err != nil {
		return fmt.Errorf("failed to fetch images: %v", err)
	}
	
	if len(images) == 0 {
		fmt.Println("No new images found.")
		return nil
	}
	
	newImagesCount := 0
	
	// Process each image until we find one that already exists
	for _, img := range images {
		// Check if image already exists
		ext := filepath.Ext(img.URL)
		if ext == "" {
			ext = ".jpg"
		}
		
		dir := "images"
		if img.NSFW {
			dir = "images_nsfw"
		}
		
		filename := fmt.Sprintf("%d%s", img.ID, ext)
		filepath := filepath.Join(dir, filename)
		
		// If file exists, we've reached already-imported content - stop here
		if _, err := os.Stat(filepath); err == nil {
			fmt.Printf("Reached already-imported image %d, stopping auto-import\n", img.ID)
			break
		}
		
		// Download new image
		downloaded, err := app.downloadImage(img)
		if err != nil {
			fmt.Printf("Error downloading image %d: %v\n", img.ID, err)
			continue
		}
		
		if downloaded {
			newImagesCount++
			fmt.Printf("Downloaded new image %d\n", img.ID)
			
		}
	}
	
	if newImagesCount > 0 {
		fmt.Printf("Auto-import completed: %d new images downloaded\n", newImagesCount)
	} else {
		fmt.Println("No new images found during auto-import")
	}
	
	return nil
}