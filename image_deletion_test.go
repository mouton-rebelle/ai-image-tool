package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
	_ "github.com/mattn/go-sqlite3"
)

func openImageDeletionTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
		PRAGMA foreign_keys = ON;
		CREATE TABLE images (
			id INTEGER PRIMARY KEY,
			filename TEXT UNIQUE NOT NULL
		);
		CREATE TABLE loras (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			image_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			weight REAL NOT NULL,
			FOREIGN KEY (image_id) REFERENCES images(id) ON DELETE CASCADE
		);
		CREATE TABLE deleted_civitai_images (
			civitai_image_id INTEGER PRIMARY KEY,
			filename TEXT NOT NULL,
			deleted_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`); err != nil {
		t.Fatalf("create test schema: %v", err)
	}

	return db
}

func writeDeletionTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create test directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("image"), 0o644); err != nil {
		t.Fatalf("create test file: %v", err)
	}
}

func TestHandleDeleteImageRemovesFilesAndBlacklistsCivitaiImage(t *testing.T) {
	t.Chdir(t.TempDir())
	db := openImageDeletionTestDB(t)
	app := &App{db: db}

	if _, err := db.Exec("INSERT INTO images (id, filename) VALUES (123, '123.jpg')"); err != nil {
		t.Fatalf("insert image: %v", err)
	}
	if _, err := db.Exec("INSERT INTO loras (image_id, name, weight) VALUES (123, 'detail', 0.8)"); err != nil {
		t.Fatalf("insert LoRA: %v", err)
	}
	writeDeletionTestFile(t, filepath.Join("images", "123.jpg"))
	writeDeletionTestFile(t, filepath.Join("thumbnails", "123.jpg"))

	request := httptest.NewRequest(http.MethodDelete, "/api/images/123", nil)
	request = mux.SetURLVars(request, map[string]string{"id": "123"})
	recorder := httptest.NewRecorder()

	app.handleDeleteImage(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, path := range []string{
		filepath.Join("images", "123.jpg"),
		filepath.Join("thumbnails", "123.jpg"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be deleted, stat error: %v", path, err)
		}
	}

	var imageCount, loraCount, blacklistCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM images WHERE id = 123").Scan(&imageCount); err != nil {
		t.Fatalf("count images: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM loras WHERE image_id = 123").Scan(&loraCount); err != nil {
		t.Fatalf("count LoRAs: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM deleted_civitai_images WHERE civitai_image_id = 123").Scan(&blacklistCount); err != nil {
		t.Fatalf("count blacklist entries: %v", err)
	}
	if imageCount != 0 || loraCount != 0 || blacklistCount != 1 {
		t.Fatalf(
			"unexpected database state: images=%d loras=%d blacklist=%d",
			imageCount,
			loraCount,
			blacklistCount,
		)
	}
}

func TestDeleteImageDoesNotBlacklistLocalFilename(t *testing.T) {
	t.Chdir(t.TempDir())
	db := openImageDeletionTestDB(t)
	app := &App{db: db}

	if _, err := db.Exec("INSERT INTO images (id, filename) VALUES (456, 'local-image.png')"); err != nil {
		t.Fatalf("insert image: %v", err)
	}
	writeDeletionTestFile(t, filepath.Join("images_nsfw", "local-image.png"))

	blacklisted, err := app.deleteImage(456)
	if err != nil {
		t.Fatalf("delete local image: %v", err)
	}
	if blacklisted {
		t.Fatal("local image should not be added to the Civitai blacklist")
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM deleted_civitai_images").Scan(&count); err != nil {
		t.Fatalf("count blacklist entries: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected an empty blacklist, got %d entries", count)
	}
}

func TestDeleteImageRestoresFilesWhenDatabaseDeletionFails(t *testing.T) {
	t.Chdir(t.TempDir())
	db := openImageDeletionTestDB(t)
	app := &App{db: db}

	if _, err := db.Exec(`
		INSERT INTO images (id, filename) VALUES (789, '789.png');
		CREATE TRIGGER reject_image_deletion
		BEFORE DELETE ON images
		BEGIN
			SELECT RAISE(ABORT, 'deletion rejected');
		END;
	`); err != nil {
		t.Fatalf("prepare failing deletion: %v", err)
	}
	imagePath := filepath.Join("images", "789.png")
	writeDeletionTestFile(t, imagePath)

	if _, err := app.deleteImage(789); err == nil {
		t.Fatal("expected image deletion to fail")
	}
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatalf("expected original image file to be restored: %v", err)
	}

	var imageCount, blacklistCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM images WHERE id = 789").Scan(&imageCount); err != nil {
		t.Fatalf("count images: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM deleted_civitai_images WHERE civitai_image_id = 789").Scan(&blacklistCount); err != nil {
		t.Fatalf("count blacklist entries: %v", err)
	}
	if imageCount != 1 || blacklistCount != 0 {
		t.Fatalf("expected rollback, got images=%d blacklist=%d", imageCount, blacklistCount)
	}
}

func TestDownloadImageSkipsBlacklistedCivitaiImage(t *testing.T) {
	t.Chdir(t.TempDir())
	db := openImageDeletionTestDB(t)
	app := &App{db: db}

	if _, err := db.Exec(`
		INSERT INTO deleted_civitai_images (civitai_image_id, filename)
		VALUES (321, '321.jpg')
	`); err != nil {
		t.Fatalf("insert blacklist entry: %v", err)
	}

	downloaded, err := app.downloadImage(CivitaiImage{
		ID:  321,
		URL: "://invalid-url-that-must-not-be-requested",
	})
	if err != nil {
		t.Fatalf("blacklisted download should be skipped without error: %v", err)
	}
	if downloaded {
		t.Fatal("blacklisted image should not be downloaded")
	}
}
