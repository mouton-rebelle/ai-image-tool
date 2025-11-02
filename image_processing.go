package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nfnt/resize"
	"github.com/rwcarlsen/goexif/exif"
)

func (app *App) processImages() error {
	// Create thumbnails directory
	os.MkdirAll("thumbnails", 0755)

	// Function to get files from a directory
	getFilesFromDir := func(dir string) []string {
		patterns := []string{
			fmt.Sprintf("%s/*.jpg", dir),
			fmt.Sprintf("%s/*.jpeg", dir),
			fmt.Sprintf("%s/*.png", dir),
			fmt.Sprintf("%s/*.JPG", dir),
			fmt.Sprintf("%s/*.JPEG", dir),
			fmt.Sprintf("%s/*.PNG", dir),
		}

		var allFiles []string
		for _, pattern := range patterns {
			files, _ := filepath.Glob(pattern)
			allFiles = append(allFiles, files...)
		}
		return allFiles
	}

	// Get files from both directories
	sfwFiles := getFilesFromDir("images")
	nsfwFiles := getFilesFromDir("images_nsfw")

	// Combine all files
	allFiles := append(sfwFiles, nsfwFiles...)

	// Remove duplicates
	fileMap := make(map[string]bool)
	var uniqueFiles []string
	for _, file := range allFiles {
		if !fileMap[file] {
			fileMap[file] = true
			uniqueFiles = append(uniqueFiles, file)
		}
	}

	fmt.Printf("Found %d SFW image files\n", len(sfwFiles))
	fmt.Printf("Found %d NSFW image files\n", len(nsfwFiles))
	fmt.Printf("Total: %d unique image files\n", len(uniqueFiles))

	for i, imagePath := range uniqueFiles {
		filename := filepath.Base(imagePath)

		// Check if already processed
		var count int
		err := app.db.QueryRow("SELECT COUNT(*) FROM images WHERE filename = ?", filename).Scan(&count)
		if err != nil {
			log.Printf("Error checking database for %s: %v", filename, err)
			continue
		}

		if count > 0 {
			fmt.Printf("Skipping %s (already in database)\n", filename)
			continue
		}

		// Determine if NSFW based on directory
		isNSFW := strings.Contains(imagePath, "images_nsfw")
		nsfwStatus := "SFW"
		if isNSFW {
			nsfwStatus = "NSFW"
		}

		fmt.Printf("Processing %d/%d: %s (%s)\n", i+1, len(uniqueFiles), filename, nsfwStatus)

		// Extract metadata and create thumbnail
		metadata, err := app.extractImageMetadata(imagePath, isNSFW)
		if err != nil {
			log.Printf("Error extracting metadata for %s: %v", filename, err)
			continue
		}

		// Insert into database
		err = app.insertImageMetadata(metadata)
		if err != nil {
			log.Printf("Error inserting metadata for %s: %v", filename, err)
			continue
		}

		// Insert LoRA data
		if len(metadata.LoRAs) > 0 {
			err = app.insertLoraData(metadata.ID, metadata.LoRAs)
			if err != nil {
				log.Printf("Error inserting LoRA data for %s: %v", filename, err)
			}
		}
	}

	return nil
}

func (app *App) extractImageMetadata(imagePath string, isNSFW bool) (*ImageMetadata, error) {
	filename := filepath.Base(imagePath)

	// Parse ID from filename (assuming format: {id}.{ext})
	idStr := strings.Split(filename, ".")[0]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		// Generate a unique ID based on filename hash for non-standard filenames
		hash := 0
		for _, c := range filename {
			hash = hash*31 + int(c)
		}
		// Ensure positive ID and avoid collision with real IDs (use high numbers)
		id = 1000000000 + (hash % 1000000000)
		if id < 0 {
			id = -id
		}
		log.Printf("Could not parse ID from filename %s, generated ID: %d", filename, id)
	}

	// Open image file
	file, err := os.Open(imagePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Detect actual file type by reading magic bytes to skip video files
	fileMagicBytes := make([]byte, 12)
	file.Read(fileMagicBytes)
	file.Seek(0, 0) // Reset file pointer

	// Check for video files (MP4, WebM, AVI, MOV)
	// MP4: starts with "ftyp" at offset 4, or ftypiso at offset 4
	// WebM: starts with 0x1A 0x45 0xDF 0xA3
	// AVI: starts with "RIFF" and contains "AVI "
	if len(fileMagicBytes) >= 8 {
		// MP4 detection
		if string(fileMagicBytes[4:8]) == "ftyp" {
			return nil, fmt.Errorf("skipping video file (MP4): %s", filename)
		}
		// WebM detection
		if fileMagicBytes[0] == 0x1A && fileMagicBytes[1] == 0x45 && fileMagicBytes[2] == 0xDF && fileMagicBytes[3] == 0xA3 {
			return nil, fmt.Errorf("skipping video file (WebM): %s", filename)
		}
		// AVI detection
		if string(fileMagicBytes[0:4]) == "RIFF" && len(fileMagicBytes) >= 12 && string(fileMagicBytes[8:12]) == "AVI " {
			return nil, fmt.Errorf("skipping video file (AVI): %s", filename)
		}
		// MOV/QuickTime detection
		if string(fileMagicBytes[4:8]) == "moov" || string(fileMagicBytes[4:8]) == "mdat" {
			return nil, fmt.Errorf("skipping video file (MOV): %s", filename)
		}
	}

	// Get image dimensions
	img, _, err := image.DecodeConfig(file)
	if err != nil {
		return nil, err
	}

	metadata := &ImageMetadata{
		ID:       id,
		Filename: filename,
		Width:    img.Width,
		Height:   img.Height,
		IsNSFW:   isNSFW,
	}

	// Calculate display timestamp for chronological ordering
	metadata.DisplayTimestamp = calculateDisplayTimestamp(imagePath, id, filename)

	// Detect actual file type by magic bytes instead of relying on extension
	file.Seek(0, 0) // Reset file pointer
	signature := make([]byte, 8)
	file.Read(signature)
	file.Seek(0, 0) // Reset again for subsequent reads

	pngSignature := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	jpegSignature := []byte{0xFF, 0xD8, 0xFF}
	isPNG := len(signature) >= 8 && string(signature[:8]) == string(pngSignature)
	isJPEG := len(signature) >= 3 && string(signature[:3]) == string(jpegSignature)

	// Try PNG metadata first if it's a PNG file (regardless of extension)
	if isPNG {
		app.extractPNGMetadata(imagePath, metadata)
	}

	// If still no metadata found, try EXIF (works for JPEG and some PNGs)
	if metadata.Seed == 0 && isJPEG {
		exifData, err := exif.Decode(file)
		if err == nil {
			// Try to extract common AI generation parameters from various EXIF fields
			if userComment, err := exifData.Get(exif.UserComment); err == nil {
				if comment := string(userComment.Val); comment != "" {
					app.parseGenerationParams(comment, metadata)
				}
			}

			if imageDescription, err := exifData.Get(exif.ImageDescription); err == nil {
				if desc := string(imageDescription.Val); desc != "" {
					app.parseGenerationParams(desc, metadata)
				}
			}

			// Try other common EXIF fields that might contain AI metadata
			if software, err := exifData.Get(exif.Software); err == nil {
				if sw := string(software.Val); sw != "" {
					app.parseGenerationParams(sw, metadata)
				}
			}

			// Try Artist field
			if artist, err := exifData.Get(exif.Artist); err == nil {
				if art := string(artist.Val); art != "" {
					app.parseGenerationParams(art, metadata)
				}
			}

			// Try Copyright field
			if copyright, err := exifData.Get(exif.Copyright); err == nil {
				if cp := string(copyright.Val); cp != "" {
					app.parseGenerationParams(cp, metadata)
				}
			}
		}
	}

	// Process model information
	if metadata.ModelHash != "" {
		model, err := app.getOrCreateModel(metadata.ModelHash)
		if err != nil {
			log.Printf("Error processing model for %s: %v", filename, err)
		} else {
			metadata.ModelID = &model.ID
			// Create combined display name if we got better info from API
			if model.Name != "" && !strings.Contains(model.Name, "Unknown Model") {
				if model.VersionName != "" {
					metadata.Model = fmt.Sprintf("%s - %s", model.Name, model.VersionName)
				} else {
					metadata.Model = model.Name
				}
			}
		}
	}

	// Create thumbnail
	thumbnailPath, err := app.createThumbnail(imagePath, filename)
	if err != nil {
		log.Printf("Error creating thumbnail for %s: %v", filename, err)
	} else {
		metadata.ThumbnailPath = thumbnailPath
	}

	return metadata, nil
}

func (app *App) createThumbnail(imagePath, filename string) (string, error) {
	thumbnailPath := filepath.Join("thumbnails", filename)

	// Check if thumbnail already exists
	if _, err := os.Stat(thumbnailPath); err == nil {
		return thumbnailPath, nil
	}

	// Open original image
	file, err := os.Open(imagePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Decode image
	img, format, err := image.Decode(file)
	if err != nil {
		return "", err
	}

	// Create thumbnail (400x600 max, maintain aspect ratio)
	// Higher resolution for masonry layout
	thumbnail := resize.Thumbnail(400, 600, img, resize.Lanczos3)

	// Create thumbnail file
	thumbnailFile, err := os.Create(thumbnailPath)
	if err != nil {
		return "", err
	}
	defer thumbnailFile.Close()

	// Encode thumbnail
	switch format {
	case "png":
		err = png.Encode(thumbnailFile, thumbnail)
	default:
		err = jpeg.Encode(thumbnailFile, thumbnail, &jpeg.Options{Quality: 80})
	}

	if err != nil {
		return "", err
	}

	return thumbnailPath, nil
}

// Helper function to set the correct image URL based on NSFW flag
func (img *ImageMetadata) SetImageURL() {
	if img.IsNSFW {
		img.ImageURL = "/images_nsfw/" + img.Filename
	} else {
		img.ImageURL = "/images/" + img.Filename
	}
}

// calculateDisplayTimestamp computes a chronological timestamp for the image
func calculateDisplayTimestamp(imagePath string, imageID int, filename string) *time.Time {
	// Method 1: Check if we have real timestamp from Civitai API
	civitaiTimestamps := loadCivitaiTimestamps()
	if createdAtStr, exists := civitaiTimestamps[filename]; exists {
		if createdAt, err := time.Parse(time.RFC3339, createdAtStr); err == nil {
			return &createdAt
		} else {
			log.Printf("Warning: Failed to parse timestamp %s for %s: %v", createdAtStr, filename, err)
		}
	}

	// Method 2: For Civitai images without timestamp mapping, use ID-based fallback
	idStr := strings.Split(filename, ".")[0]
	if civitaiID, err := strconv.Atoi(idStr); err == nil {
		// This is a Civitai image but no timestamp available - use improved algorithm
		// Use more recent base date and better scaling for high IDs
		baseDate := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		timestampOffset := time.Duration(civitaiID/10000) * time.Second
		displayTime := baseDate.Add(timestampOffset)
		return &displayTime
	}

	// Method 3: For local images, use file modification time
	if fileInfo, err := os.Stat(imagePath); err == nil {
		modTime := fileInfo.ModTime()
		return &modTime
	}

	// Method 4: Fallback to current time
	now := time.Now()
	return &now
}

// loadCivitaiTimestamps loads timestamp mapping from civitai_timestamps.json
func loadCivitaiTimestamps() map[string]string {
	mapping := make(map[string]string)

	file, err := os.Open("civitai_timestamps.json")
	if err != nil {
		// File doesn't exist or can't be opened
		return mapping
	}
	defer file.Close()

	if err := json.NewDecoder(file).Decode(&mapping); err != nil {
		log.Printf("Warning: Failed to decode civitai_timestamps.json: %v", err)
		return make(map[string]string)
	}

	return mapping
}
