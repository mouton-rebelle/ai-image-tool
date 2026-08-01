package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func openImportTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
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

func feedImage(id int) CivitaiImage {
	img := CivitaiImage{
		ID:        id,
		URL:       fmt.Sprintf("https://example.test/%d.jpeg", id),
		CreatedAt: "2026-07-31T18:22:25.453Z",
	}
	return img
}

func writeImportTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create test directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("image"), 0o644); err != nil {
		t.Fatalf("create test file: %v", err)
	}
}

// A post published after images that were generated more recently lands in the
// middle of the "Newest" feed, below images we already have. The scan must keep
// going past those instead of treating them as an end-of-new-content marker.
func TestDownloadMissingFeedImagesContinuesPastAlreadyImportedImages(t *testing.T) {
	t.Chdir(t.TempDir())
	app := &App{db: openImportTestDB(t)}

	writeImportTestFile(t, filepath.Join("images", "2.jpeg"))
	writeImportTestFile(t, filepath.Join("images_nsfw", "3.jpeg"))

	images := []CivitaiImage{
		feedImage(1), // new, above the already-imported ones
		feedImage(2), // already on disk (SFW)
		feedImage(3), // already on disk (NSFW)
		feedImage(4), // new, below the already-imported ones
		feedImage(5), // new, below the already-imported ones
	}

	var attempted []int
	mapping := make(TimestampMapping)
	count, err := app.downloadMissingFeedImages(images, mapping, func(img CivitaiImage) (bool, error) {
		attempted = append(attempted, img.ID)
		return true, nil
	})
	if err != nil {
		t.Fatalf("downloadMissingFeedImages: %v", err)
	}

	want := []int{1, 4, 5}
	if len(attempted) != len(want) {
		t.Fatalf("downloaded %v, want %v", attempted, want)
	}
	for i, id := range want {
		if attempted[i] != id {
			t.Fatalf("downloaded %v, want %v", attempted, want)
		}
	}
	if count != len(want) {
		t.Errorf("count = %d, want %d", count, len(want))
	}

	// Timestamps are captured for every image on the page, downloaded or not.
	for _, img := range images {
		if _, ok := mapping[fmt.Sprintf("%d.jpeg", img.ID)]; !ok {
			t.Errorf("missing timestamp mapping for image %d", img.ID)
		}
	}
}

func TestDownloadMissingFeedImagesSkipsBlacklistedWithoutStopping(t *testing.T) {
	t.Chdir(t.TempDir())
	app := &App{db: openImportTestDB(t)}

	if _, err := app.db.Exec(
		"INSERT INTO deleted_civitai_images (civitai_image_id, filename) VALUES (2, '2.jpeg')",
	); err != nil {
		t.Fatalf("insert blacklist row: %v", err)
	}

	images := []CivitaiImage{feedImage(1), feedImage(2), feedImage(3)}

	var attempted []int
	count, err := app.downloadMissingFeedImages(images, make(TimestampMapping), func(img CivitaiImage) (bool, error) {
		attempted = append(attempted, img.ID)
		return true, nil
	})
	if err != nil {
		t.Fatalf("downloadMissingFeedImages: %v", err)
	}

	if len(attempted) != 2 || attempted[0] != 1 || attempted[1] != 3 {
		t.Fatalf("downloaded %v, want [1 3]", attempted)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

// A failed download must not abort the rest of the page, and must not be counted.
func TestDownloadMissingFeedImagesContinuesAfterDownloadError(t *testing.T) {
	t.Chdir(t.TempDir())
	app := &App{db: openImportTestDB(t)}

	images := []CivitaiImage{feedImage(1), feedImage(2), feedImage(3)}

	count, err := app.downloadMissingFeedImages(images, make(TimestampMapping), func(img CivitaiImage) (bool, error) {
		if img.ID == 2 {
			return false, fmt.Errorf("boom")
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("downloadMissingFeedImages: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}
