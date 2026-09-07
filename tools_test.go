package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func writeTool(t *testing.T, slug, body string) {
	t.Helper()
	dir := filepath.Join(toolsRoot, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if body == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", slug, err)
	}
}

func TestListToolsReadsTitlesAndSkipsIncompleteFolders(t *testing.T) {
	t.Chdir(t.TempDir())

	writeTool(t, "h3-prompt", "<html><head><title>H3 Prompt Builder</title></head></html>")
	writeTool(t, "untitled", "<html><body>no title here</body></html>")
	writeTool(t, "no-entry-point", "")
	writeTool(t, ".hidden", "<title>Hidden</title>")

	tools := listTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d: %+v", len(tools), tools)
	}

	// Sorted by display name: "H3 Prompt Builder" before "untitled".
	if tools[0].Name != "H3 Prompt Builder" || tools[0].Path != "/tools/h3-prompt/" {
		t.Errorf("first tool = %+v", tools[0])
	}
	// No <title> falls back to the folder name.
	if tools[1].Name != "untitled" {
		t.Errorf("expected the slug as fallback name, got %q", tools[1].Name)
	}
}

func TestListToolsWithoutDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	if tools := listTools(); tools != nil {
		t.Errorf("expected no tools without a %s directory, got %+v", toolsRoot, tools)
	}
}

func TestNoListingDirHidesFoldersWithoutIndex(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "served"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "served", "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := noListingDir{http.Dir(root)}

	if _, err := dir.Open("/bare"); !os.IsNotExist(err) {
		t.Errorf("a folder without index.html should read as missing, got %v", err)
	}
	file, err := dir.Open("/served")
	if err != nil {
		t.Fatalf("a folder with an index.html should open: %v", err)
	}
	file.Close()
}
