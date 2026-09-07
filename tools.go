package main

import (
	"html"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// toolsRoot holds one self-contained static tool per subdirectory. Publishing a
// tool is dropping a folder in: the index is read from the filesystem, never
// from a list that would have to be kept in sync by hand.
const toolsRoot = "tools"

// Tool is one entry of the /tools index.
type Tool struct {
	Slug string
	Name string
	Path string
}

var toolTitle = regexp.MustCompile(`(?is)<title>(.*?)</title>`)

// toolName prefers the tool's own <title>, so it is named the same way in the
// browser tab and in the index. The folder name is the fallback.
func toolName(dir, slug string) string {
	data, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		return slug
	}
	match := toolTitle.FindSubmatch(data)
	if match == nil {
		return slug
	}
	if title := strings.TrimSpace(html.UnescapeString(string(match[1]))); title != "" {
		return title
	}
	return slug
}

// listTools returns the tools worth linking to: a subdirectory only counts once
// it has an index.html to land on.
func listTools() []Tool {
	entries, err := os.ReadDir(toolsRoot)
	if err != nil {
		return nil
	}

	var tools []Tool
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(toolsRoot, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
			continue
		}
		tools = append(tools, Tool{
			Slug: entry.Name(),
			Name: toolName(dir, entry.Name()),
			Path: "/" + toolsRoot + "/" + entry.Name() + "/",
		})
	}

	sort.Slice(tools, func(i, j int) bool {
		return strings.ToLower(tools[i].Name) < strings.ToLower(tools[j].Name)
	})
	return tools
}

func (app *App) handleToolsIndex(w http.ResponseWriter, r *http.Request) {
	// Keep the trailing slash canonical so relative links behave the same way
	// whichever form was typed.
	if r.URL.Path == "/"+toolsRoot {
		http.Redirect(w, r, "/"+toolsRoot+"/", http.StatusMovedPermanently)
		return
	}

	data := struct{ Tools []Tool }{Tools: listTools()}
	if err := app.templates.ExecuteTemplate(w, "tools.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// noListingDir serves files but refuses to enumerate a directory, so a folder
// without an index.html reads as missing instead of exposing its contents.
type noListingDir struct{ http.Dir }

func (d noListingDir) Open(name string) (http.File, error) {
	file, err := d.Dir.Open(name)
	if err != nil {
		return nil, err
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.IsDir() {
		return file, nil
	}

	index, err := d.Dir.Open(path.Join(name, "index.html"))
	if err != nil {
		file.Close()
		return nil, os.ErrNotExist
	}
	index.Close()
	return file, nil
}
