package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// extractLoRAs extracts LoRA information from text and returns cleaned text and LoRA data
func extractLoRAs(text string) (string, []LoraData) {
	// Regex to match LoRA patterns like <lora:name:weight>
	loraRegex := regexp.MustCompile(`<lora:([^:]+):([^>]+)>`)
	
	var loras []LoraData
	
	// Find all LoRA matches
	matches := loraRegex.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) == 3 {
			name := match[1]
			weightStr := match[2]
			
			// Parse the weight as float
			weight, err := strconv.ParseFloat(weightStr, 64)
			if err != nil {
				continue // Skip if can't parse weight
			}
			
			// Round to 2 decimal places
			roundedWeight := fmt.Sprintf("%.2f", weight)
			finalWeight, _ := strconv.ParseFloat(roundedWeight, 64)
			
			loras = append(loras, LoraData{
				Name:   name,
				Weight: finalWeight,
			})
		}
	}
	
	// Remove all LoRA tags from the text
	cleanedText := loraRegex.ReplaceAllString(text, "")
	
	// Clean up extra spaces that might be left after removing LoRAs
	cleanedText = regexp.MustCompile(`\s+`).ReplaceAllString(cleanedText, " ")
	cleanedText = strings.TrimSpace(cleanedText)
	
	return cleanedText, loras
}

func (app *App) parseGenerationParams(text string, metadata *ImageMetadata) {
	// Clean Unicode encoding where spaces are inserted between characters
	cleanText := app.cleanUnicodeText(text)

	// Try to parse common AI generation parameters
	// Look for prompt at the beginning (often the first part before parameters)

	// Check if this looks like a generation parameters string
	if strings.Contains(cleanText, "Steps:") || strings.Contains(cleanText, "CFG scale:") || strings.Contains(cleanText, "Sampler:") {
		// Split by lines and collect all prompt lines before parameters
		parts := strings.Split(cleanText, "\n")
		if len(parts) > 0 && metadata.Prompt == "" {
			var promptLines []string

			// Collect all lines until we hit "Negative prompt:" or parameter lines
			for _, line := range parts {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" {
					// Add empty lines to preserve formatting
					promptLines = append(promptLines, trimmed)
					continue
				}
				if strings.HasPrefix(trimmed, "Negative prompt:") ||
					strings.Contains(trimmed, "Steps:") ||
					strings.Contains(trimmed, "CFG scale:") ||
					strings.Contains(trimmed, "Sampler:") ||
					strings.Contains(trimmed, "Model:") ||
					strings.Contains(trimmed, "Seed:") ||
					strings.Contains(trimmed, "Size:") ||
					strings.Contains(trimmed, "Version:") {
					break
				}
				promptLines = append(promptLines, trimmed)
			}

			// Join all prompt lines and set as the full prompt
			if len(promptLines) > 0 {
				fullPrompt := strings.Join(promptLines, "\n")
				cleanedPrompt, promptLoRAs := extractLoRAs(fullPrompt)
				metadata.Prompt = cleanedPrompt
				metadata.LoRAs = append(metadata.LoRAs, promptLoRAs...)
			}
		}

		// Look for negative prompt
		if negIndex := strings.Index(cleanText, "Negative prompt:"); negIndex != -1 {
			negStart := negIndex + len("Negative prompt:")
			negEnd := strings.Index(cleanText[negStart:], "\n")
			if negEnd == -1 {
				negEnd = strings.Index(cleanText[negStart:], "Steps:")
			}
			if negEnd != -1 {
				negPrompt := strings.TrimSpace(cleanText[negStart : negStart+negEnd])
				cleanedNegPrompt, negLoRAs := extractLoRAs(negPrompt)
				metadata.NegPrompt = cleanedNegPrompt
				metadata.LoRAs = append(metadata.LoRAs, negLoRAs...)
			} else {
				negPrompt := strings.TrimSpace(cleanText[negStart:])
				cleanedNegPrompt, negLoRAs := extractLoRAs(negPrompt)
				metadata.NegPrompt = cleanedNegPrompt
				metadata.LoRAs = append(metadata.LoRAs, negLoRAs...)
			}
		}
	} else if metadata.Prompt == "" {
		// If no generation parameters found, treat the whole text as prompt
		cleanedPrompt, promptLoRAs := extractLoRAs(cleanText)
		metadata.Prompt = cleanedPrompt
		metadata.LoRAs = append(metadata.LoRAs, promptLoRAs...)
	}

	// Extract numeric parameters
	if strings.Contains(cleanText, "Steps:") {
		if steps := extractParam(cleanText, "Steps:", ","); steps != "" {
			if s, err := strconv.Atoi(strings.TrimSpace(steps)); err == nil {
				metadata.Steps = s
			}
		}
	}

	if strings.Contains(cleanText, "CFG scale:") {
		if cfg := extractParam(cleanText, "CFG scale:", ","); cfg != "" {
			if c, err := strconv.ParseFloat(strings.TrimSpace(cfg), 64); err == nil {
				metadata.CFGScale = c
			}
		}
	}

	if strings.Contains(cleanText, "Sampler:") {
		sampler := extractParam(cleanText, "Sampler:", ",")
		if sampler != "" {
			metadata.Sampler = strings.TrimSpace(sampler)
		}
	}

	if strings.Contains(cleanText, "Schedule type:") {
		scheduler := extractParam(cleanText, "Schedule type:", ",")
		if scheduler != "" {
			metadata.Scheduler = strings.TrimSpace(scheduler)
		}
	} else if strings.Contains(cleanText, "Scheduler:") {
		scheduler := extractParam(cleanText, "Scheduler:", ",")
		if scheduler != "" {
			metadata.Scheduler = strings.TrimSpace(scheduler)
		}
	}

	if strings.Contains(cleanText, "Model:") {
		model := extractParam(cleanText, "Model:", ",")
		if model != "" {
			metadata.Model = strings.TrimSpace(model)
		}
	}

	// Extract model hash if present
	if strings.Contains(cleanText, "Model hash:") {
		hash := extractParam(cleanText, "Model hash:", ",")
		if hash != "" {
			metadata.ModelHash = strings.TrimSpace(hash)
		}
	}

	if strings.Contains(cleanText, "Seed:") {
		if seed := extractParam(cleanText, "Seed:", ","); seed != "" {
			if s, err := strconv.ParseInt(strings.TrimSpace(seed), 10, 64); err == nil {
				metadata.Seed = s
			}
		}
	}
}

func (app *App) cleanUnicodeText(text string) string {
	// Remove "UNICODE" prefix if present
	cleanText := strings.TrimPrefix(text, "UNICODE")
	cleanText = strings.TrimSpace(cleanText)

	// Handle null-separated Unicode text (common in EXIF)
	// Look for pattern where every other byte is null (0x00)
	if len(cleanText) > 10 {
		nullCount := 0
		totalOddPositions := 0

		for i := 1; i < len(cleanText) && i < 100; i += 2 {
			totalOddPositions++
			if cleanText[i] == 0 {
				nullCount++
			}
		}

		// If more than 80% of odd positions are null bytes, this is Unicode-encoded text
		if totalOddPositions > 0 && float64(nullCount)/float64(totalOddPositions) > 0.8 {
			var result strings.Builder
			for i := 0; i < len(cleanText); i += 2 {
				if i < len(cleanText) && cleanText[i] != 0 {
					result.WriteByte(cleanText[i])
				}
			}
			return result.String()
		}

		// Also check for space-separated pattern
		spaceCount := 0
		for i := 1; i < len(cleanText) && i < 100; i += 2 {
			if cleanText[i] == ' ' {
				spaceCount++
			}
		}

		// If more than 80% of odd positions are spaces, this is space-separated Unicode
		if totalOddPositions > 0 && float64(spaceCount)/float64(totalOddPositions) > 0.8 {
			var result strings.Builder
			for i := 0; i < len(cleanText); i += 2 {
				if i < len(cleanText) {
					result.WriteByte(cleanText[i])
				}
			}
			return result.String()
		}
	}

	return cleanText
}

func extractParam(text, prefix, suffix string) string {
	start := strings.Index(text, prefix)
	if start == -1 {
		return ""
	}
	start += len(prefix)

	end := strings.Index(text[start:], suffix)
	if end == -1 {
		return strings.TrimSpace(text[start:])
	}

	return strings.TrimSpace(text[start : start+end])
}

func (app *App) extractPNGMetadata(filePath string, metadata *ImageMetadata) {
	file, err := os.Open(filePath)
	if err != nil {
		return
	}
	defer file.Close()

	// Read PNG signature (8 bytes)
	signature := make([]byte, 8)
	if _, err := file.Read(signature); err != nil {
		return
	}

	// Verify PNG signature
	pngSignature := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	if !bytes.Equal(signature, pngSignature) {
		return
	}

	// Read chunks looking for text chunks
	for {
		// Read chunk length (4 bytes)
		lengthBytes := make([]byte, 4)
		if _, err := file.Read(lengthBytes); err != nil {
			break
		}
		length := binary.BigEndian.Uint32(lengthBytes)

		// Read chunk type (4 bytes)
		typeBytes := make([]byte, 4)
		if _, err := file.Read(typeBytes); err != nil {
			break
		}
		chunkType := string(typeBytes)

		// Read chunk data
		data := make([]byte, length)
		if _, err := file.Read(data); err != nil {
			break
		}

		// Skip CRC (4 bytes)
		file.Seek(4, 1)

		// Process text chunks
		switch chunkType {
		case "tEXt":
			app.processPNGTextChunk(data, metadata)
		case "zTXt":
			app.processPNGzTextChunk(data, metadata)
		case "iTXt":
			app.processPNGiTextChunk(data, metadata)
		case "IEND":
			return // End of PNG file
		}
	}
}

func (app *App) processPNGTextChunk(data []byte, metadata *ImageMetadata) {
	// tEXt format: keyword\0text
	nullIndex := bytes.IndexByte(data, 0)
	if nullIndex == -1 {
		return
	}

	keyword := string(data[:nullIndex])
	text := string(data[nullIndex+1:])

	log.Printf("PNG tEXt chunk - %s: %s", keyword, text)
	app.checkPNGTextForParams(keyword, text, metadata)
}

func (app *App) processPNGzTextChunk(_ []byte, _ *ImageMetadata) {
	// zTXt format: keyword\0compression_method\0compressed_text
	// For now, we'll skip compressed text chunks as they're more complex
	log.Printf("Found zTXt chunk, skipping compressed text parsing")
}

func (app *App) processPNGiTextChunk(data []byte, metadata *ImageMetadata) {
	// iTXt format is more complex with language tags, we'll extract what we can
	nullIndex := bytes.IndexByte(data, 0)
	if nullIndex == -1 {
		return
	}

	keyword := string(data[:nullIndex])
	// Skip compression flag, compression method, language tag, translated keyword
	// and try to find the actual text
	remaining := data[nullIndex+1:]
	if len(remaining) > 0 {
		// Simple extraction - this might need refinement
		text := string(remaining)
		log.Printf("PNG iTXt chunk - %s: %s", keyword, text)
		app.checkPNGTextForParams(keyword, text, metadata)
	}
}

func (app *App) checkPNGTextForParams(keyword, text string, metadata *ImageMetadata) {
	// Check common keys used by AI image generators
	switch strings.ToLower(keyword) {
	case "parameters", "workflow", "prompt", "generation_data", "usercomment", "description":
		app.parseGenerationParams(text, metadata)
	case "software":
		if strings.Contains(strings.ToLower(text), "comfyui") {
			// ComfyUI often stores params in workflow
			app.parseGenerationParams(text, metadata)
		}
	}
}