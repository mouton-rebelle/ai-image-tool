package main

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestSanitizePromptForStorage(t *testing.T) {
	raw := string([]byte{0, 0, 0, 0, ',', ' ', 0xff}) +
		"A\u00a0prompt \uFFFD"

	if got, want := sanitizePromptForStorage(raw), "A prompt"; got != want {
		t.Fatalf("sanitize prompt: got %q, want %q", got, want)
	}
}

func TestSanitizeStoredImagePrompts(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
		CREATE TABLE images (
			id INTEGER PRIMARY KEY,
			prompt TEXT,
			neg_prompt TEXT
		)
	`); err != nil {
		t.Fatalf("create images table: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO images (id, prompt, neg_prompt) VALUES (?, ?, ?)",
		1,
		"\x00\x00\x00\x00, A valid prompt",
		"bad \uFFFD anatomy",
	); err != nil {
		t.Fatalf("insert image: %v", err)
	}

	app := &App{db: db}
	if err := app.sanitizeStoredImagePrompts(); err != nil {
		t.Fatalf("sanitize stored prompts: %v", err)
	}

	var prompt, negPrompt string
	if err := db.QueryRow(
		"SELECT prompt, neg_prompt FROM images WHERE id = 1",
	).Scan(&prompt, &negPrompt); err != nil {
		t.Fatalf("read sanitized prompts: %v", err)
	}
	if prompt != "A valid prompt" {
		t.Fatalf("unexpected prompt %q", prompt)
	}
	if negPrompt != "bad  anatomy" {
		t.Fatalf("unexpected negative prompt %q", negPrompt)
	}
}
