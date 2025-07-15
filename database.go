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

	_, err = app.db.Exec(createModelsTable)
	if err != nil {
		return err
	}

	_, err = app.db.Exec(createImagesTable)
	return err
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

	return err
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