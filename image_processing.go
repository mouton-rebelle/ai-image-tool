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

// mediaExtensions lists the image containers we index. Videos are matched
// separately through isVideoFilename.
var mediaExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
}

// listMediaFiles returns every indexable file in a library directory.
func listMediaFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if mediaExtensions[ext] || isVideoFilename(name) {
			files = append(files, filepath.Join(dir, name))
		}
	}
	return files
}

func (app *App) processImages() error {
	// Create thumbnails directory
	os.MkdirAll("thumbnails", 0755)

	if err := ensureMediaDirs(); err != nil {
		return fmt.Errorf("failed to create media directories: %v", err)
	}

	// Videos used to land in images/ and images_nsfw/ alongside the stills.
	// Move any leftovers into their own libraries before scanning.
	if moved, duplicates := normalizeMediaLibraries(); moved > 0 || duplicates > 0 {
		fmt.Printf("Moved %d video files into the video libraries", moved)
		if duplicates > 0 {
			fmt.Printf("; moved %d duplicate downloads to temp/ for review", duplicates)
		}
		fmt.Println()
	}

	var uniqueFiles []string
	fileMap := make(map[string]bool)
	for _, dir := range mediaDirs() {
		files := listMediaFiles(dir)
		fmt.Printf("Found %d files in %s\n", len(files), dir)
		for _, file := range files {
			if !fileMap[file] {
				fileMap[file] = true
				uniqueFiles = append(uniqueFiles, file)
			}
		}
	}

	fmt.Printf("Total: %d unique media files\n", len(uniqueFiles))

	for i, imagePath := range uniqueFiles {
		filename := filepath.Base(imagePath)

		if civitaiID, ok := civitaiImageIDFromFilename(filename); ok {
			blacklisted, err := app.isCivitaiImageBlacklisted(civitaiID)
			if err != nil {
				log.Printf("Error checking deletion blacklist for %s: %v", filename, err)
				continue
			}
			if blacklisted {
				fmt.Printf("Skipping %s (previously deleted)\n", filename)
				continue
			}
		}

		// Check if already processed
		var count int
		err := app.db.QueryRow("SELECT COUNT(*) FROM images WHERE filename = ?", filename).Scan(&count)
		if err != nil {
			log.Printf("Error checking database for %s: %v", filename, err)
			continue
		}

		if count > 0 {
			if isVideoFilename(filename) {
				// Posters are a cache; regenerate the ones that went missing.
				app.refreshVideoPoster(filename)
			}
			fmt.Printf("Skipping %s (already in database)\n", filename)
			continue
		}

		// Determine if NSFW based on directory
		isNSFW := mediaDirIsNSFW(filepath.Dir(imagePath))
		nsfwStatus := "SFW"
		if isNSFW {
			nsfwStatus = "NSFW"
		}

		fmt.Printf("Processing %d/%d: %s (%s)\n", i+1, len(uniqueFiles), filename, nsfwStatus)

		// Extract metadata and create thumbnail
		metadata, err := app.extractMediaMetadata(imagePath, isNSFW)
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
	id := mediaIDFromFilename(filename)

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
		ID:        id,
		Filename:  filename,
		Width:     img.Width,
		Height:    img.Height,
		IsNSFW:    isNSFW,
		MediaType: mediaTypeImage,
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

	app.resolveMediaModel(metadata)

	// Create thumbnail
	thumbnailPath, err := app.createThumbnail(imagePath, filename)
	if err != nil {
		log.Printf("Error creating thumbnail for %s: %v", filename, err)
	} else {
		metadata.ThumbnailPath = thumbnailPath
	}

	return metadata, nil
}

// resolveMediaModel links the entry to a row in the models table, looking the
// checkpoint up on Civitai when we have its hash and falling back to the local
// checkpoint filename that ComfyUI workflows carry.
func (app *App) resolveMediaModel(metadata *ImageMetadata) {
	if metadata.ModelHash != "" {
		model, err := app.getOrCreateModel(metadata.ModelHash)
		if err != nil {
			log.Printf("Error processing model for %s: %v", metadata.Filename, err)
			return
		}

		metadata.ModelID = &model.ID
		// Create combined display name if we got better info from API
		if model.Name != "" && !strings.Contains(model.Name, "Unknown Model") {
			if model.VersionName != "" {
				metadata.Model = fmt.Sprintf("%s - %s", model.Name, model.VersionName)
			} else {
				metadata.Model = model.Name
			}
		}
		return
	}

	if metadata.Model == "" {
		return
	}

	model, err := app.getOrCreateLocalModel(metadata.Model)
	if err != nil {
		log.Printf("Error registering local model for %s: %v", metadata.Filename, err)
		return
	}
	metadata.ModelID = &model.ID
}

// extractMediaMetadata routes a file to the image or the video extractor.
func (app *App) extractMediaMetadata(mediaPath string, isNSFW bool) (*ImageMetadata, error) {
	if isVideoFilename(mediaPath) {
		return app.extractVideoMetadata(mediaPath, isNSFW)
	}
	return app.extractImageMetadata(mediaPath, isNSFW)
}

// mediaIDFromFilename derives the database id from a filename, reusing the
// Civitai id when the file is named after one.
func mediaIDFromFilename(filename string) int {
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	if id, err := strconv.Atoi(stem); err == nil {
		return id
	}

	hash := 0
	for _, c := range filename {
		hash = hash*31 + int(c)
	}
	// Ensure positive ID and avoid collision with real IDs (use high numbers)
	id := 1000000000 + (hash % 1000000000)
	if id < 0 {
		id = -id
	}
	log.Printf("Could not parse ID from filename %s, generated ID: %d", filename, id)
	return id
}

// extractVideoMetadata probes a video for its dimensions and reads the ComfyUI
// generation data that ComfyUI writes into the container metadata. Videos that
// went through Civitai's re-encoder keep those tags, so this works for imports
// as well as for local renders; older clips simply carry no tags.
func (app *App) extractVideoMetadata(videoPath string, isNSFW bool) (*ImageMetadata, error) {
	filename := filepath.Base(videoPath)

	if !videoToolsAvailable() {
		return nil, fmt.Errorf("skipping video %s: ffmpeg/ffprobe not installed", filename)
	}

	info, err := probeVideo(videoPath)
	if err != nil {
		return nil, err
	}

	metadata := &ImageMetadata{
		ID:        mediaIDFromFilename(filename),
		Filename:  filename,
		Width:     info.Width,
		Height:    info.Height,
		IsNSFW:    isNSFW,
		MediaType: mediaTypeVideo,
		Duration:  info.Duration,
		HasAudio:  info.HasAudio,
	}

	metadata.DisplayTimestamp = calculateDisplayTimestamp(videoPath, metadata.ID, filename)

	// "prompt" holds the executed API graph, "workflow" the editor graph. The
	// API graph carries the values actually used, so it is tried first.
	for _, tag := range []string{"prompt", "workflow", "parameters", "comment", "description"} {
		if metadata.Prompt != "" {
			break
		}
		if value := strings.TrimSpace(info.Tags[tag]); value != "" {
			app.parseGenerationParams(value, metadata)
		}
	}

	// Hosted generators leave no workflow, only a provenance blob. Naming the
	// tool at least keeps these clips out of the "Unknown Model" bucket.
	if metadata.Model == "" && metadata.ModelHash == "" {
		metadata.Model = identifyVideoGenerator(info.Tags)
	}

	app.resolveMediaModel(metadata)

	posterPath := thumbnailPathFor(filename)
	if err := extractVideoPoster(videoPath, posterPath, info.Duration); err != nil {
		log.Printf("Error creating poster for %s: %v", filename, err)
	} else {
		metadata.ThumbnailPath = posterPath
	}

	return metadata, nil
}

// quarantineDuplicateMedia parks a redundant download in temp/, the same place
// -clean-duplicates uses, so the user can look before anything is deleted.
func quarantineDuplicateMedia(sourcePath string) bool {
	if err := os.MkdirAll("temp", 0755); err != nil {
		log.Printf("Failed to create temp directory: %v", err)
		return false
	}

	targetPath := filepath.Join("temp", filepath.Base(sourcePath))
	if _, err := os.Stat(targetPath); err == nil {
		log.Printf("Leaving duplicate %s in place: temp already holds a file with that name", sourcePath)
		return false
	}

	if err := os.Rename(sourcePath, targetPath); err != nil {
		log.Printf("Failed to move duplicate %s to temp: %v", sourcePath, err)
		return false
	}

	return true
}

// refreshVideoPoster makes sure an already-indexed video still has a poster on
// disk, and records its path when the row does not carry one yet.
func (app *App) refreshVideoPoster(filename string) {
	posterPath, err := app.ensureThumbnail(filename)
	if err != nil {
		log.Printf("Error creating poster for %s: %v", filename, err)
		return
	}

	if _, err := app.db.Exec(
		"UPDATE images SET thumbnail_path = ? WHERE filename = ? AND (thumbnail_path IS NULL OR thumbnail_path = '')",
		posterPath, filename,
	); err != nil {
		log.Printf("Error recording poster path for %s: %v", filename, err)
	}
}

// ensureThumbnail regenerates a missing thumbnail from the source media, so the
// grid keeps working after the thumbnails directory is wiped.
func (app *App) ensureThumbnail(filename string) (string, error) {
	if filename == "" || filepath.Base(filename) != filename {
		return "", fmt.Errorf("invalid media filename")
	}

	thumbnailPath := thumbnailPathFor(filename)
	if info, err := os.Stat(thumbnailPath); err == nil && info.Size() > 0 {
		return thumbnailPath, nil
	}

	mediaPath, found := findMediaPath(filename)
	if !found {
		return "", fmt.Errorf("source media not found for %s", filename)
	}

	if isVideoFilename(filename) {
		if !videoToolsAvailable() {
			return "", fmt.Errorf("ffmpeg is not installed")
		}
		duration := 0.0
		if info, err := probeVideo(mediaPath); err == nil {
			duration = info.Duration
		}
		if err := extractVideoPoster(mediaPath, thumbnailPath, duration); err != nil {
			return "", err
		}
		return thumbnailPath, nil
	}

	return app.createThumbnail(mediaPath, filename)
}

// normalizeMediaLibraries moves videos sitting in the image libraries into
// their own, giving the ones that were downloaded under an image extension the
// extension their container actually is. When the video library already holds
// that name the file is a duplicate download, so it goes to temp/ for review
// rather than being deleted. Returns the number of files moved and quarantined.
func normalizeMediaLibraries() (int, int) {
	moves := []struct{ from, to string }{
		{dirImagesSFW, dirVideosSFW},
		{dirImagesNSFW, dirVideosNSFW},
	}

	moved, conflicts := 0, 0
	for _, move := range moves {
		entries, err := os.ReadDir(move.from)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			name := entry.Name()
			sourcePath := filepath.Join(move.from, name)

			targetName := name
			if !isVideoFilename(name) {
				extension, isVideo := sniffVideoExtension(sourcePath)
				if !isVideo {
					continue
				}
				targetName = strings.TrimSuffix(name, filepath.Ext(name)) + extension
			}

			targetPath := filepath.Join(move.to, targetName)
			if _, err := os.Stat(targetPath); err == nil {
				if quarantineDuplicateMedia(sourcePath) {
					conflicts++
				}
				continue
			}
			if err := os.Rename(sourcePath, targetPath); err != nil {
				log.Printf("Failed to move %s to %s: %v", sourcePath, targetPath, err)
				continue
			}

			if targetName != name {
				log.Printf("Moved %s to %s (the file is a video, not an image)", sourcePath, targetPath)
			}
			moved++
		}
	}

	return moved, conflicts
}

func (app *App) createThumbnail(imagePath, filename string) (string, error) {
	thumbnailPath := thumbnailPathFor(filename)

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

// SetImageURL fills in the URLs the templates use, picking the library the file
// actually lives in.
func (img *ImageMetadata) SetImageURL() {
	img.ImageURL = "/" + mediaDir(img.IsNSFW, img.IsVideo()) + "/" + img.Filename
	img.ThumbnailURL = "/thumbnails/" + thumbnailFilename(img.Filename)
}

// calculateDisplayTimestamp computes a chronological timestamp for the image
func calculateDisplayTimestamp(imagePath string, imageID int, filename string) *time.Time {
	// Method 1: Check if we have real timestamp from Civitai API
	if createdAtStr, exists := civitaiTimestampFor(filename); exists {
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

// civitaiTimestampFor looks a file's creation date up in the Civitai mapping.
// The mapping is keyed by the filename we downloaded, so a file whose extension
// was corrected afterwards is matched on its stem instead.
func civitaiTimestampFor(filename string) (string, bool) {
	timestamps := loadCivitaiTimestamps()
	if createdAt, exists := timestamps[filename]; exists {
		return createdAt, true
	}

	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	for mappedName, createdAt := range timestamps {
		if strings.TrimSuffix(mappedName, filepath.Ext(mappedName)) == stem {
			return createdAt, true
		}
	}

	return "", false
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
