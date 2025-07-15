package main

import (
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

	// Try to extract EXIF data (this might contain generation parameters)
	file.Seek(0, 0) // Reset file pointer
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

	// If EXIF didn't work and this is a PNG file, try PNG text chunks
	if strings.ToLower(filepath.Ext(filename)) == ".png" && metadata.Seed == 0 {
		app.extractPNGMetadata(imagePath, metadata)
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