package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// loadExcludedWords loads excluded words from excluded_words.txt
func loadExcludedWords() []string {
	file, err := os.Open("excluded_words.txt")
	if err != nil {
		fmt.Printf("Warning: Could not open excluded_words.txt: %v\n", err)
		return []string{}
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		fmt.Printf("Warning: Could not read excluded_words.txt: %v\n", err)
		return []string{}
	}

	words := strings.Split(string(content), ",")
	var cleanWords []string
	for _, word := range words {
		word = strings.TrimSpace(word)
		if word != "" {
			cleanWords = append(cleanWords, word)
		}
	}

	return cleanWords
}

// Precompiled regexes for prompt cleaning
var (
	loraRegex          = regexp.MustCompile(`<lora:[^>]+>`)
	artistTagRegex     = regexp.MustCompile(`@\w+(?:\s+\w+)?`)
	scoreTagRegex      = regexp.MustCompile(`(?i)\bscore_\d+(?:_up)?\b`)
	standaloneNumRegex = regexp.MustCompile(`\b\d{4,}\b`)
	smoothPrefixRegex  = regexp.MustCompile(`(?i)Smooth\s+(?:Quality|Negative)\s*-\s*Illustrious\s*,?\s*`)
	jsonMetadataRegex  = regexp.MustCompile(`(?i)sui_image_params|swarm_version|cfgscale`)
	multiCommaRegex    = regexp.MustCompile(`\s*,\s*,[\s,]*`)
	leadingCommaRegex  = regexp.MustCompile(`^\s*,\s*`)
	trailingCommaRegex = regexp.MustCompile(`\s*,\s*$`)
	multiSpaceRegex = regexp.MustCompile(`\s+`)
)

// sanitizeUTF8 ensures the string is valid UTF-8 by replacing invalid bytes
// and converting common problematic characters (e.g. 0xa0 non-breaking space)
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		// Still replace non-breaking spaces (U+00A0) with regular spaces
		return strings.ReplaceAll(s, "\u00a0", " ")
	}
	// Strip invalid UTF-8 sequences byte by byte
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			// Invalid byte — check if it's a known Latin-1 char we can salvage
			if s[i] == 0xa0 {
				b.WriteByte(' ')
			}
			// Otherwise skip the invalid byte
			i++
			continue
		}
		if r == '\u00a0' {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// cleanPrompt removes LoRA tags, artist tags, quality/score tags, and excluded words from a prompt
func cleanPrompt(prompt string, excludedWords []string) string {
	if prompt == "" {
		return ""
	}

	// Skip entirely if raw JSON metadata leaked through
	if jsonMetadataRegex.MatchString(prompt) {
		return ""
	}

	// Sanitize invalid UTF-8 before any regex processing
	cleaned := sanitizeUTF8(prompt)

	// Remove LoRA tags: <lora:name:weight>
	cleaned = loraRegex.ReplaceAllString(cleaned, "")

	// Remove @artist tags (Anima Preview format): @minaba hideo, @torotei, etc.
	cleaned = artistTagRegex.ReplaceAllString(cleaned, "")

	// Remove score tags: score_6_up, score_7, etc.
	cleaned = scoreTagRegex.ReplaceAllString(cleaned, "")

	// Remove standalone large numbers (model IDs / trigger codes): 106858, etc.
	cleaned = standaloneNumRegex.ReplaceAllString(cleaned, "")

	// Remove "Smooth Quality - Illustrious" / "Smooth Negative- Illustrious" prefixes
	cleaned = smoothPrefixRegex.ReplaceAllString(cleaned, "")

	// Remove excluded words
	for _, word := range excludedWords {
		if word != "" {
			regex := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(word) + `\b`)
			cleaned = regex.ReplaceAllString(cleaned, "")
		}
	}

	// Clean up extra spaces and commas
	cleaned = multiCommaRegex.ReplaceAllString(cleaned, ", ")
	cleaned = leadingCommaRegex.ReplaceAllString(cleaned, "")
	cleaned = trailingCommaRegex.ReplaceAllString(cleaned, "")
	cleaned = multiSpaceRegex.ReplaceAllString(cleaned, " ")
	cleaned = strings.TrimSpace(cleaned)

	return cleaned
}

// appendPromptToFile appends a prompt pair to the appropriate prompt file
func appendPromptToFile(prompt, negPrompt string, isNSFW bool, excludedWords []string) error {
	// Clean the prompts
	cleanedPrompt := cleanPrompt(prompt, excludedWords)
	cleanedNegPrompt := cleanPrompt(negPrompt, excludedWords)

	// Only save if positive prompt exists after cleaning
	if cleanedPrompt == "" {
		return nil
	}

	promptPair := cleanedPrompt + "|||" + cleanedNegPrompt

	// Determine which file to write to
	filename := "prompts_sfw.txt"
	if isNSFW {
		filename = "prompts_nsfw.txt"
	}

	// Append to file
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open %s: %v", filename, err)
	}
	defer file.Close()

	_, err = file.WriteString(promptPair + "\n")
	if err != nil {
		return fmt.Errorf("failed to write to %s: %v", filename, err)
	}

	return nil
}

// deduplicatePromptFiles removes duplicate entries from both prompt files
func deduplicatePromptFiles() error {
	files := []string{"prompts_sfw.txt", "prompts_nsfw.txt"}
	
	for _, filename := range files {
		// Read file contents
		file, err := os.Open(filename)
		if err != nil {
			// File might not exist yet, which is fine
			continue
		}
		
		content, err := io.ReadAll(file)
		file.Close()
		if err != nil {
			return fmt.Errorf("failed to read %s: %v", filename, err)
		}
		
		// Sanitize content in case existing file has invalid UTF-8
		sanitized := sanitizeUTF8(string(content))

		// Split into lines and deduplicate
		lines := strings.Split(sanitized, "\n")
		uniqueLines := make(map[string]bool)
		var dedupedLines []string
		
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !uniqueLines[line] {
				uniqueLines[line] = true
				dedupedLines = append(dedupedLines, line)
			}
		}
		
		// Write back to file
		outFile, err := os.Create(filename)
		if err != nil {
			return fmt.Errorf("failed to create %s: %v", filename, err)
		}
		
		for _, line := range dedupedLines {
			_, err := outFile.WriteString(line + "\n")
			if err != nil {
				outFile.Close()
				return fmt.Errorf("failed to write to %s: %v", filename, err)
			}
		}
		
		outFile.Close()
		fmt.Printf("Deduplicated %s: %d unique prompts\n", filename, len(dedupedLines))
	}
	
	return nil
}