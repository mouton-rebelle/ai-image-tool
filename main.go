package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"image"
	"image/jpeg"
	"image/png"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/nfnt/resize"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rwcarlsen/goexif/exif"
)

type ImageMetadata struct {
	ID          int    `json:"id"`
	Filename    string `json:"filename"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Model       string `json:"model"`
	Prompt      string `json:"prompt"`
	NegPrompt   string `json:"neg_prompt"`
	Steps       int    `json:"steps"`
	CFGScale    float64 `json:"cfg_scale"`
	Sampler     string `json:"sampler"`
	Scheduler   string `json:"scheduler"`
	Seed        int64  `json:"seed"`
	ThumbnailPath string `json:"thumbnail_path"`
	IsNSFW      bool   `json:"is_nsfw"`
	TruncatedPrompt string `json:"-"`
}

type PageData struct {
	Title string
}

type ImageGridData struct {
	Images         []ImageMetadata
	ShowPagination bool
	HasPrevious    bool
	HasNext        bool
	PreviousPage   int
	NextPage       int
	CurrentPage    int
	SearchQuery    string
	PageNumbers    []PageNumber
}

type PageNumber struct {
	Number   int
	IsActive bool
}

type App struct {
	db        *sql.DB
	templates *template.Template
}

func main() {
	app := &App{}
	
	// Initialize templates
	if err := app.initTemplates(); err != nil {
		log.Fatal("Failed to initialize templates:", err)
	}
	
	// Initialize database
	if err := app.initDB(); err != nil {
		log.Fatal("Failed to initialize database:", err)
	}
	defer app.db.Close()
	
	// Process images and create thumbnails
	if err := app.processImages(); err != nil {
		log.Fatal("Failed to process images:", err)
	}
	
	// Start HTTP server
	router := mux.NewRouter()
	app.setupRoutes(router)
	
	fmt.Println("Starting server on :8081")
	log.Fatal(http.ListenAndServe(":8081", router))
}

func (app *App) initTemplates() error {
	var err error
	app.templates, err = template.ParseGlob("templates/*.html")
	return err
}

func (app *App) initDB() error {
	var err error
	app.db, err = sql.Open("sqlite3", "./images.db")
	if err != nil {
		return err
	}
	
	// Create images table
	createTable := `
	CREATE TABLE IF NOT EXISTS images (
		id INTEGER PRIMARY KEY,
		filename TEXT UNIQUE NOT NULL,
		width INTEGER,
		height INTEGER,
		model TEXT,
		prompt TEXT,
		neg_prompt TEXT,
		steps INTEGER,
		cfg_scale REAL,
		sampler TEXT,
		scheduler TEXT,
		seed INTEGER,
		thumbnail_path TEXT,
		is_nsfw BOOLEAN DEFAULT FALSE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	
	CREATE INDEX IF NOT EXISTS idx_model ON images(model);
	CREATE INDEX IF NOT EXISTS idx_prompt ON images(prompt);
	CREATE INDEX IF NOT EXISTS idx_nsfw ON images(is_nsfw);
	`
	
	_, err = app.db.Exec(createTable)
	return err
}

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
		log.Printf("Could not parse ID from filename %s, using 0", filename)
		id = 0
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
	
	// Create thumbnail
	thumbnailPath, err := app.createThumbnail(imagePath, filename)
	if err != nil {
		log.Printf("Error creating thumbnail for %s: %v", filename, err)
	} else {
		metadata.ThumbnailPath = thumbnailPath
	}
	
	return metadata, nil
}

func (app *App) parseGenerationParams(text string, metadata *ImageMetadata) {
	// Clean Unicode encoding where spaces are inserted between characters
	cleanText := app.cleanUnicodeText(text)
	
	// Try to parse common AI generation parameters
	// Look for prompt at the beginning (often the first part before parameters)
	
	// Check if this looks like a generation parameters string
	if strings.Contains(cleanText, "Steps:") || strings.Contains(cleanText, "CFG scale:") || strings.Contains(cleanText, "Sampler:") {
		// Split by lines and collect all prompt lines before parameters
		parts := strings.Split(cleanText, "\n")
		if len(parts) > 0 && metadata.Prompt == "" {
			var promptLines []string
			
			// Collect all lines until we hit "Negative prompt:" or parameter lines
			for _, line := range parts {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" {
					// Add empty lines to preserve formatting
					promptLines = append(promptLines, trimmed)
					continue
				}
				if strings.HasPrefix(trimmed, "Negative prompt:") ||
				   strings.Contains(trimmed, "Steps:") ||
				   strings.Contains(trimmed, "CFG scale:") ||
				   strings.Contains(trimmed, "Sampler:") ||
				   strings.Contains(trimmed, "Model:") ||
				   strings.Contains(trimmed, "Seed:") ||
				   strings.Contains(trimmed, "Size:") ||
				   strings.Contains(trimmed, "Version:") {
					break
				}
				promptLines = append(promptLines, trimmed)
			}
			
			// Join all prompt lines and set as the full prompt
			if len(promptLines) > 0 {
				metadata.Prompt = strings.Join(promptLines, "\n")
			}
		}
		
		// Look for negative prompt
		if negIndex := strings.Index(cleanText, "Negative prompt:"); negIndex != -1 {
			negStart := negIndex + len("Negative prompt:")
			negEnd := strings.Index(cleanText[negStart:], "\n")
			if negEnd == -1 {
				negEnd = strings.Index(cleanText[negStart:], "Steps:")
			}
			if negEnd != -1 {
				metadata.NegPrompt = strings.TrimSpace(cleanText[negStart : negStart+negEnd])
			} else {
				metadata.NegPrompt = strings.TrimSpace(cleanText[negStart:])
			}
		}
	} else if metadata.Prompt == "" {
		// If no generation parameters found, treat the whole text as prompt
		metadata.Prompt = cleanText
	}
	
	// Extract numeric parameters
	if strings.Contains(cleanText, "Steps:") {
		if steps := extractParam(cleanText, "Steps:", ","); steps != "" {
			if s, err := strconv.Atoi(strings.TrimSpace(steps)); err == nil {
				metadata.Steps = s
			}
		}
	}
	
	if strings.Contains(cleanText, "CFG scale:") {
		if cfg := extractParam(cleanText, "CFG scale:", ","); cfg != "" {
			if c, err := strconv.ParseFloat(strings.TrimSpace(cfg), 64); err == nil {
				metadata.CFGScale = c
			}
		}
	}
	
	if strings.Contains(cleanText, "Sampler:") {
		sampler := extractParam(cleanText, "Sampler:", ",")
		if sampler != "" {
			metadata.Sampler = strings.TrimSpace(sampler)
		}
	}
	
	if strings.Contains(cleanText, "Schedule type:") {
		scheduler := extractParam(cleanText, "Schedule type:", ",")
		if scheduler != "" {
			metadata.Scheduler = strings.TrimSpace(scheduler)
		}
	} else if strings.Contains(cleanText, "Scheduler:") {
		scheduler := extractParam(cleanText, "Scheduler:", ",")
		if scheduler != "" {
			metadata.Scheduler = strings.TrimSpace(scheduler)
		}
	}
	
	if strings.Contains(cleanText, "Model:") {
		model := extractParam(cleanText, "Model:", ",")
		if model != "" {
			metadata.Model = strings.TrimSpace(model)
		}
	}
	
	if strings.Contains(cleanText, "Seed:") {
		if seed := extractParam(cleanText, "Seed:", ","); seed != "" {
			if s, err := strconv.ParseInt(strings.TrimSpace(seed), 10, 64); err == nil {
				metadata.Seed = s
			}
		}
	}
}

func (app *App) cleanUnicodeText(text string) string {
	// Remove "UNICODE" prefix if present
	cleanText := strings.TrimPrefix(text, "UNICODE")
	cleanText = strings.TrimSpace(cleanText)
	
	// Handle null-separated Unicode text (common in EXIF)
	// Look for pattern where every other byte is null (0x00)
	if len(cleanText) > 10 {
		nullCount := 0
		totalOddPositions := 0
		
		for i := 1; i < len(cleanText) && i < 100; i += 2 {
			totalOddPositions++
			if cleanText[i] == 0 {
				nullCount++
			}
		}
		
		// If more than 80% of odd positions are null bytes, this is Unicode-encoded text
		if totalOddPositions > 0 && float64(nullCount)/float64(totalOddPositions) > 0.8 {
			var result strings.Builder
			for i := 0; i < len(cleanText); i += 2 {
				if i < len(cleanText) && cleanText[i] != 0 {
					result.WriteByte(cleanText[i])
				}
			}
			return result.String()
		}
		
		// Also check for space-separated pattern
		spaceCount := 0
		for i := 1; i < len(cleanText) && i < 100; i += 2 {
			if cleanText[i] == ' ' {
				spaceCount++
			}
		}
		
		// If more than 80% of odd positions are spaces, this is space-separated Unicode
		if totalOddPositions > 0 && float64(spaceCount)/float64(totalOddPositions) > 0.8 {
			var result strings.Builder
			for i := 0; i < len(cleanText); i += 2 {
				if i < len(cleanText) {
					result.WriteByte(cleanText[i])
				}
			}
			return result.String()
		}
	}
	
	return cleanText
}

func extractParam(text, prefix, suffix string) string {
	start := strings.Index(text, prefix)
	if start == -1 {
		return ""
	}
	start += len(prefix)
	
	end := strings.Index(text[start:], suffix)
	if end == -1 {
		return strings.TrimSpace(text[start:])
	}
	
	return strings.TrimSpace(text[start : start+end])
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

func (app *App) insertImageMetadata(metadata *ImageMetadata) error {
	query := `
	INSERT INTO images (id, filename, width, height, model, prompt, neg_prompt, steps, cfg_scale, sampler, scheduler, seed, thumbnail_path, is_nsfw)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	
	_, err := app.db.Exec(query,
		metadata.ID,
		metadata.Filename,
		metadata.Width,
		metadata.Height,
		metadata.Model,
		metadata.Prompt,
		metadata.NegPrompt,
		metadata.Steps,
		metadata.CFGScale,
		metadata.Sampler,
		metadata.Scheduler,
		metadata.Seed,
		metadata.ThumbnailPath,
		metadata.IsNSFW,
	)
	
	return err
}

func (app *App) setupRoutes(router *mux.Router) {
	// Serve static files
	router.PathPrefix("/images/").Handler(http.StripPrefix("/images/", http.FileServer(http.Dir("./images/"))))
	router.PathPrefix("/images_nsfw/").Handler(http.StripPrefix("/images_nsfw/", http.FileServer(http.Dir("./images_nsfw/"))))
	router.PathPrefix("/thumbnails/").Handler(http.StripPrefix("/thumbnails/", http.FileServer(http.Dir("./thumbnails/"))))
	router.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.Dir("./static/"))))
	
	// API routes
	router.HandleFunc("/", app.handleIndex).Methods("GET")
	router.HandleFunc("/api/images", app.handleAPIImages).Methods("GET")
	router.HandleFunc("/search", app.handleSearch).Methods("GET")
}

func (app *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Civitai Image Viewer",
	}
	
	w.Header().Set("Content-Type", "text/html")
	err := app.templates.ExecuteTemplate(w, "layout.html", data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (app *App) handleAPIImages(w http.ResponseWriter, r *http.Request) {
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}
	
	limit := 300
	offset := (page - 1) * limit
	
	// Check for NSFW filter
	nsfwFilter := r.URL.Query().Get("nsfw")
	
	// Get total count with NSFW filter
	var total int
	var countQuery string
	if nsfwFilter == "sfw" {
		countQuery = "SELECT COUNT(*) FROM images WHERE is_nsfw = 0"
	} else if nsfwFilter == "nsfw" {
		countQuery = "SELECT COUNT(*) FROM images WHERE is_nsfw = 1"
	} else {
		countQuery = "SELECT COUNT(*) FROM images"
	}
	
	err := app.db.QueryRow(countQuery).Scan(&total)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	var query string
	var args []interface{}
	
	if nsfwFilter == "sfw" {
		query = `
		SELECT id, filename, width, height, model, prompt, neg_prompt, steps, cfg_scale, sampler, scheduler, seed, thumbnail_path, is_nsfw
		FROM images 
		WHERE is_nsfw = 0
		ORDER BY id DESC 
		LIMIT ? OFFSET ?
		`
		args = []interface{}{limit, offset}
	} else if nsfwFilter == "nsfw" {
		query = `
		SELECT id, filename, width, height, model, prompt, neg_prompt, steps, cfg_scale, sampler, scheduler, seed, thumbnail_path, is_nsfw
		FROM images 
		WHERE is_nsfw = 1
		ORDER BY id DESC 
		LIMIT ? OFFSET ?
		`
		args = []interface{}{limit, offset}
	} else {
		// Show all images
		query = `
		SELECT id, filename, width, height, model, prompt, neg_prompt, steps, cfg_scale, sampler, scheduler, seed, thumbnail_path, is_nsfw
		FROM images 
		ORDER BY id DESC 
		LIMIT ? OFFSET ?
		`
		args = []interface{}{limit, offset}
	}
	
	rows, err := app.db.Query(query, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	
	var images []ImageMetadata
	for rows.Next() {
		var img ImageMetadata
		err := rows.Scan(&img.ID, &img.Filename, &img.Width, &img.Height, 
			&img.Model, &img.Prompt, &img.NegPrompt, &img.Steps, &img.CFGScale, 
			&img.Sampler, &img.Scheduler, &img.Seed, &img.ThumbnailPath, &img.IsNSFW)
		if err != nil {
			log.Printf("Error scanning row: %v", err)
			continue
		}
		images = append(images, img)
	}
	
	app.renderImageGrid(w, images, page, total, limit, "")
}

func (app *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	nsfwFilter := r.URL.Query().Get("nsfw")
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}
	
	limit := 300
	offset := (page - 1) * limit
	
	var images []ImageMetadata
	var total int
	
	// Build WHERE clause for NSFW filter
	var nsfwWhere string
	if nsfwFilter == "sfw" {
		nsfwWhere = " AND is_nsfw = 0"
	} else if nsfwFilter == "nsfw" {
		nsfwWhere = " AND is_nsfw = 1"
	}
	
	if query == "" {
		// No search query, return all images with NSFW filter
		countQuery := "SELECT COUNT(*) FROM images WHERE 1=1" + nsfwWhere
		app.db.QueryRow(countQuery).Scan(&total)
		
		selectQuery := `
			SELECT id, filename, width, height, model, prompt, neg_prompt, steps, cfg_scale, sampler, scheduler, seed, thumbnail_path, is_nsfw
			FROM images 
			WHERE 1=1` + nsfwWhere + `
			ORDER BY id DESC 
			LIMIT ? OFFSET ?
		`
		rows, err := app.db.Query(selectQuery, limit, offset)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var img ImageMetadata
				rows.Scan(&img.ID, &img.Filename, &img.Width, &img.Height, 
					&img.Model, &img.Prompt, &img.NegPrompt, &img.Steps, &img.CFGScale, 
					&img.Sampler, &img.Scheduler, &img.Seed, &img.ThumbnailPath, &img.IsNSFW)
				images = append(images, img)
			}
		}
	} else {
		// Search query provided
		searchParam := "%" + query + "%"
		
		countQuery := `
			SELECT COUNT(*) FROM images 
			WHERE (model LIKE ? OR prompt LIKE ?)` + nsfwWhere
		app.db.QueryRow(countQuery, searchParam, searchParam).Scan(&total)
		
		selectQuery := `
			SELECT id, filename, width, height, model, prompt, neg_prompt, steps, cfg_scale, sampler, scheduler, seed, thumbnail_path, is_nsfw
			FROM images 
			WHERE (model LIKE ? OR prompt LIKE ?)` + nsfwWhere + `
			ORDER BY id DESC 
			LIMIT ? OFFSET ?
		`
		rows, err := app.db.Query(selectQuery, searchParam, searchParam, limit, offset)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var img ImageMetadata
				rows.Scan(&img.ID, &img.Filename, &img.Width, &img.Height, 
					&img.Model, &img.Prompt, &img.NegPrompt, &img.Steps, &img.CFGScale, 
					&img.Sampler, &img.Scheduler, &img.Seed, &img.ThumbnailPath, &img.IsNSFW)
				images = append(images, img)
			}
		}
	}
	
	app.renderImageGrid(w, images, page, total, limit, query)
}

func (app *App) renderImageGrid(w http.ResponseWriter, images []ImageMetadata, page, total, limit int, searchQuery string) {
	totalPages := (total + limit - 1) / limit
	
	// Truncate prompts for display
	for i := range images {
		if len(images[i].Prompt) > 100 {
			images[i].TruncatedPrompt = images[i].Prompt[:100] + "..."
		} else {
			images[i].TruncatedPrompt = images[i].Prompt
		}
	}
	
	// Build pagination data
	var pageNumbers []PageNumber
	showPagination := totalPages > 1
	
	if showPagination {
		start := max(1, page-2)
		end := min(totalPages, page+2)
		
		for i := start; i <= end; i++ {
			pageNumbers = append(pageNumbers, PageNumber{
				Number:   i,
				IsActive: i == page,
			})
		}
	}
	
	// URL encode search query
	encodedQuery := url.QueryEscape(searchQuery)
	
	data := ImageGridData{
		Images:         images,
		ShowPagination: showPagination,
		HasPrevious:    page > 1,
		HasNext:        page < totalPages,
		PreviousPage:   page - 1,
		NextPage:       page + 1,
		CurrentPage:    page,
		SearchQuery:    encodedQuery,
		PageNumbers:    pageNumbers,
	}
	
	w.Header().Set("Content-Type", "text/html")
	err := app.templates.ExecuteTemplate(w, "image-grid.html", data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}