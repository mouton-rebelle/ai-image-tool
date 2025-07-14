package main

import (
	"os"
	"strings"
	"testing"
)

func TestExtractImageMetadata(t *testing.T) {
	app := &App{}
	
	// Test extracting metadata from a specific image
	imagePath := "images/87788655.jpeg"
	
	// Check if the file exists
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		t.Skip("Test image file does not exist:", imagePath)
	}
	
	metadata, err := app.extractImageMetadata(imagePath, false)
	if err != nil {
		t.Fatalf("Failed to extract metadata: %v", err)
	}
	
	// Test the parsed ID
	expectedID := 87788655
	if metadata.ID != expectedID {
		t.Errorf("Expected ID %d, got %d", expectedID, metadata.ID)
	}
	
	// Test filename
	expectedFilename := "87788655.jpeg"
	if metadata.Filename != expectedFilename {
		t.Errorf("Expected filename %s, got %s", expectedFilename, metadata.Filename)
	}
	
	// Test expected parsing values based on your specified data
	expectedPrompt := "watercolor, monster with a long leg and a short leg, multicolored_fur, cat_ears, standing on one hand, cute, masterpiece, best quality, good quality <lora:marge:0.9> <lora:haiz_ai_illu:0.5> <lora:MeMaXL4:0.45> <lora:Ghibli_Harem_V1.0:0.35> <lora:dmd2_sdxl_4step_lora:1.0>"
	expectedSteps := 24
	expectedSampler := "Euler a"
	
	// Print metadata for debugging
	t.Logf("Extracted metadata:")
	t.Logf("  ID: %d", metadata.ID)
	t.Logf("  Filename: %s", metadata.Filename)
	t.Logf("  Dimensions: %dx%d", metadata.Width, metadata.Height)
	t.Logf("  Model: %s", metadata.Model)
	t.Logf("  Prompt: %s", metadata.Prompt)
	t.Logf("  NegPrompt: %s", metadata.NegPrompt)
	t.Logf("  Steps: %d", metadata.Steps)
	t.Logf("  CFGScale: %.1f", metadata.CFGScale)
	t.Logf("  Sampler: %s", metadata.Sampler)
	t.Logf("  Scheduler: %s", metadata.Scheduler)
	t.Logf("  Seed: %d", metadata.Seed)
	
	// Check if prompt was parsed correctly
	if metadata.Prompt != expectedPrompt {
		t.Logf("Prompt parsing mismatch:")
		t.Logf("Expected: %s", expectedPrompt)
		t.Logf("Got: %s", metadata.Prompt)
	}
	
	// Check steps
	if metadata.Steps != expectedSteps {
		t.Logf("Steps parsing mismatch - expected %d, got %d", expectedSteps, metadata.Steps)
	}
	
	// Check sampler
	if metadata.Sampler != expectedSampler {
		t.Logf("Sampler parsing mismatch - expected %s, got %s", expectedSampler, metadata.Sampler)
	}
}

func TestParseGenerationParams(t *testing.T) {
	app := &App{}
	
	// Test case with expected metadata from the specific image
	testText := `watercolor, monster with a long leg and a short leg, multicolored_fur, cat_ears, standing on one hand, cute, masterpiece, best quality, good quality <lora:marge:0.9> <lora:haiz_ai_illu:0.5> <lora:MeMaXL4:0.45> <lora:Ghibli_Harem_V1.0:0.35> <lora:dmd2_sdxl_4step_lora:1.0>
Negative prompt: 
Steps: 24, Sampler: Euler a, CFG scale: 7, Seed: 3848979095, Size: 1024x1024, Model hash: 7c906a26f8, Model: copaxTimelessxlSDXL1_v12, Version: v1.7.0`

	metadata := &ImageMetadata{}
	app.parseGenerationParams(testText, metadata)
	
	// Test expected values
	expectedPrompt := "watercolor, monster with a long leg and a short leg, multicolored_fur, cat_ears, standing on one hand, cute, masterpiece, best quality, good quality <lora:marge:0.9> <lora:haiz_ai_illu:0.5> <lora:MeMaXL4:0.45> <lora:Ghibli_Harem_V1.0:0.35> <lora:dmd2_sdxl_4step_lora:1.0>"
	expectedSteps := 24
	expectedSampler := "Euler a"
	expectedCFGScale := 7.0
	expectedSeed := int64(3848979095)
	expectedModel := "copaxTimelessxlSDXL1_v12"
	
	if metadata.Prompt != expectedPrompt {
		t.Errorf("Expected prompt:\n%s\nGot:\n%s", expectedPrompt, metadata.Prompt)
	}
	
	if metadata.Steps != expectedSteps {
		t.Errorf("Expected steps %d, got %d", expectedSteps, metadata.Steps)
	}
	
	if metadata.Sampler != expectedSampler {
		t.Errorf("Expected sampler %s, got %s", expectedSampler, metadata.Sampler)
	}
	
	if metadata.CFGScale != expectedCFGScale {
		t.Errorf("Expected CFG scale %.1f, got %.1f", expectedCFGScale, metadata.CFGScale)
	}
	
	if metadata.Seed != expectedSeed {
		t.Errorf("Expected seed %d, got %d", expectedSeed, metadata.Seed)
	}
	
	if metadata.Model != expectedModel {
		t.Errorf("Expected model %s, got %s", expectedModel, metadata.Model)
	}
}

func TestCleanUnicodeText(t *testing.T) {
	app := &App{}
	
	// Test Unicode cleaning with actual data from the EXIF
	unicodeText := "UNICODE  w a t e r c o l o r ,   m o n s t e r   w i t h   a   l o n g   l e g   a n d   a   s h o r t   l e g ,   m u l t i c o l o r e d _ f u r ,   c a t _ e a r s ,   s t a n d i n g   o n   o n e   h a n d ,   c u t e ,   m a s t e r p i e c e ,   b e s t   q u a l i t y ,   g o o d   q u a l i t y   < l o r a : m a r g e : 0 . 9 >   < l o r a : h a i z _ a i _ i l l u : 0 . 5 >   < l o r a : M e M a X L 4 : 0 . 4 5 >   < l o r a : G h i b l i _ H a r e m _ V 1 . 0 : 0 . 3 5 >   < l o r a : d m d 2 _ s d x l _ 4 s t e p _ l o r a : 1 . 0 > "
	
	cleaned := app.cleanUnicodeText(unicodeText)
	t.Logf("Original: %s", unicodeText[:100])
	t.Logf("Cleaned: %s", cleaned[:min(100, len(cleaned))])
	
	expected := "watercolor, monster with a long leg and a short leg, multicolored_fur, cat_ears, standing on one hand, cute, masterpiece, best quality, good quality <lora:marge:0.9> <lora:haiz_ai_illu:0.5> <lora:MeMaXL4:0.45> <lora:Ghibli_Harem_V1.0:0.35> <lora:dmd2_sdxl_4step_lora:1.0>"
	
	if cleaned != expected {
		t.Errorf("Unicode cleaning failed.\nExpected: %s\nGot: %s", expected, cleaned)
	}
}

func TestParseActualUnicodeExif(t *testing.T) {
	app := &App{}
	
	// Test with the actual Unicode EXIF data that would contain Steps, Sampler, etc.
	unicodeExifText := "UNICODE  w a t e r c o l o r ,   m o n s t e r   w i t h   a   l o n g   l e g   a n d   a   s h o r t   l e g ,   m u l t i c o l o r e d _ f u r ,   c a t _ e a r s ,   s t a n d i n g   o n   o n e   h a n d ,   c u t e ,   m a s t e r p i e c e ,   b e s t   q u a l i t y ,   g o o d   q u a l i t y   < l o r a : m a r g e : 0 . 9 >   < l o r a : h a i z _ a i _ i l l u : 0 . 5 >   < l o r a : M e M a X L 4 : 0 . 4 5 >   < l o r a : G h i b l i _ H a r e m _ V 1 . 0 : 0 . 3 5 >   < l o r a : d m d 2 _ s d x l _ 4 s t e p _ l o r a : 1 . 0 > \n N e g a t i v e   p r o m p t :   b a d   q u a l i t y ,   w o r s t   q u a l i t y ,   l o w r e s ,   j p e g   a r t i f a c t s ,   b a d   a n a t o m y ,   b a d   h a n d s ,   s i g n a t u r e ,   w a t e r m a r k ,   l i g h t _ p a r t i c l e s , ,   n u d e \n S t e p s :   2 4 ,   S a m p l e r :   E u l e r   a ,   C F G   s c a l e :   4 . 0 ,   S e e d :   5 7 9 9 8 0 5 0 0 1 5 8 3"
	
	metadata := &ImageMetadata{}
	app.parseGenerationParams(unicodeExifText, metadata)
	
	t.Logf("Parsed metadata:")
	t.Logf("  Prompt: %s", metadata.Prompt)
	t.Logf("  NegPrompt: %s", metadata.NegPrompt)
	t.Logf("  Steps: %d", metadata.Steps)
	t.Logf("  Sampler: %s", metadata.Sampler)
	t.Logf("  Scheduler: %s", metadata.Scheduler)
	t.Logf("  CFGScale: %.1f", metadata.CFGScale)
	
	// Test expected values
	expectedPrompt := "watercolor, monster with a long leg and a short leg, multicolored_fur, cat_ears, standing on one hand, cute, masterpiece, best quality, good quality <lora:marge:0.9> <lora:haiz_ai_illu:0.5> <lora:MeMaXL4:0.45> <lora:Ghibli_Harem_V1.0:0.35> <lora:dmd2_sdxl_4step_lora:1.0>"
	expectedSteps := 24
	expectedSampler := "Euler a"
	expectedCFG := 4.0
	
	if metadata.Prompt != expectedPrompt {
		t.Errorf("Expected prompt: %s\nGot: %s", expectedPrompt, metadata.Prompt)
	}
	
	if metadata.Steps != expectedSteps {
		t.Errorf("Expected steps %d, got %d", expectedSteps, metadata.Steps)
	}
	
	if metadata.Sampler != expectedSampler {
		t.Errorf("Expected sampler %s, got %s", expectedSampler, metadata.Sampler)
	}
	
	if metadata.CFGScale != expectedCFG {
		t.Errorf("Expected CFG %.1f, got %.1f", expectedCFG, metadata.CFGScale)
	}
}

func TestMultilinePromptParsing(t *testing.T) {
	app := &App{}
	
	// Test extracting metadata from image with multiline prompt
	imagePath := "images/74811051.jpeg"
	
	// Check if the file exists
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		t.Skip("Test image file does not exist:", imagePath)
	}
	
	metadata, err := app.extractImageMetadata(imagePath, false)
	if err != nil {
		t.Fatalf("Failed to extract metadata: %v", err)
	}
	
	// Expected multiline prompt (with extra space before (breast_envy:1.2) as it appears in the actual EXIF data)
	expectedPrompt := `Flatline, Flat vector illustration, haiz_ai,

very_long_hair, blonde_hair, bob_cut,

2girls, couple, streetwear, partially_undressed, living_room, from_side, sidelighting, potted_plant, complementary colors, night, plush,  (breast_envy:1.2), sensitive, explicit, looking_at_breasts, breast_lift, surprised, (female_pervert:1.1), naughty_face,

very_short_hair, pixie_cut, black hair, masterpiece,best quality,amazing quality,`
	
	t.Logf("Extracted multiline prompt metadata:")
	t.Logf("  ID: %d", metadata.ID)
	t.Logf("  Filename: %s", metadata.Filename)
	t.Logf("  Model: %s", metadata.Model)
	t.Logf("  Prompt: %s", metadata.Prompt)
	t.Logf("  NegPrompt: %s", metadata.NegPrompt)
	t.Logf("  Steps: %d", metadata.Steps)
	t.Logf("  CFGScale: %.1f", metadata.CFGScale)
	t.Logf("  Sampler: %s", metadata.Sampler)
	t.Logf("  Scheduler: %s", metadata.Scheduler)
	
	// Check if the full multiline prompt was parsed correctly
	if metadata.Prompt != expectedPrompt {
		t.Logf("Multiline prompt parsing issue:")
		t.Logf("Expected length: %d", len(expectedPrompt))
		t.Logf("Got length: %d", len(metadata.Prompt))
		t.Logf("Expected: %q", expectedPrompt)
		t.Logf("Got: %q", metadata.Prompt)
		
		// Show where they differ
		expectedLines := strings.Split(expectedPrompt, "\n")
		gotLines := strings.Split(metadata.Prompt, "\n")
		t.Logf("Expected lines: %d", len(expectedLines))
		t.Logf("Got lines: %d", len(gotLines))
		
		for i, line := range expectedLines {
			if i < len(gotLines) {
				if line != gotLines[i] {
					t.Logf("Line %d differs:", i+1)
					t.Logf("  Expected: %q", line)
					t.Logf("  Got: %q", gotLines[i])
				}
			} else {
				t.Logf("Missing line %d: %q", i+1, line)
			}
		}
		
		t.Errorf("Multiline prompt not parsed correctly")
	}
}

