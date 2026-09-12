package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Civitai never parses generation metadata out of uploaded videos: the site's
// addPostImage skips EXIF extraction for MediaType.video, so whatever the
// container carries is ignored. The site's own upload API instead accepts an
// explicit `meta` object per image, so we read the metadata from our database
// (parsed at index time) and push it alongside the file. The flow mirrors the
// official civitai-mcp-server:
//
//  1. POST /api/v1/image-upload   -> {id: <uuid>, uploadURL: <presigned PUT>}
//  2. PUT the file bytes to uploadURL
//  3. POST /api/trpc/post.createWithImages with images[{url: uuid, meta: {...}}]
//     -> creates a draft post, attaches the media, returns post/image ids.
//
// Auth is the user's personal API key (Authorization: Bearer CIVITAI_TOKEN),
// which the session middleware resolves for both endpoints. The account must
// be onboarded and the key must carry the MediaWrite scope (a full key does).
const (
	civitaiBaseURL       = "https://civitai.com"
	civitaiUploadTimeout = 15 * time.Minute // single PUT of up to 750MB
	civitaiAPITimeout    = 30 * time.Second
)

type civitaiUploadResult struct {
	OK      bool   `json:"ok"`
	Already bool   `json:"already"`
	PostID  int64  `json:"post_id,omitempty"`
	ImageID int64  `json:"image_id,omitempty"`
	PostURL string `json:"post_url,omitempty"`
	Message string `json:"message,omitempty"`
}

// writeJSON serialises a JSON response body.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

// civitaiMediaRow carries everything the upload needs from the images row.
type civitaiMediaRow struct {
	Filename       string
	ID             int64
	MediaType      string
	Width          int
	Height         int
	Prompt         string
	NegPrompt      string
	Steps          int
	CFGScale       float64
	Sampler        string
	Scheduler      string
	Seed           int64
	IsNSFW         bool
	SHA256         string
	CivitaiPostID  sql.NullInt64
	CivitaiImageID sql.NullInt64
	ModelName      string
	BaseModel      string
	ModelHash      string
}

// civitaiResource mirrors the resources entries Civitai shows under the media.
type civitaiResource struct {
	Type   string  `json:"type"`
	Name   string  `json:"name,omitempty"`
	Weight float64 `json:"weight,omitempty"`
	Hash   string  `json:"hash,omitempty"`
}

// buildCivitaiMeta maps our stored metadata onto the loose meta schema Civitai
// accepts on image records (prompt/negativePrompt/steps/cfgScale/sampler/seed
// plus passthrough fields like baseModel, hashes, resources and comfy).
func (app *App) buildCivitaiMeta(row *civitaiMediaRow, rawComfyGraph string, loras []LoraData) map[string]any {
	meta := map[string]any{
		"prompt":         row.Prompt,
		"negativePrompt": row.NegPrompt,
		"steps":          row.Steps,
		"cfgScale":       row.CFGScale,
		"sampler":        row.Sampler,
		"seed":           row.Seed,
	}

	if row.Scheduler != "" {
		meta["scheduler"] = row.Scheduler
	}

	baseModel := row.BaseModel
	if baseModel == "" {
		baseModel = "Unknown"
	}
	meta["baseModel"] = baseModel

	hashes := map[string]string{}
	if row.ModelHash != "" {
		hashes["model"] = row.ModelHash
	}
	if len(hashes) > 0 {
		meta["hashes"] = hashes
	}

	resources := []civitaiResource{}
	if row.ModelName != "" {
		resources = append(resources, civitaiResource{
			Type: "model",
			Name: row.ModelName,
			Hash: row.ModelHash,
		})
	}
	for _, lora := range loras {
		resources = append(resources, civitaiResource{
			Type:   "lora",
			Name:   lora.Name,
			Weight: lora.Weight,
		})
	}
	if len(resources) > 0 {
		meta["resources"] = resources
	}

	// The raw ComfyUI API graph, when the container still carries it. Civitai
	// stores this in meta.comfy as stringified JSON and renders the graph on
	// the media page; the schema tolerates an absent workflow field.
	if rawComfyGraph != "" {
		meta["comfy"] = rawComfyGraph
	}

	return meta
}

// computeFileSHA256 hashes a file in 1MB chunks.
func computeFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	buf := make([]byte, 1024*1024)
	for {
		n, err := file.Read(buf)
		if n > 0 {
			if _, hashErr := hasher.Write(buf[:n]); hashErr != nil {
				return "", hashErr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// backfillMissingHashes hashes library videos that predate the sha256 column,
// so the import dedup checks have something to compare against. Images are
// skipped: they are named after their Civitai ID, which the filename-based
// skip already covers.
func (app *App) backfillMissingHashes() {
	rows, err := app.db.Query("SELECT filename FROM images WHERE media_type = 'video' AND sha256 IS NULL")
	if err != nil {
		log.Printf("Warning: could not query videos missing sha256: %v", err)
		return
	}

	var filenames []string
	for rows.Next() {
		var filename string
		if err := rows.Scan(&filename); err == nil {
			filenames = append(filenames, filename)
		}
	}
	rows.Close()

	if len(filenames) == 0 {
		return
	}

	fmt.Printf("Hashing %d videos missing sha256...\n", len(filenames))
	for _, filename := range filenames {
		path, ok := findMediaPath(filename)
		if !ok {
			continue
		}
		hash, err := computeFileSHA256(path)
		if err != nil {
			log.Printf("Warning: could not hash %s: %v", filename, err)
			continue
		}
		if _, err := app.db.Exec("UPDATE images SET sha256 = ? WHERE filename = ?", hash, filename); err != nil {
			log.Printf("Warning: could not store sha256 for %s: %v", filename, err)
		}
	}
}

// handleCivitaiUpload accepts POST /api/civitai/upload?id=<image id> and
// pushes that media (videos only for now) to Civitai as a draft post.
func (app *App) handleCivitaiUpload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"missing or invalid id"}`, http.StatusBadRequest)
		return
	}

	token := strings.TrimSpace(os.Getenv("CIVITAI_TOKEN"))
	if token == "" {
		http.Error(w, `{"error":"CIVITAI_TOKEN not set"}`, http.StatusBadRequest)
		return
	}

	row, err := app.loadCivitaiMediaRow(id)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, `{"error":"media not found"}`, http.StatusNotFound)
			return
		}
		log.Printf("Error loading media %d for Civitai upload: %v", id, err)
		http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
		return
	}

	if row.CivitaiPostID.Valid {
		writeJSON(w, http.StatusOK, civitaiUploadResult{
			OK:      true,
			Already: true,
			PostID:  row.CivitaiPostID.Int64,
			PostURL: fmt.Sprintf("%s/posts/%d", civitaiBaseURL, row.CivitaiPostID.Int64),
		})
		return
	}

	if row.MediaType != mediaTypeVideo {
		http.Error(w, `{"error":"only videos can be uploaded for now"}`, http.StatusBadRequest)
		return
	}

	path, ok := findMediaPath(row.Filename)
	if !ok {
		http.Error(w, `{"error":"file not found in library directories"}`, http.StatusNotFound)
		return
	}

	if row.SHA256 == "" {
		if hash, err := computeFileSHA256(path); err == nil {
			row.SHA256 = hash
			app.db.Exec("UPDATE images SET sha256 = ? WHERE id = ?", hash, id)
		}
	}

	// Re-probe the container for the raw ComfyUI API graph. The DB keeps the
	// parsed fields, but the graph itself gives Civitai the full node map.
	rawComfyGraph := ""
	if videoToolsAvailable() {
		if info, err := probeVideo(path); err == nil {
			if graph, ok := decodeComfyAPIGraph(info.Tags["prompt"]); ok {
				if encoded, err := json.Marshal(graph); err == nil {
					rawComfyGraph = string(encoded)
				}
			}
		}
	}

	loras := app.loadLorasForImage(id)
	meta := app.buildCivitaiMeta(row, rawComfyGraph, loras)

	if err := app.civitaiUploadAndCreatePost(token, path, row, meta); err != nil {
		log.Printf("Civitai upload failed for %s: %v", row.Filename, err)
		writeJSON(w, http.StatusBadGateway, civitaiUploadResult{OK: false, Message: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, civitaiUploadResult{
		OK:      true,
		PostID:  row.CivitaiPostID.Int64,
		ImageID: row.CivitaiImageID.Int64,
		PostURL: fmt.Sprintf("%s/posts/%d", civitaiBaseURL, row.CivitaiPostID.Int64),
	})
}

// loadCivitaiMediaRow reads the upload-relevant fields for one media row.
func (app *App) loadCivitaiMediaRow(id int64) (*civitaiMediaRow, error) {
	query := `
	SELECT i.id, i.filename, i.media_type, i.width, i.height, i.prompt, i.neg_prompt,
	       i.steps, i.cfg_scale, i.sampler, i.scheduler, i.seed, i.is_nsfw,
	       i.sha256, i.civitai_post_id, i.civitai_image_id,
	       COALESCE(m.name, ''), COALESCE(m.base_model, ''), COALESCE(m.hash, i.model_hash)
	FROM images i
	LEFT JOIN models m ON i.model_id = m.id
	WHERE i.id = ?`

	row := &civitaiMediaRow{}
	err := app.db.QueryRow(query, id).Scan(
		&row.ID, &row.Filename, &row.MediaType, &row.Width, &row.Height,
		&row.Prompt, &row.NegPrompt, &row.Steps, &row.CFGScale,
		&row.Sampler, &row.Scheduler, &row.Seed, &row.IsNSFW,
		&row.SHA256, &row.CivitaiPostID, &row.CivitaiImageID,
		&row.ModelName, &row.BaseModel, &row.ModelHash,
	)
	if err != nil {
		return nil, err
	}
	return row, nil
}

// loadLorasForImage returns the LoRAs recorded for a media row.
func (app *App) loadLorasForImage(id int64) []LoraData {
	rows, err := app.db.Query("SELECT name, weight FROM loras WHERE image_id = ?", id)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var loras []LoraData
	for rows.Next() {
		var lora LoraData
		if err := rows.Scan(&lora.Name, &lora.Weight); err == nil {
			loras = append(loras, lora)
		}
	}
	return loras
}

// civitaiUploadAndCreatePost runs the three-step upload and records the
// returned Civitai IDs on the local row.
func (app *App) civitaiUploadAndCreatePost(token, path string, row *civitaiMediaRow, meta map[string]any) error {
	apiClient := &http.Client{Timeout: civitaiAPITimeout}
	uploadClient := &http.Client{Timeout: civitaiUploadTimeout}

	// 1. Presign.
	uuid, uploadURL, err := civitaiPresignUpload(apiClient, token)
	if err != nil {
		return fmt.Errorf("presign failed: %w", err)
	}

	// 2. PUT the bytes.
	if err := civitaiPutFile(uploadClient, uploadURL, path); err != nil {
		return fmt.Errorf("file upload failed: %w", err)
	}

	// 3. Create the draft post.
	postID, imageID, err := civitaiCreatePost(apiClient, token, uuid, row, meta)
	if err != nil {
		return fmt.Errorf("post creation failed: %w", err)
	}

	_, err = app.db.Exec(
		"UPDATE images SET civitai_image_id = ?, civitai_post_id = ?, civitai_uploaded_at = CURRENT_TIMESTAMP WHERE id = ?",
		imageID, postID, row.ID,
	)
	if err != nil {
		return fmt.Errorf("uploaded to Civitai but could not record IDs locally: %w", err)
	}

	row.CivitaiPostID = sql.NullInt64{Int64: postID, Valid: true}
	row.CivitaiImageID = sql.NullInt64{Int64: imageID, Valid: true}

	log.Printf("Uploaded %s to Civitai: post %d, image %d", row.Filename, postID, imageID)
	return nil
}

// civitaiPresignUpload requests a presigned upload URL. The returned id is the
// bare S3 key (a UUID) that post.createWithImages expects as image url.
func civitaiPresignUpload(client *http.Client, token string) (string, string, error) {
	req, err := http.NewRequest("POST", civitaiBaseURL+"/api/v1/image-upload", strings.NewReader("{}"))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("status %d: %s", resp.StatusCode, truncateForLog(string(body), 200))
	}

	var payload struct {
		ID        string `json:"id"`
		UploadURL string `json:"uploadURL"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", "", err
	}
	if payload.ID == "" || payload.UploadURL == "" {
		return "", "", fmt.Errorf("upload endpoint returned no id/uploadURL")
	}
	return payload.ID, payload.UploadURL, nil
}

// civitaiPutFile streams the file to the presigned URL in one PUT.
func civitaiPutFile(client *http.Client, uploadURL, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return err
	}

	req, err := http.NewRequest("PUT", uploadURL, file)
	if err != nil {
		return err
	}
	req.ContentLength = stat.Size()
	req.Header.Set("Content-Type", mime.TypeByExtension(filepath.Ext(path)))

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncateForLog(string(body), 200))
	}
	return nil
}

// civitaiCreatePost calls post.createWithImages through the tRPC endpoint.
// Publish is always false: the post lands as a draft the user reviews.
func civitaiCreatePost(client *http.Client, token, uuid string, row *civitaiMediaRow, meta map[string]any) (int64, int64, error) {
	image := map[string]any{
		"url":    uuid,
		"type":   row.MediaType,
		"width":  row.Width,
		"height": row.Height,
		"index":  0,
		"meta":   meta,
	}

	input := map[string]any{
		"images":  []any{image},
		"publish": false,
	}

	wrapped := map[string]any{"json": input}
	body, err := json.Marshal(wrapped)
	if err != nil {
		return 0, 0, err
	}

	req, err := http.NewRequest("POST", civitaiBaseURL+"/api/trpc/post.createWithImages", bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, 0, err
	}

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("status %d: %s", resp.StatusCode, truncateForLog(string(raw), 300))
	}

	// Unwrap {result: {data: {json: {...}}}}.
	var envelope struct {
		Result struct {
			Data struct {
				JSON struct {
					ID       int64   `json:"id"`
					ImageIDs []int64 `json:"imageIds"`
				} `json:"json"`
			} `json:"data"`
		} `json:"result"`
		Error *struct {
			JSON struct {
				Message string `json:"message"`
			} `json:"json"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return 0, 0, fmt.Errorf("unexpected response: %s", truncateForLog(string(raw), 300))
	}
	if envelope.Error != nil {
		return 0, 0, fmt.Errorf("%s", envelope.Error.JSON.Message)
	}
	if envelope.Result.Data.JSON.ID == 0 {
		return 0, 0, fmt.Errorf("no post id in response: %s", truncateForLog(string(raw), 300))
	}

	imageID := int64(0)
	if len(envelope.Result.Data.JSON.ImageIDs) > 0 {
		imageID = envelope.Result.Data.JSON.ImageIDs[0]
	}
	return envelope.Result.Data.JSON.ID, imageID, nil
}
