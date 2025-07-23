package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
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

// cleanPrompt removes LoRA tags and excluded words from a prompt
func cleanPrompt(prompt string, excludedWords []string) string {
	if prompt == "" {
		return ""
	}

	// Remove LoRA tags
	loraRegex := regexp.MustCompile(`<lora:[^>]+>`)
	cleaned := loraRegex.ReplaceAllString(prompt, "")

	// Remove excluded words
	for _, word := range excludedWords {
		if word != "" {
			// Case-insensitive replacement
			regex := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(word) + `\b`)
			cleaned = regex.ReplaceAllString(cleaned, "")
		}
	}

	// Clean up extra spaces and commas
	cleaned = regexp.MustCompile(`\s*,\s*,\s*`).ReplaceAllString(cleaned, ", ")
	cleaned = regexp.MustCompile(`^\s*,\s*`).ReplaceAllString(cleaned, "")
	cleaned = regexp.MustCompile(`\s*,\s*$`).ReplaceAllString(cleaned, "")
	cleaned = regexp.MustCompile(`\s+`).ReplaceAllString(cleaned, " ")
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
		
		// Split into lines and deduplicate
		lines := strings.Split(string(content), "\n")
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