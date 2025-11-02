package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestPNGWithWrongExtension tests that PNG files with wrong extensions (like .jpeg) are still parsed correctly
func TestPNGWithWrongExtension(t *testing.T) {
	// This is a regression test for the bug where PNG files with .jpeg extension
	// were not having their metadata extracted because the code only checked file extension
	// instead of actual file type via magic bytes

	// Create a test database
	app := &App{}
	db := setupTestDB(t, app)
	defer db.Close()
	app.db = db

	// Test with the actual problematic file
	imagePath := "images/107670305.jpeg"

	// Check if the test file exists
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		t.Skipf("Test file %s does not exist, skipping test", imagePath)
		return
	}

	// Extract metadata
	metadata, err := app.extractImageMetadata(imagePath, false)
	if err != nil {
		t.Fatalf("Failed to extract metadata from %s: %v", imagePath, err)
	}

	// Verify that metadata was extracted (this file has a seed value)
	if metadata.Seed == 0 {
		t.Errorf("Expected non-zero seed for PNG file with .jpeg extension, got 0")
	}

	// Verify other fields were extracted
	if metadata.Prompt == "" {
		t.Errorf("Expected non-empty prompt for PNG file with .jpeg extension, got empty string")
	}

	if metadata.Model == "" {
		t.Errorf("Expected non-empty model for PNG file with .jpeg extension, got empty string")
	}

	if metadata.Steps == 0 {
		t.Errorf("Expected non-zero steps for PNG file with .jpeg extension, got 0")
	}

	if metadata.CFGScale == 0 {
		t.Errorf("Expected non-zero CFG scale for PNG file with .jpeg extension, got 0")
	}

	// Log what we found for debugging
	t.Logf("Successfully extracted metadata:")
	t.Logf("  Seed: %d", metadata.Seed)
	t.Logf("  Model: %s", metadata.Model)
	t.Logf("  Steps: %d", metadata.Steps)
	t.Logf("  CFG Scale: %.1f", metadata.CFGScale)
	t.Logf("  Sampler: %s", metadata.Sampler)
	t.Logf("  Prompt (first 100 chars): %s", truncateString(metadata.Prompt, 100))
}

// TestFileTypeDetection tests that the file type detection works correctly
func TestFileTypeDetection(t *testing.T) {
	tests := []struct {
		filename     string
		expectedType string // "png" or "jpeg"
	}{
		{"images/107670305.jpeg", "png"}, // PNG with wrong extension
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			// Check if file exists
			if _, err := os.Stat(tt.filename); os.IsNotExist(err) {
				t.Skipf("Test file %s does not exist, skipping", tt.filename)
				return
			}

			file, err := os.Open(tt.filename)
			if err != nil {
				t.Fatalf("Failed to open file: %v", err)
			}
			defer file.Close()

			signature := make([]byte, 8)
			file.Read(signature)

			pngSignature := []byte{137, 80, 78, 71, 13, 10, 26, 10}
			jpegSignature := []byte{0xFF, 0xD8, 0xFF}

			isPNG := len(signature) >= 8 && string(signature[:8]) == string(pngSignature)
			isJPEG := len(signature) >= 3 && string(signature[:3]) == string(jpegSignature)

			var detectedType string
			if isPNG {
				detectedType = "png"
			} else if isJPEG {
				detectedType = "jpeg"
			} else {
				detectedType = "unknown"
			}

			if detectedType != tt.expectedType {
				t.Errorf("Expected file type %s for %s, but detected %s (ext: %s)",
					tt.expectedType, tt.filename, detectedType, filepath.Ext(tt.filename))
			}
		})
	}
}

// TestRealPNGFile tests that actual PNG files with .png extension still work
func TestRealPNGFile(t *testing.T) {
	// Find a real PNG file in the images directory
	pngFiles, _ := filepath.Glob("images/*.png")
	if len(pngFiles) == 0 {
		pngFiles, _ = filepath.Glob("images/*.PNG")
	}

	if len(pngFiles) == 0 {
		t.Skip("No PNG files found in images directory, skipping test")
		return
	}

	app := &App{db: nil}

	// Test the first PNG file found
	imagePath := pngFiles[0]
	metadata, err := app.extractImageMetadata(imagePath, false)
	if err != nil {
		t.Fatalf("Failed to extract metadata from %s: %v", imagePath, err)
	}

	t.Logf("Tested PNG file: %s", imagePath)
	t.Logf("  Seed: %d", metadata.Seed)
	t.Logf("  Model: %s", metadata.Model)
	// Note: Some PNG files might not have metadata, so we don't assert here
}

// Helper function to truncate strings for logging
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// Mock database setup for testing
func setupTestDB(t *testing.T, app *App) *sql.DB {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	// Temporarily set the db so we can call initDB
	app.db = db
	if err := app.initDB(); err != nil {
		t.Fatalf("Failed to initialize test database schema: %v", err)
	}

	return db
}
