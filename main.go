package main

import (
	"database/sql"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	_ "github.com/mattn/go-sqlite3"
)

type Model struct {
	ID          int    `json:"id"`
	Hash        string `json:"hash"`
	Name        string `json:"name"`         // Model name (base checkpoint)
	VersionName string `json:"version_name"` // Version name
	Type        string `json:"type"`
	NSFW        bool   `json:"nsfw"`
	Description string `json:"description"`
	BaseModel   string `json:"base_model"`
	CreatedAt   string `json:"created_at"`
}

type CivitaiModelResponse struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Type          string `json:"type"`
	NSFW          bool   `json:"nsfw"`
	ModelVersions []struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		BaseModel string `json:"baseModel"`
		CreatedAt string `json:"createdAt"`
		Files     []struct {
			Hashes struct {
				SHA256 string `json:"SHA256"`
				BLAKE3 string `json:"BLAKE3"`
				CRC32  string `json:"CRC32"`
				AutoV1 string `json:"AutoV1"`
				AutoV2 string `json:"AutoV2"`
			} `json:"hashes"`
		} `json:"files"`
	} `json:"modelVersions"`
}

type CivitaiVersionResponse struct {
	ID        int    `json:"id"`
	Name      string `json:"name"` // Version name
	BaseModel string `json:"baseModel"`
	CreatedAt string `json:"createdAt"`
	Model     struct {
		ID          int    `json:"id"`
		Name        string `json:"name"` // Model name
		Description string `json:"description"`
		Type        string `json:"type"`
		NSFW        bool   `json:"nsfw"`
	} `json:"model"`
}

type ImageMetadata struct {
	ID              int       `json:"id"`
	Filename        string    `json:"filename"`
	Width           int       `json:"width"`
	Height          int       `json:"height"`
	ModelID         *int      `json:"model_id"`
	Model           string    `json:"model"` // For display purposes
	ModelHash       string    `json:"model_hash"`
	Prompt          string    `json:"prompt"`
	NegPrompt       string    `json:"neg_prompt"`
	Steps           int       `json:"steps"`
	CFGScale        float64   `json:"cfg_scale"`
	Sampler         string    `json:"sampler"`
	Scheduler       string    `json:"scheduler"`
	Seed            int64     `json:"seed"`
	ThumbnailPath   string    `json:"thumbnail_path"`
	IsNSFW          bool      `json:"is_nsfw"`
	ImageURL        string    `json:"image_url"`      // Full URL to the image
	TruncatedPrompt string    `json:"-"`
	LoRAs           []LoraData `json:"loras"` // LoRA data for JSON and template display
}

type ModelStat struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	VersionName string `json:"version_name"`
	ImageCount  int    `json:"image_count"`
}

type PageData struct {
	Title           string
	TotalCount      int
	SearchQuery     string
	NSFWFilter      string
	Models          []ModelStat
	InitialURL      string
	SelectedModelID int
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
	TotalCount     int
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
	// Parse command line flags
	clearImages := flag.Bool("clear-images", false, "Clear images and loras tables (preserves models)")
	importImages := flag.Bool("import-civitai", false, "Import images and prompts from Civitai API")
	help := flag.Bool("help", false, "Show usage information")
	flag.Parse()

	if *help {
		fmt.Println("AI Generated Image Viewer")
		fmt.Println("Usage:")
		fmt.Println("  ./ai-generated-image-viewer                # Run the web server")
		fmt.Println("  ./ai-generated-image-viewer -clear-images  # Clear images and LoRAs tables")
		fmt.Println("  ./ai-generated-image-viewer -import-civitai # Import images from Civitai API")
		fmt.Println("  ./ai-generated-image-viewer -help          # Show this help")
		fmt.Println("")
		fmt.Println("Configuration for Import:")
		fmt.Println("  1. Create civitai.config file (copy from civitai.config.example)")
		fmt.Println("  2. Or use environment variables:")
		fmt.Println("     CIVITAI_TOKEN           # API token (optional, for higher rate limits)")
		fmt.Println("     CIVITAI_USERNAME        # Username to fetch from (default: moutonrebelle)")
		fmt.Println("     AUTO_IMPORT_ON_STARTUP  # Enable auto-import on startup (true/false)")
		fmt.Println("")
		fmt.Println("  Auto-import feature:")
		fmt.Println("    When AUTO_IMPORT_ON_STARTUP=true, the app will check for new images")
		fmt.Println("    on startup and stop as soon as it finds an already-imported image.")
		fmt.Println("    This keeps your collection up-to-date without re-downloading everything.")
		fmt.Println("")
		fmt.Println("  Note: Sort order is fixed to 'Newest', Period is fixed to 'AllTime'")
		fmt.Println("        Content includes all images (both SFW and NSFW)")
		os.Exit(0)
	}

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

	// Handle clear-images flag
	if *clearImages {
		if err := app.clearImagesTables(); err != nil {
			log.Fatal("Failed to clear images tables:", err)
		}
		fmt.Println("Images and LoRAs tables cleared successfully.")
		fmt.Println("Models table preserved. You can now run the application normally.")
		os.Exit(0)
	}

	// Handle import-civitai flag
	if *importImages {
		if err := app.importFromCivitai(); err != nil {
			log.Fatal("Failed to import from Civitai:", err)
		}
		fmt.Println("Civitai import completed successfully.")
		os.Exit(0)
	}

	// Check for new Civitai images on startup if auto-import is enabled
	if err := app.checkForNewCivitaiImages(); err != nil {
		log.Printf("Warning: Auto-import failed: %v", err)
	}

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
	// Check for URL parameters
	promptQuery := r.URL.Query().Get("q")
	modelFilter := r.URL.Query().Get("model")
	nsfwFilter := r.URL.Query().Get("nsfw")

	// Parse selected model ID
	var selectedModelID int
	if modelFilter != "" && modelFilter != "all" {
		if parsed, err := strconv.Atoi(modelFilter); err == nil {
			selectedModelID = parsed
		}
	}

	// Default to ALL filter if not specified (to preserve existing behavior)
	if nsfwFilter == "" {
		nsfwFilter = "all"
	}

	// Get total count based on current filters
	var totalCount int
	var countQuery string
	var args []any

	// Build count query with filters
	whereClause := "WHERE 1=1"
	if nsfwFilter == "sfw" {
		whereClause += " AND i.is_nsfw = 0"
	} else if nsfwFilter == "nsfw" {
		whereClause += " AND i.is_nsfw = 1"
	}

	if modelFilter != "" && modelFilter != "all" {
		whereClause += " AND i.model_id = ?"
		args = append(args, modelFilter)
	}

	if promptQuery != "" {
		whereClause += " AND i.prompt LIKE ?"
		searchTerm := "%" + promptQuery + "%"
		args = append(args, searchTerm)
	}

	countQuery = "SELECT COUNT(*) FROM images i LEFT JOIN models m ON i.model_id = m.id " + whereClause

	if len(args) > 0 {
		err := app.db.QueryRow(countQuery, args...).Scan(&totalCount)
		if err != nil {
			log.Printf("Error getting total count: %v", err)
			totalCount = 0
		}
	} else {
		err := app.db.QueryRow(countQuery).Scan(&totalCount)
		if err != nil {
			log.Printf("Error getting total count: %v", err)
			totalCount = 0
		}
	}

	// Get model statistics
	models, err := app.getModelStats()
	if err != nil {
		log.Printf("Error getting model stats: %v", err)
		models = []ModelStat{}
	}

	// Build initial URL for HTMX request
	var initialURL string
	if promptQuery != "" || modelFilter != "" {
		params := url.Values{}
		if promptQuery != "" {
			params.Set("q", promptQuery)
		}
		if modelFilter != "" && modelFilter != "all" {
			params.Set("model", modelFilter)
		}
		params.Set("nsfw", nsfwFilter)
		params.Set("page", "1")
		initialURL = "/search?" + params.Encode()
	} else {
		initialURL = "/api/images?page=1&nsfw=" + nsfwFilter
	}

	data := PageData{
		Title:           "AI Generated Images Viewer",
		TotalCount:      totalCount,
		SearchQuery:     promptQuery,
		NSFWFilter:      nsfwFilter,
		Models:          models,
		InitialURL:      initialURL,
		SelectedModelID: selectedModelID,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err = app.templates.ExecuteTemplate(w, "layout.html", data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ImageSearchParams holds the parameters for searching images
type ImageSearchParams struct {
	Page        int
	Limit       int
	NSFWFilter  string
	ModelFilter string
	PromptQuery string
}

// parseImageSearchParams extracts search parameters from HTTP request
func parseImageSearchParams(r *http.Request) ImageSearchParams {
	params := ImageSearchParams{
		Page:        1,
		Limit:       300,
		NSFWFilter:  r.URL.Query().Get("nsfw"),
		ModelFilter: r.URL.Query().Get("model"),
		PromptQuery: r.URL.Query().Get("q"),
	}

	if p := r.URL.Query().Get("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			params.Page = parsed
		}
	}

	return params
}

// queryImages performs the unified image search with given parameters
func (app *App) queryImages(params ImageSearchParams) ([]ImageMetadata, int, error) {
	offset := (params.Page - 1) * params.Limit

	// Build WHERE clause components
	var whereConditions []string
	var args []interface{}

	// NSFW filter
	if params.NSFWFilter == "sfw" {
		whereConditions = append(whereConditions, "i.is_nsfw = 0")
	} else if params.NSFWFilter == "nsfw" {
		whereConditions = append(whereConditions, "i.is_nsfw = 1")
	}

	// Model filter
	if params.ModelFilter != "" && params.ModelFilter != "all" {
		whereConditions = append(whereConditions, "i.model_id = ?")
		args = append(args, params.ModelFilter)
	}

	// Prompt search (only positive prompts)
	if params.PromptQuery != "" {
		whereConditions = append(whereConditions, "i.prompt LIKE ?")
		args = append(args, "%"+params.PromptQuery+"%")
	}

	// Build complete WHERE clause
	whereClause := ""
	if len(whereConditions) > 0 {
		whereClause = "WHERE " + strings.Join(whereConditions, " AND ")
	}

	// Count query
	countQuery := "SELECT COUNT(*) FROM images i LEFT JOIN models m ON i.model_id = m.id " + whereClause
	var total int
	if len(args) > 0 {
		err := app.db.QueryRow(countQuery, args...).Scan(&total)
		if err != nil {
			return nil, 0, err
		}
	} else {
		err := app.db.QueryRow(countQuery).Scan(&total)
		if err != nil {
			return nil, 0, err
		}
	}

	// Select query with LEFT JOIN to loras table
	selectQuery := `
		SELECT i.id, i.filename, i.width, i.height, 
		       CASE 
		           WHEN m.name IS NOT NULL AND m.version_name IS NOT NULL THEN m.name || ' - ' || m.version_name
		           WHEN m.name IS NOT NULL THEN m.name
		           ELSE 'Unknown Model'
		       END as model_display,
		       i.prompt, i.neg_prompt, i.steps, i.cfg_scale, i.sampler, i.scheduler, i.seed, i.thumbnail_path, i.is_nsfw,
		       l.name as lora_name, l.weight as lora_weight
		FROM images i
		LEFT JOIN models m ON i.model_id = m.id
		LEFT JOIN (
			SELECT DISTINCT image_id, name, weight 
			FROM loras
		) l ON i.id = l.image_id ` + whereClause + `
		ORDER BY i.id DESC, l.name ASC
		LIMIT ? OFFSET ?
	`

	// Add limit and offset to args
	queryArgs := append(args, params.Limit, offset)
	rows, err := app.db.Query(selectQuery, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var images []ImageMetadata
	imageMap := make(map[int]*ImageMetadata)
	var orderedIDs []int

	for rows.Next() {
		var img ImageMetadata
		var loraName, loraWeight sql.NullString
		
		err := rows.Scan(&img.ID, &img.Filename, &img.Width, &img.Height,
			&img.Model, &img.Prompt, &img.NegPrompt, &img.Steps, &img.CFGScale,
			&img.Sampler, &img.Scheduler, &img.Seed, &img.ThumbnailPath, &img.IsNSFW,
			&loraName, &loraWeight)
		if err != nil {
			log.Printf("Error scanning row: %v", err)
			continue
		}

		// Check if we already have this image
		if existingImg, exists := imageMap[img.ID]; exists {
			// Image already exists, just add LoRA data if present
			if loraName.Valid && loraWeight.Valid {
				weight, _ := strconv.ParseFloat(loraWeight.String, 64)
				existingImg.LoRAs = append(existingImg.LoRAs, LoraData{
					Name:   loraName.String,
					Weight: weight,
				})
			}
		} else {
			// New image, set URL and add to map
			img.SetImageURL()
			
			// Add LoRA data if present
			if loraName.Valid && loraWeight.Valid {
				weight, _ := strconv.ParseFloat(loraWeight.String, 64)
				img.LoRAs = append(img.LoRAs, LoraData{
					Name:   loraName.String,
					Weight: weight,
				})
			}
			
			imageMap[img.ID] = &img
			orderedIDs = append(orderedIDs, img.ID)
		}
	}

	// Convert map back to ordered slice
	for _, id := range orderedIDs {
		if img, exists := imageMap[id]; exists {
			images = append(images, *img)
		}
	}

	return images, total, nil
}

func (app *App) handleAPIImages(w http.ResponseWriter, r *http.Request) {
	params := parseImageSearchParams(r)
	images, total, err := app.queryImages(params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	app.renderImageGrid(w, images, params.Page, total, params.Limit, "")
}

func (app *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	params := parseImageSearchParams(r)
	images, total, err := app.queryImages(params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	app.renderImageGrid(w, images, params.Page, total, params.Limit, params.PromptQuery)
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
		TotalCount:     total,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
