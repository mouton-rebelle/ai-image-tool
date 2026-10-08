package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Civitai serves some animated WebP clips under an image URL, so they land in
// the image libraries with a .jpeg/.png name. Go's golang.org/x/image/webp
// decoder only reads still images and rejects the animated container with
// "webp: invalid format". When that happens we reduce the file to its first
// frame for indexing and thumbnails; the original file is served untouched and
// browsers play the animation in the grid and lightbox.

const webpmuxTimeout = 30 * time.Second

var (
	webpmuxCheckOnce sync.Once
	webpmuxPath      string
)

func resolveWebPTools() {
	webpmuxCheckOnce.Do(func() {
		if path, err := exec.LookPath("webpmux"); err == nil {
			webpmuxPath = path
		}
	})
}

// isWebPMagic reports whether the leading bytes are a RIFF/WebP container.
func isWebPMagic(header []byte) bool {
	return len(header) >= 12 && string(header[0:4]) == "RIFF" && string(header[8:12]) == "WEBP"
}

// decodeAnimatedWebPFirstFrame decodes the first animation frame of a WebP
// file whose full decode failed. The temporary still file is removed before
// returning, so callers only keep the decoded image.
func decodeAnimatedWebPFirstFrame(srcPath string) (image.Image, error) {
	resolveWebPTools()
	if webpmuxPath == "" {
		return nil, fmt.Errorf("animated webp %s needs webpmux, which is not installed", filepath.Base(srcPath))
	}

	ctx, cancel := context.WithTimeout(context.Background(), webpmuxTimeout)
	defer cancel()
	tempPath := filepath.Join(os.TempDir(), fmt.Sprintf("webp-frame-%d.webp", time.Now().UnixNano()))
	cmd := exec.CommandContext(ctx, webpmuxPath, "-get", "frame", "1", srcPath, "-o", tempPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(tempPath)
		return nil, fmt.Errorf("extract first webp frame from %s: %v: %s",
			filepath.Base(srcPath), err, bytes.TrimSpace(out))
	}

	still, err := os.Open(tempPath)
	if err != nil {
		return nil, err
	}
	defer still.Close()
	defer os.Remove(tempPath)

	img, _, err := image.Decode(still)
	if err != nil {
		return nil, fmt.Errorf("decode first webp frame of %s: %w", filepath.Base(srcPath), err)
	}
	return img, nil
}

// decodeImageWithAnimatedWebPFallback wraps image.Decode for library files:
// when the decode fails on a WebP payload, the animated first frame is used
// instead. format is "webp" for the fallback so encoders pick a sane output.
func decodeImageWithAnimatedWebPFallback(imagePath string, header []byte) (img image.Image, format string, err error) {
	img, format, err = decodeFileImage(imagePath)
	if err == nil {
		return img, format, nil
	}
	if !isWebPMagic(header) {
		return nil, "", err
	}

	stillImg, stillErr := decodeAnimatedWebPFirstFrame(imagePath)
	if stillErr != nil {
		log.Printf("Error decoding webp %s: %v", filepath.Base(imagePath), stillErr)
		return nil, "", err // surface the original decode error
	}
	return stillImg, "webp", nil
}

// decodeFileImage decodes the image stored at a path.
func decodeFileImage(imagePath string) (image.Image, string, error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	return image.Decode(file)
}
