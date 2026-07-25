package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"

	"github.com/nfnt/resize"
)

const (
	promptImageMaxDimension = 1536
	promptImageJPEGQuality  = 85
	promptImageDetail       = "auto"
)

type PromptImage struct {
	MediaType string
	Data      []byte
	Detail    string
}

func preparePromptImage(imagePath string) (*PromptImage, error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return nil, fmt.Errorf("open prompt image: %w", err)
	}
	defer file.Close()

	return preparePromptImageFromReader(file)
}

func preparePromptImageFromBytes(data []byte) (*PromptImage, error) {
	return preparePromptImageFromReader(bytes.NewReader(data))
}

func preparePromptImageFromReader(source io.Reader) (*PromptImage, error) {
	decoded, _, err := image.Decode(source)
	if err != nil {
		return nil, fmt.Errorf("decode prompt image: %w", err)
	}
	resized := resize.Thumbnail(promptImageMaxDimension, promptImageMaxDimension, decoded, resize.Lanczos3)

	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, resized, &jpeg.Options{Quality: promptImageJPEGQuality}); err != nil {
		return nil, fmt.Errorf("encode prompt image: %w", err)
	}

	return &PromptImage{
		MediaType: "image/jpeg",
		Data:      encoded.Bytes(),
		Detail:    promptImageDetail,
	}, nil
}

func (app *App) promptImagePath(filename string, isNSFW bool) (string, error) {
	if filename == "" || filepath.Base(filename) != filename {
		return "", errors.New("invalid image filename")
	}
	directory := "images"
	if isNSFW {
		directory = "images_nsfw"
	}
	return filepath.Join(app.promptImageBaseDir, directory, filename), nil
}
