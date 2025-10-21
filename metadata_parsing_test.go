package main

import (
	"testing"
)

// Test data based on actual problematic images
const swarmUITestData = `{ "sui_image_params": { "prompt": "plumb mature Japanese woman with messy black hair brown eyes fair tanned skin small breasts dark blue hoodie, kissing a man with long blonde hair, complementary colors, score_9, score_8_up, score_7_up, high angle, disheveled, street, graffiti, intimate, love, shy, groping, dim light", "negativeprompt": "score_4, score_5, score_3, child, teen, young", "model": "tPonynai3_v65", "seed": 882695506, "steps": 28, "cfgscale": 6.0, "aspectratio": "8:5", "width": 1216, "height": 768, "sampler": "euler_ancestral", "scheduler": "normal", "zeronegative": true, "refinercontrolpercentage": 0.2, "refinermethod": "PostApply", "refinerupscalemethod": "model-4x_NMKD-YandereNeoXL_200k.pth", "refinerdotiling": true, "clipstopatlayer": -2, "automaticvae": true, "initimagecreativity": 0.0, "swarm_version": "0.9.3.1", "date": "2024-10-15", "generation_time": "0.08 (prep) and 2.61 (gen) seconds" } }`

const comfyUITestData = `{"23":{"class_type":"UpscaleModelLoader","inputs":{"model_name":"urn:air:other:upscaler:civitai:147759@164821"},"_meta":{"title":"Load Upscale Model"}},"26":{"class_type":"LoadImage","inputs":{"image":"https://orchestration.civitai.com/v2/consumer/blobs/XDPJCW80YBTEATSNW2GB8K7E30","upload":"image"},"_meta":{"title":"Load Image"}},"22":{"class_type":"ImageUpscaleWithModel","inputs":{"upscale_model":["23",0],"image":["26",0]},"_meta":{"title":"Upscale Image (using Model)"}},"24":{"class_type":"ImageScale","inputs":{"upscale_method":"bilinear","crop":"disabled","width":2048,"height":2048,"image":["22",0]},"_meta":{"title":"Upscale Image"}},"12":{"class_type":"SaveImage","inputs":{"filename_prefix":"ComfyUI","images":["24",0]},"_meta":{"title":"Save Image"}},"extra":{"airs":["urn:air:other:upscaler:civitai:147759@164821"]},"extraMetadata":"{\"prompt\":\"photography, surrealism, closeup, vagina shaped orbit, eyeball, prosthesis, pupil, shaved lashes, flesh, alien like creature, closeup\",\"negativePrompt\":\"\",\"steps\":40,\"cfgScale\":3.5,\"sampler\":\"dpmpp_2m\",\"seed\":1966159266,\"workflowId\":\"img2img-upscale\",\"resources\":[{\"modelVersionId\":699332,\"strength\":1},{\"modelVersionId\":699332,\"strength\":1}]}"}`

func TestSwarmUIMetadataParsing(t *testing.T) {
	app := &App{}
	metadata := &ImageMetadata{}

	// Test Swarm UI parsing
	success := app.parseSwarmUIParams(swarmUITestData, metadata)
	if !success {
		t.Fatal("Failed to parse Swarm UI metadata")
	}

	// Check extracted values
	expectedPrompt := "plumb mature Japanese woman with messy black hair brown eyes fair tanned skin small breasts dark blue hoodie, kissing a man with long blonde hair, complementary colors, score_9, score_8_up, score_7_up, high angle, disheveled, street, graffiti, intimate, love, shy, groping, dim light"
	if metadata.Prompt != expectedPrompt {
		t.Errorf("Expected prompt: %s\nGot: %s", expectedPrompt, metadata.Prompt)
	}

	expectedNegPrompt := "score_4, score_5, score_3, child, teen, young"
	if metadata.NegPrompt != expectedNegPrompt {
		t.Errorf("Expected negative prompt: %s\nGot: %s", expectedNegPrompt, metadata.NegPrompt)
	}

	if metadata.Model != "tPonynai3_v65" {
		t.Errorf("Expected model: tPonynai3_v65, got: %s", metadata.Model)
	}

	if metadata.Seed != 882695506 {
		t.Errorf("Expected seed: 882695506, got: %d", metadata.Seed)
	}

	if metadata.Steps != 28 {
		t.Errorf("Expected steps: 28, got: %d", metadata.Steps)
	}

	if metadata.CFGScale != 6.0 {
		t.Errorf("Expected CFG scale: 6.0, got: %f", metadata.CFGScale)
	}

	if metadata.Sampler != "euler_ancestral" {
		t.Errorf("Expected sampler: euler_ancestral, got: %s", metadata.Sampler)
	}

	if metadata.Scheduler != "normal" {
		t.Errorf("Expected scheduler: normal, got: %s", metadata.Scheduler)
	}
}

func TestComfyUIMetadataParsing(t *testing.T) {
	app := &App{}
	metadata := &ImageMetadata{}

	// Test ComfyUI parsing
	success := app.parseComfyUIWorkflow(comfyUITestData, metadata)
	if !success {
		t.Fatal("Failed to parse ComfyUI metadata")
	}

	// Check extracted values
	expectedPrompt := "photography, surrealism, closeup, vagina shaped orbit, eyeball, prosthesis, pupil, shaved lashes, flesh, alien like creature, closeup"
	if metadata.Prompt != expectedPrompt {
		t.Errorf("Expected prompt: %s\nGot: %s", expectedPrompt, metadata.Prompt)
	}

	if metadata.NegPrompt != "" {
		t.Errorf("Expected empty negative prompt, got: %s", metadata.NegPrompt)
	}

	if metadata.Seed != 1966159266 {
		t.Errorf("Expected seed: 1966159266, got: %d", metadata.Seed)
	}

	if metadata.Steps != 40 {
		t.Errorf("Expected steps: 40, got: %d", metadata.Steps)
	}

	if metadata.CFGScale != 3.5 {
		t.Errorf("Expected CFG scale: 3.5, got: %f", metadata.CFGScale)
	}

	if metadata.Sampler != "dpmpp_2m" {
		t.Errorf("Expected sampler: dpmpp_2m, got: %s", metadata.Sampler)
	}
}

func TestJSONDetectionAndParsing(t *testing.T) {
	app := &App{}

	// Test Swarm UI detection and parsing
	t.Run("SwarmUI", func(t *testing.T) {
		metadata := &ImageMetadata{}
		app.parseGenerationParams(swarmUITestData, metadata)
		
		if metadata.Prompt == "" {
			t.Error("Failed to parse Swarm UI prompt")
		}
		if metadata.Model != "tPonynai3_v65" {
			t.Errorf("Expected model: tPonynai3_v65, got: %s", metadata.Model)
		}
	})

	// Test ComfyUI detection and parsing
	t.Run("ComfyUI", func(t *testing.T) {
		metadata := &ImageMetadata{}
		app.parseGenerationParams(comfyUITestData, metadata)
		
		if metadata.Prompt == "" {
			t.Error("Failed to parse ComfyUI prompt")
		}
		if metadata.Steps != 40 {
			t.Errorf("Expected steps: 40, got: %d", metadata.Steps)
		}
	})

	// Test non-JSON fallback
	t.Run("TraditionalFormat", func(t *testing.T) {
		metadata := &ImageMetadata{}
		traditionalData := "masterpiece, best quality, 1girl\nNegative prompt: bad quality\nSteps: 20, CFG scale: 7, Sampler: DPM++ 2M, Seed: 12345"
		app.parseGenerationParams(traditionalData, metadata)
		
		if metadata.Prompt != "masterpiece, best quality, 1girl" {
			t.Errorf("Failed to parse traditional prompt: %s", metadata.Prompt)
		}
		if metadata.Steps != 20 {
			t.Errorf("Expected steps: 20, got: %d", metadata.Steps)
		}
	})
}