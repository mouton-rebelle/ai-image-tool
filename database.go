package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
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
	return err
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
	INSERT INTO images (id, filename, width, height, model_id, model_hash, prompt, neg_prompt, steps, cfg_scale, sampler, scheduler, seed, thumbnail_path, is_nsfw)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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