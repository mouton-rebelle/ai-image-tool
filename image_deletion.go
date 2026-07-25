package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

var errImageNotFound = errors.New("image not found")

type DeleteImageResponse struct {
	Success     bool   `json:"success"`
	DeletedID   int    `json:"deleted_id,omitempty"`
	Blacklisted bool   `json:"blacklisted,omitempty"`
	Error       string `json:"error,omitempty"`
}

type stagedDeletionFile struct {
	originalPath string
	stagedPath   string
}

func civitaiImageIDFromFilename(filename string) (int, bool) {
	if filename == "" || filepath.Base(filename) != filename {
		return 0, false
	}

	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	id, err := strconv.Atoi(stem)
	return id, err == nil && id > 0
}

func (app *App) isCivitaiImageBlacklisted(imageID int) (bool, error) {
	var exists int
	err := app.db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM deleted_civitai_images WHERE civitai_image_id = ?)",
		imageID,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}

func stageFilesForDeletion(paths []string) ([]stagedDeletionFile, error) {
	staged := make([]stagedDeletionFile, 0, len(paths))

	for _, path := range paths {
		if _, err := os.Lstat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			restoreStagedDeletionFiles(staged)
			return nil, fmt.Errorf("inspect %s: %w", path, err)
		}

		stagedPath := fmt.Sprintf("%s.deleting-%d", path, time.Now().UnixNano())
		if err := os.Rename(path, stagedPath); err != nil {
			restoreStagedDeletionFiles(staged)
			return nil, fmt.Errorf("stage %s for deletion: %w", path, err)
		}

		staged = append(staged, stagedDeletionFile{
			originalPath: path,
			stagedPath:   stagedPath,
		})
	}

	return staged, nil
}

func restoreStagedDeletionFiles(files []stagedDeletionFile) {
	for i := len(files) - 1; i >= 0; i-- {
		if err := os.Rename(files[i].stagedPath, files[i].originalPath); err != nil {
			log.Printf("Failed to restore staged image file %s: %v", files[i].originalPath, err)
		}
	}
}

func removeStagedDeletionFiles(files []stagedDeletionFile) {
	for _, file := range files {
		if err := os.Remove(file.stagedPath); err != nil && !os.IsNotExist(err) {
			log.Printf("Failed to remove staged image file %s: %v", file.stagedPath, err)
		}
	}
}

func (app *App) deleteImage(imageID int) (bool, error) {
	var filename string
	err := app.db.QueryRow("SELECT filename FROM images WHERE id = ?", imageID).Scan(&filename)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errImageNotFound
	}
	if err != nil {
		return false, fmt.Errorf("find image: %w", err)
	}
	if filename == "" || filepath.Base(filename) != filename {
		return false, errors.New("image has an invalid filename")
	}

	paths := []string{
		filepath.Join("images", filename),
		filepath.Join("images_nsfw", filename),
		filepath.Join("thumbnails", filename),
	}
	stagedFiles, err := stageFilesForDeletion(paths)
	if err != nil {
		return false, err
	}

	tx, err := app.db.Begin()
	if err != nil {
		restoreStagedDeletionFiles(stagedFiles)
		return false, fmt.Errorf("begin image deletion: %w", err)
	}

	rollback := func() {
		_ = tx.Rollback()
		restoreStagedDeletionFiles(stagedFiles)
	}

	civitaiID, blacklisted := civitaiImageIDFromFilename(filename)
	if blacklisted {
		_, err = tx.Exec(`
			INSERT INTO deleted_civitai_images (civitai_image_id, filename)
			VALUES (?, ?)
			ON CONFLICT(civitai_image_id) DO UPDATE SET
				filename = excluded.filename,
				deleted_at = CURRENT_TIMESTAMP
		`, civitaiID, filename)
		if err != nil {
			rollback()
			return false, fmt.Errorf("blacklist Civitai image: %w", err)
		}
	}

	result, err := tx.Exec("DELETE FROM images WHERE id = ?", imageID)
	if err != nil {
		rollback()
		return false, fmt.Errorf("delete image record: %w", err)
	}
	deletedRows, err := result.RowsAffected()
	if err != nil || deletedRows != 1 {
		rollback()
		if err != nil {
			return false, fmt.Errorf("verify image deletion: %w", err)
		}
		return false, errImageNotFound
	}

	if err := tx.Commit(); err != nil {
		restoreStagedDeletionFiles(stagedFiles)
		return false, fmt.Errorf("commit image deletion: %w", err)
	}

	removeStagedDeletionFiles(stagedFiles)
	return blacklisted, nil
}

func (app *App) handleDeleteImage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	imageID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil || imageID <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(DeleteImageResponse{
			Success: false,
			Error:   "Invalid image ID",
		})
		return
	}

	blacklisted, err := app.deleteImage(imageID)
	if errors.Is(err, errImageNotFound) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(DeleteImageResponse{
			Success: false,
			Error:   "Image not found",
		})
		return
	}
	if err != nil {
		log.Printf("Failed to delete image %d: %v", imageID, err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(DeleteImageResponse{
			Success: false,
			Error:   "Failed to delete image",
		})
		return
	}

	_ = json.NewEncoder(w).Encode(DeleteImageResponse{
		Success:     true,
		DeletedID:   imageID,
		Blacklisted: blacklisted,
	})
}
