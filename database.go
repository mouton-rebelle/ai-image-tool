package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	_ "github.com/mattn/go-sqlite3"
)

func (app *App) initDB() error {
	var err error
	app.db, err = sql.Open("sqlite3", "./images.db?_foreign_keys=on&_journal_mode=WAL&_synchronous=NORMAL&_cache_size=1000&_busy_timeout=5000")
	if err != nil {
		return err
	}

	// Ensure UTF-8 encoding
	app.db.Exec("PRAGMA encoding = 'UTF-8'")
	app.db.Exec("PRAGMA journal_mode = WAL")

	// Create models table
	createModelsTable := `
	CREATE TABLE IF NOT EXISTS models (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		hash TEXT UNIQUE NOT NULL,
		name TEXT,
		version_name TEXT,
		type TEXT,
		nsfw BOOLEAN DEFAULT FALSE,
		description TEXT,
		base_model TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	
	CREATE INDEX IF NOT EXISTS idx_model_hash ON models(hash);
	`

	// Create images table
	createImagesTable := `
	CREATE TABLE IF NOT EXISTS images (
		id INTEGER PRIMARY KEY,
		filename TEXT UNIQUE NOT NULL,
		width INTEGER,
		height INTEGER,
		model_id INTEGER,
		model_hash TEXT,
		prompt TEXT,
		neg_prompt TEXT,
		steps INTEGER,
		cfg_scale REAL,
		sampler TEXT,
		scheduler TEXT,
		seed INTEGER,
		thumbnail_path TEXT,
		is_nsfw BOOLEAN DEFAULT FALSE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (model_id) REFERENCES models(id)
	);
	
	CREATE INDEX IF NOT EXISTS idx_model_id ON images(model_id);
	CREATE INDEX IF NOT EXISTS idx_model_hash ON images(model_hash);
	CREATE INDEX IF NOT EXISTS idx_prompt ON images(prompt);
	CREATE INDEX IF NOT EXISTS idx_nsfw ON images(is_nsfw);
	CREATE INDEX IF NOT EXISTS idx_created_at ON images(created_at DESC);
	`

	// Create loras table
	createLorasTable := `
	CREATE TABLE IF NOT EXISTS loras (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		image_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		weight REAL NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (image_id) REFERENCES images(id) ON DELETE CASCADE
	);
	
	CREATE INDEX IF NOT EXISTS idx_lora_image_id ON loras(image_id);
	CREATE INDEX IF NOT EXISTS idx_lora_name ON loras(name);
	`

	_, err = app.db.Exec(createModelsTable)
	if err != nil {
		return err
	}

	_, err = app.db.Exec(createImagesTable)
	if err != nil {
		return err
	}

	_, err = app.db.Exec(createLorasTable)
	if err != nil {
		return err
	}

	// Migration: Add display_timestamp column if it doesn't exist
	log.Printf("Attempting to add display_timestamp column...")
	_, err = app.db.Exec("ALTER TABLE images ADD COLUMN display_timestamp DATETIME")
	if err != nil {
		// Column might already exist, check if it's a different error
		if !strings.Contains(err.Error(), "duplicate column name") && !strings.Contains(err.Error(), "already exists") {
			log.Printf("Warning: Failed to add display_timestamp column: %v", err)
			// Don't proceed with migration if column creation failed
			return nil
		} else {
			log.Printf("Column already exists, continuing...")
		}
	} else {
		log.Printf("Successfully added display_timestamp column")
	}

	// Create index for display_timestamp if it doesn't exist
	_, err = app.db.Exec("CREATE INDEX IF NOT EXISTS idx_display_timestamp ON images(display_timestamp DESC)")
	if err != nil {
		log.Printf("Warning: Failed to create display_timestamp index: %v", err)
	}

	// Migration: Populate display_timestamp for existing records that don't have it
	// First check if the column exists before trying to migrate
	if app.columnExists("images", "display_timestamp") {
		err = app.migrateDisplayTimestamps()
		if err != nil {
			log.Printf("Warning: Failed to migrate display timestamps: %v", err)
		}
	}

	return nil
}

func (app *App) columnExists(tableName, columnName string) bool {
	query := fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = '%s'", tableName, columnName)
	var count int
	err := app.db.QueryRow(query).Scan(&count)
	if err != nil {
		log.Printf("Error checking if column exists: %v", err)
		return false
	}
	log.Printf("Column check: %s.%s exists = %t", tableName, columnName, count > 0)
	return count > 0
}

func (app *App) clearImagesTables() error {
	// Clear loras table first (foreign key constraint)
	_, err := app.db.Exec("DELETE FROM loras")
	if err != nil {
		return fmt.Errorf("failed to clear loras table: %v", err)
	}

	// Clear images table
	_, err = app.db.Exec("DELETE FROM images")
	if err != nil {
		return fmt.Errorf("failed to clear images table: %v", err)
	}

	// Reset auto-increment sequences
	_, err = app.db.Exec("DELETE FROM sqlite_sequence WHERE name IN ('loras')")
	if err != nil {
		return fmt.Errorf("failed to reset sequences: %v", err)
	}

	// Get counts for confirmation
	var modelCount, imageCount, loraCount int
	app.db.QueryRow("SELECT COUNT(*) FROM models").Scan(&modelCount)
	app.db.QueryRow("SELECT COUNT(*) FROM images").Scan(&imageCount)
	app.db.QueryRow("SELECT COUNT(*) FROM loras").Scan(&loraCount)

	fmt.Printf("Table counts after clearing:\n")
	fmt.Printf("  Models: %d (preserved)\n", modelCount)
	fmt.Printf("  Images: %d (cleared)\n", imageCount)
	fmt.Printf("  LoRAs: %d (cleared)\n", loraCount)

	return nil
}

func (app *App) migrateDisplayTimestamps() error {
	log.Printf("Starting display timestamp migration...")
	
	// Double-check that the column exists before querying it
	if !app.columnExists("images", "display_timestamp") {
		log.Printf("display_timestamp column does not exist, skipping migration")
		return nil
	}
	
	// Find all images that don't have display_timestamp set
	query := "SELECT id, filename FROM images WHERE display_timestamp IS NULL"
	rows, err := app.db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query images for migration: %v", err)
	}
	defer rows.Close()

	var imagesToUpdate []struct {
		ID       int
		Filename string
	}

	for rows.Next() {
		var imageData struct {
			ID       int
			Filename string
		}
		if err := rows.Scan(&imageData.ID, &imageData.Filename); err != nil {
			log.Printf("Error scanning image data: %v", err)
			continue
		}
		imagesToUpdate = append(imagesToUpdate, imageData)
	}

	if len(imagesToUpdate) == 0 {
		return nil // No migration needed
	}

	log.Printf("Migrating display timestamps for %d existing images...", len(imagesToUpdate))

	// Update each image with computed display timestamp
	updateQuery := "UPDATE images SET display_timestamp = ? WHERE id = ?"
	for _, imageData := range imagesToUpdate {
		// Try to find the actual image file
		var imagePath string
		if _, err := os.Stat(filepath.Join("images", imageData.Filename)); err == nil {
			imagePath = filepath.Join("images", imageData.Filename)
		} else if _, err := os.Stat(filepath.Join("images_nsfw", imageData.Filename)); err == nil {
			imagePath = filepath.Join("images_nsfw", imageData.Filename)
		} else {
			// File not found, use fallback logic based on filename only
			imagePath = ""
		}

		// Calculate display timestamp (inline implementation)
		var displayTimestamp *time.Time
		
		// Method 1: For Civitai images (numeric filenames), use ID-based timestamp
		idStr := strings.Split(imageData.Filename, ".")[0]
		if civitaiID, err := strconv.Atoi(idStr); err == nil {
			// Civitai IDs are roughly chronological
			baseDate := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			timestampOffset := time.Duration(civitaiID/100) * time.Second
			computed := baseDate.Add(timestampOffset)
			displayTimestamp = &computed
		} else if imagePath != "" {
			// Method 2: For local images, use file modification time
			if fileInfo, err := os.Stat(imagePath); err == nil {
				modTime := fileInfo.ModTime()
				displayTimestamp = &modTime
			} else {
				// Method 3: Fallback to current time
				now := time.Now()
				displayTimestamp = &now
			}
		} else {
			// Method 3: Fallback to current time if no file found
			now := time.Now()
			displayTimestamp = &now
		}

		_, err := app.db.Exec(updateQuery, displayTimestamp, imageData.ID)
		if err != nil {
			log.Printf("Error updating display timestamp for image %d: %v", imageData.ID, err)
		}
	}

	log.Printf("Display timestamp migration completed for %d images", len(imagesToUpdate))
	return nil
}

func (app *App) getOrCreateModel(hash string) (*Model, error) {
	// Clean the hash
	cleanHash := strings.TrimSpace(hash)
	if cleanHash == "" {
		return nil, fmt.Errorf("empty hash")
	}

	// First, check if model already exists in database
	var model Model
	err := app.db.QueryRow("SELECT id, hash, name, version_name, type, nsfw, description, base_model, created_at FROM models WHERE hash = ?", cleanHash).Scan(
		&model.ID, &model.Hash, &model.Name, &model.VersionName, &model.Type, &model.NSFW, &model.Description, &model.BaseModel, &model.CreatedAt)

	if err == nil {
		// Model found in database
		return &model, nil
	}

	if err != sql.ErrNoRows {
		// Database error
		return nil, fmt.Errorf("database error: %v", err)
	}

	// Model not found, fetch from Civitai API
	log.Printf("Fetching model from Civitai API for hash: %s", cleanHash)
	apiModel, err := app.fetchModelFromCivitai(cleanHash)
	if err != nil {
		log.Printf("Failed to fetch model from API: %v", err)
		// Create a placeholder model with just the hash
		apiModel = &Model{
			Hash: cleanHash,
			Name: fmt.Sprintf("Unknown Model (%s)", cleanHash[:8]),
			Type: "Unknown",
		}
	}

	// Insert into database
	result, err := app.db.Exec("INSERT INTO models (hash, name, version_name, type, nsfw, description, base_model) VALUES (?, ?, ?, ?, ?, ?, ?)",
		apiModel.Hash, apiModel.Name, apiModel.VersionName, apiModel.Type, apiModel.NSFW, apiModel.Description, apiModel.BaseModel)
	if err != nil {
		return nil, fmt.Errorf("failed to insert model: %v", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get model ID: %v", err)
	}

	apiModel.ID = int(id)
	log.Printf("Created new model: %s (ID: %d)", apiModel.Name, apiModel.ID)

	return apiModel, nil
}

func (app *App) insertImageMetadata(metadata *ImageMetadata) error {
	// Check if ID already exists and increment if needed
	originalID := metadata.ID
	for {
		var exists int
		err := app.db.QueryRow("SELECT COUNT(*) FROM images WHERE id = ?", metadata.ID).Scan(&exists)
		if err != nil {
			return err
		}

		if exists == 0 {
			break // ID is available
		}

		// ID exists, increment and try again
		metadata.ID++
		if metadata.ID != originalID && metadata.ID%1000 == 0 {
			log.Printf("ID collision for %s, trying ID: %d", metadata.Filename, metadata.ID)
		}
	}

	query := `
	INSERT INTO images (id, filename, width, height, model_id, model_hash, prompt, neg_prompt, steps, cfg_scale, sampler, scheduler, seed, thumbnail_path, is_nsfw, display_timestamp)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := app.db.Exec(query,
		metadata.ID,
		metadata.Filename,
		metadata.Width,
		metadata.Height,
		metadata.ModelID,
		metadata.ModelHash,
		metadata.Prompt,
		metadata.NegPrompt,
		metadata.Steps,
		metadata.CFGScale,
		metadata.Sampler,
		metadata.Scheduler,
		metadata.Seed,
		metadata.ThumbnailPath,
		metadata.IsNSFW,
		metadata.DisplayTimestamp,
	)

	if err != nil {
		return err
	}

	// Append prompt to appropriate file
	excludedWords := loadExcludedWords()
	if err := appendPromptToFile(metadata.Prompt, metadata.NegPrompt, metadata.IsNSFW, excludedWords); err != nil {
		// Log error but don't fail - prompt file writing is not critical
		fmt.Printf("Warning: Failed to append prompt to file: %v\n", err)
	}

	return nil
}

type LoraData struct {
	Name   string
	Weight float64
}

func (app *App) insertLoraData(imageID int, loras []LoraData) error {
	if len(loras) == 0 {
		return nil
	}

	// Prepare statement for bulk insert
	stmt, err := app.db.Prepare("INSERT INTO loras (image_id, name, weight) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	// Insert each LoRA
	for _, lora := range loras {
		_, err := stmt.Exec(imageID, lora.Name, lora.Weight)
		if err != nil {
			return err
		}
	}

	return nil
}


func (app *App) getModelStats() ([]ModelStat, error) {
	query := `
		SELECT 
			m.id,
			CASE 
				WHEN m.name IS NOT NULL AND m.version_name IS NOT NULL 
				THEN m.name 
				ELSE COALESCE(m.name, 'Unknown Model')
			END as model_name,
			COALESCE(m.version_name, '') as version_name,
			COUNT(i.id) as image_count
		FROM models m
		LEFT JOIN images i ON m.id = i.model_id
		GROUP BY m.id, m.name, m.version_name
		HAVING image_count > 0
		ORDER BY image_count DESC, model_name ASC
	`

	rows, err := app.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []ModelStat
	for rows.Next() {
		var model ModelStat
		err := rows.Scan(&model.ID, &model.Name, &model.VersionName, &model.ImageCount)
		if err != nil {
			return nil, err
		}
		models = append(models, model)
	}

	return models, nil
}