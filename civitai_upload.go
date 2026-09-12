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
	"net/url"
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

	// civitai.red is the explicit-content domain; post links in the UI point
	// there. The upload API itself always talks to civitai.com.
	civitaiWebURL = "https://civitai.red"
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

// civitaiCheckpointVersion is a Civitai model version matched to our local
// checkpoint. Linking it via meta.civitaiResources is what makes the base
// model chip resolve on the site: the chip reads the linked version's
// baseModel, not the meta.baseModel string.
type civitaiCheckpointVersion struct {
	ID        int64
	BaseModel string
}

// buildCivitaiMeta maps our stored metadata onto the loose meta schema Civitai
// accepts on image records (prompt/negativePrompt/steps/cfgScale/sampler/seed
// plus passthrough fields like baseModel, hashes, resources and comfy). The
// base model chip on Civitai is derived from linked model versions, not from
// the baseModel string, so a resolved checkpoint also carries civitaiResources.
func (app *App) buildCivitaiMeta(row *civitaiMediaRow, rawComfyGraph string, loras []LoraData, resolved *civitaiCheckpointVersion, modelSHA string) map[string]any {
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

	if resolved != nil && resolved.BaseModel != "" {
		meta["baseModel"] = resolved.BaseModel
	} else if row.BaseModel != "" {
		meta["baseModel"] = row.BaseModel
	}

	hashes := map[string]string{}
	// The full SHA256 of the real weight file is what Civitai matches its
	// model files against; a stored short hash is only a fallback.
	modelHash := modelSHA
	if modelHash == "" && row.ModelHash != "" && !strings.HasPrefix(row.ModelHash, "local:") {
		modelHash = row.ModelHash
	}
	if modelHash != "" {
		hashes["model"] = modelHash
		meta["hashes"] = hashes
	}

	resources := []civitaiResource{}
	if row.ModelName != "" {
		resources = append(resources, civitaiResource{
			Type: "model",
			Name: row.ModelName,
			Hash: modelHash,
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

	if resolved != nil && resolved.ID != 0 {
		meta["civitaiResources"] = []map[string]any{
			{"type": "checkpoint", "modelVersionId": resolved.ID},
		}
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

	// force=1 re-uploads media that already carries a Civitai post id, for
	// example to refresh the meta after a fix. The old draft stays on the
	// account; the row's recorded IDs move to the new post.
	force := r.URL.Query().Get("force") == "1"

	// A post id can go stale when the draft was deleted on the site; verify
	// before claiming an upload is done, and clear the stale record so the
	// media can be uploaded again.
	apiClient := &http.Client{Timeout: civitaiAPITimeout}
	if row.CivitaiPostID.Valid && !force {
		if app.civitaiPostExists(apiClient, token, row.CivitaiPostID.Int64) {
			writeJSON(w, http.StatusOK, civitaiUploadResult{
				OK:      true,
				Already: true,
				PostID:  row.CivitaiPostID.Int64,
				PostURL: fmt.Sprintf("%s/posts/%d", civitaiWebURL, row.CivitaiPostID.Int64),
			})
			return
		}
		log.Printf("Civitai post %d no longer exists, clearing upload record for %s", row.CivitaiPostID.Int64, row.Filename)
		app.db.Exec("UPDATE images SET civitai_image_id = NULL, civitai_post_id = NULL, civitai_uploaded_at = NULL WHERE id = ?", row.ID)
		row.CivitaiPostID = sql.NullInt64{}
		row.CivitaiImageID = sql.NullInt64{}
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

	// Hash the checkpoint file on disk when it is reachable: a full SHA256
	// lets Civitai match the published model file exactly (same mechanism as
	// A1111 metadata), which is the only safe attribution for local
	// finetunes whose names appear nowhere on Civitai.
	modelSHA := locateModelFileSHA256(row.ModelName)

	// Resolve the checkpoint to a Civitai model version so the base model
	// chip can be recognized (the chip reads linked resources, not the
	// meta.baseModel string). Best-effort: an unresolved checkpoint only
	// means the chip stays blank.
	resolved := app.resolveCivitaiCheckpoint(apiClient, token, row, modelSHA)

	loras := app.loadLorasForImage(id)
	meta := app.buildCivitaiMeta(row, rawComfyGraph, loras, resolved, modelSHA)

	if err := app.civitaiUploadAndCreatePost(token, path, row, meta); err != nil {
		log.Printf("Civitai upload failed for %s: %v", row.Filename, err)
		writeJSON(w, http.StatusBadGateway, civitaiUploadResult{OK: false, Message: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, civitaiUploadResult{
		OK:      true,
		PostID:  row.CivitaiPostID.Int64,
		ImageID: row.CivitaiImageID.Int64,
		PostURL: fmt.Sprintf("%s/posts/%d", civitaiWebURL, row.CivitaiPostID.Int64),
	})
}

// resolveCivitaiCheckpoint matches our local checkpoint to a Civitai model
// version, in order of trust: the SHA256 of the actual weight file on disk
// (exact, same mechanism as A1111 metadata), a stored real file hash through
// the public by-hash endpoint, then a strict name search. Local ComfyUI
// finetunes only resolve when their weight file is reachable and published.
func (app *App) resolveCivitaiCheckpoint(client *http.Client, token string, row *civitaiMediaRow, modelSHA string) *civitaiCheckpointVersion {
	if row.ModelName == "" {
		return nil
	}

	// A full SHA256 of the real file is the strongest signal.
	if modelSHA != "" {
		if version, err := civitaiVersionByHash(client, token, modelSHA); err == nil && version != nil {
			return version
		}
	}

	if row.ModelHash != "" && !strings.HasPrefix(row.ModelHash, "local:") {
		if version, err := civitaiVersionByHash(client, token, row.ModelHash); err == nil && version != nil {
			return version
		}
	}

	if version, err := civitaiVersionByName(client, token, row.ModelName); err == nil && version != nil {
		return version
	}
	return nil
}

// locateModelFileSHA256 hashes the weight file named after the checkpoint,
// searching COMFYUI_MODELS_DIR (':'-separated roots) up to two levels deep,
// mirroring ComfyUI's unet/diffusion_models/checkpoints layout. Returns ""
// when the file is not reachable, so the upload proceeds without a hash.
func locateModelFileSHA256(checkpointName string) string {
	roots := strings.Split(os.Getenv("COMFYUI_MODELS_DIR"), string(os.PathListSeparator))
	if len(roots) == 1 && roots[0] == "" {
		return ""
	}

	if !strings.HasSuffix(strings.ToLower(checkpointName), ".safetensors") {
		checkpointName += ".safetensors"
	}

	for _, root := range roots {
		if root == "" {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			// Models sit either directly in the root or one level down in
			// subdirectories like unet/ and diffusion_models/.
			var candidates []string
			if entry.IsDir() {
				candidates = []string{filepath.Join(root, entry.Name(), checkpointName)}
			} else {
				candidates = []string{filepath.Join(root, entry.Name())}
			}
			for _, candidate := range candidates {
				info, err := os.Stat(candidate)
				if err != nil || info.IsDir() {
					continue
				}
				if !strings.EqualFold(filepath.Base(candidate), checkpointName) {
					continue
				}
				hash, err := computeFileSHA256(candidate)
				if err != nil {
					log.Printf("Could not hash %s: %v", candidate, err)
					return ""
				}
				log.Printf("Hashed checkpoint %s -> %s", candidate, hash[:12])
				return hash
			}
		}
	}
	return ""
}

// civitaiPostExists checks whether a post still exists on the site, so a
// draft deleted after an upload does not block re-uploading.
func (app *App) civitaiPostExists(client *http.Client, token string, postID int64) bool {
	wrapped := map[string]any{"json": map[string]any{"id": postID}}
	body, _ := json.Marshal(wrapped)

	req, err := http.NewRequest("GET", civitaiBaseURL+"/api/trpc/post.get?input="+url.QueryEscape(string(body)), nil)
	if err != nil {
		return true // fail open: never block a re-upload on a probe error
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return true
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return true
	}

	var envelope struct {
		Result struct {
			Data struct {
				JSON *json.RawMessage `json:"json"`
			} `json:"data"`
		} `json:"result"`
		Error *struct {
			JSON struct {
				Message string `json:"message"`
			} `json:"json"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return true
	}
	if envelope.Error != nil {
		return false
	}
	if envelope.Result.Data.JSON == nil {
		return false
	}
	// post.get answers null for a missing or not-visible post.
	var post map[string]any
	if err := json.Unmarshal(*envelope.Result.Data.JSON, &post); err != nil || post == nil {
		return false
	}
	_, ok := post["id"]
	return ok
}

// civitaiVersionByHash resolves a checkpoint file hash (AutoV2 or SHA256) to
// its model version.
func civitaiVersionByHash(client *http.Client, token, hash string) (*civitaiCheckpointVersion, error) {
	req, err := http.NewRequest("GET", civitaiBaseURL+"/api/v1/model-versions/by-hash/"+url.PathEscape(hash), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("by-hash lookup status %d", resp.StatusCode)
	}

	var payload struct {
		ID        int64  `json:"id"`
		BaseModel string `json:"baseModel"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.ID == 0 {
		return nil, fmt.Errorf("no version id in by-hash response")
	}
	return &civitaiCheckpointVersion{ID: payload.ID, BaseModel: payload.BaseModel}, nil
}

// civitaiVersionByName searches Civitai models by checkpoint name. A match is
// accepted only when a file name equals the checkpoint name (with or without
// the .safetensors suffix) or when exactly one Checkpoint-typed model comes
// back — anything fuzzier stays unlinked rather than guessing a base model.
func civitaiVersionByName(client *http.Client, token, checkpointName string) (*civitaiCheckpointVersion, error) {
	params := url.Values{}
	params.Set("query", checkpointName)
	params.Set("limit", "20")

	req, err := http.NewRequest("GET", civitaiBaseURL+"/api/v1/models?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model search status %d", resp.StatusCode)
	}

	var payload struct {
		Items []struct {
			Type          string `json:"type"`
			Name          string `json:"name"`
			ModelVersions []struct {
				ID        int64  `json:"id"`
				BaseModel string `json:"baseModel"`
				Files     []struct {
					Name string `json:"name"`
				} `json:"files"`
			} `json:"modelVersions"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	name := strings.ToLower(strings.TrimSuffix(checkpointName, ".safetensors"))
	var checkpoints []int
	for i, item := range payload.Items {
		for _, version := range item.ModelVersions {
			for _, file := range version.Files {
				if strings.ToLower(strings.TrimSuffix(file.Name, ".safetensors")) == name {
					return &civitaiCheckpointVersion{ID: version.ID, BaseModel: version.BaseModel}, nil
				}
			}
		}
		if strings.EqualFold(item.Type, "Checkpoint") {
			checkpoints = append(checkpoints, i)
		}
	}

	// No file-name match: accept a single Checkpoint result whose name shares
	// a token with the checkpoint name, otherwise refuse to guess.
	if len(checkpoints) == 1 {
		item := payload.Items[checkpoints[0]]
		if len(item.ModelVersions) > 0 {
			version := item.ModelVersions[0]
			return &civitaiCheckpointVersion{ID: version.ID, BaseModel: version.BaseModel}, nil
		}
	}
	return nil, nil
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
