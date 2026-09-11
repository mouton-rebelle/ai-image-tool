package main

import "testing"

// A conventional Stable Diffusion graph: two identical text encoders that can
// only be told apart by following the sampler's positive / negative inputs.
const samplerGraph = `{
  "3": {"class_type": "KSampler", "inputs": {
    "seed": 987654321, "steps": 28, "cfg": 7.5,
    "sampler_name": "dpmpp_2m", "scheduler": "karras",
    "model": ["10", 0], "positive": ["6", 0], "negative": ["7", 0]
  }},
  "6": {"class_type": "CLIPTextEncode", "inputs": {"text": "a lighthouse at dusk", "clip": ["4", 0]}},
  "7": {"class_type": "CLIPTextEncode", "inputs": {"text": "blurry, watermark", "clip": ["4", 0]}},
  "4": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "sdxl/realvis.safetensors"}},
  "10": {"class_type": "LoraLoader", "inputs": {
    "lora_name": "styles\\film_grain.safetensors", "strength_model": 0.75, "strength_clip": 0.75,
    "model": ["4", 0], "clip": ["4", 1]
  }}
}`

// Video models take the prompt as a plain widget: no text encoder, and no
// positive/negative pair on the sampler to follow.
const widgetPromptGraph = `{
  "1": {"class_type": "UNETLoader", "inputs": {"unet_name": "minimax_h3_fl2va.safetensors"}},
  "70": {"class_type": "Seed (rgthree)", "inputs": {"seed": 558679450481796}},
  "65": {"class_type": "KSampler Config (rgthree)", "inputs": {
    "steps_total": 8, "cfg": 1, "sampler_name": "er_sde", "scheduler": "simple"
  }},
  "81": {"class_type": "MiniMaxH3ImageToVideo", "inputs": {"prompt": "she walks toward the window", "length": 124}},
  "22": {"class_type": "Power Lora Loader (rgthree)", "inputs": {
    "lora_1": {"on": true, "lora": "minimax\\turbo_8step.safetensors", "strength": 1},
    "lora_2": {"on": false, "lora": "minimax\\disabled.safetensors", "strength": 1}
  }}
}`

func TestParseComfyAPIPromptSamplerGraph(t *testing.T) {
	app := &App{}
	metadata := &ImageMetadata{}

	if !app.parseComfyAPIPrompt(samplerGraph, metadata) {
		t.Fatal("parseComfyAPIPrompt returned false for a valid graph")
	}

	if metadata.Prompt != "a lighthouse at dusk" {
		t.Errorf("Prompt = %q", metadata.Prompt)
	}
	if metadata.NegPrompt != "blurry, watermark" {
		t.Errorf("NegPrompt = %q", metadata.NegPrompt)
	}
	if metadata.Seed != 987654321 {
		t.Errorf("Seed = %d", metadata.Seed)
	}
	if metadata.Steps != 28 {
		t.Errorf("Steps = %d", metadata.Steps)
	}
	if metadata.CFGScale != 7.5 {
		t.Errorf("CFGScale = %v", metadata.CFGScale)
	}
	if metadata.Sampler != "dpmpp_2m" || metadata.Scheduler != "karras" {
		t.Errorf("Sampler/Scheduler = %q/%q", metadata.Sampler, metadata.Scheduler)
	}
	if metadata.Model != "realvis" {
		t.Errorf("Model = %q, want the checkpoint name without its path or extension", metadata.Model)
	}

	if len(metadata.LoRAs) != 1 {
		t.Fatalf("got %d LoRAs, want 1: %+v", len(metadata.LoRAs), metadata.LoRAs)
	}
	if metadata.LoRAs[0].Name != "film_grain" || metadata.LoRAs[0].Weight != 0.75 {
		t.Errorf("LoRA = %+v", metadata.LoRAs[0])
	}
}

func TestParseComfyAPIPromptWidgetGraph(t *testing.T) {
	app := &App{}
	metadata := &ImageMetadata{}

	if !app.parseComfyAPIPrompt(widgetPromptGraph, metadata) {
		t.Fatal("parseComfyAPIPrompt returned false for a widget-prompt graph")
	}

	if metadata.Prompt != "she walks toward the window" {
		t.Errorf("Prompt = %q", metadata.Prompt)
	}
	if metadata.NegPrompt != "" {
		t.Errorf("NegPrompt = %q, want empty for a model with no negative", metadata.NegPrompt)
	}
	if metadata.Seed != 558679450481796 {
		t.Errorf("Seed = %d", metadata.Seed)
	}
	if metadata.Steps != 8 {
		t.Errorf("Steps = %d", metadata.Steps)
	}
	if metadata.Model != "minimax_h3_fl2va" {
		t.Errorf("Model = %q", metadata.Model)
	}

	// Only the enabled slot of the Power Lora Loader counts.
	if len(metadata.LoRAs) != 1 || metadata.LoRAs[0].Name != "turbo_8step" {
		t.Errorf("LoRAs = %+v, want only the enabled slot", metadata.LoRAs)
	}
}

func TestParseComfyAPIPromptUnwrapsEnvelope(t *testing.T) {
	app := &App{}
	metadata := &ImageMetadata{}

	// ComfyUI's video nodes wrap the graph in an object, as an escaped string.
	envelope := `{"prompt": "{\"6\": {\"class_type\": \"CLIPTextEncode\", \"inputs\": {\"text\": \"a red balloon\"}}}", "workflow": "{}"}`
	if !app.parseComfyAPIPrompt(envelope, metadata) {
		t.Fatal("parseComfyAPIPrompt returned false for an enveloped graph")
	}
	if metadata.Prompt != "a red balloon" {
		t.Errorf("Prompt = %q", metadata.Prompt)
	}
}

func TestParseComfyAPIPromptRejectsOtherJSON(t *testing.T) {
	app := &App{}

	tests := map[string]string{
		"editor workflow": `{"id": "abc", "last_node_id": 81, "nodes": [{"id": 1, "type": "KSampler"}]}`,
		"swarm params":    `{"sui_image_params": {"prompt": "a cat"}}`,
		"empty object":    `{}`,
		"not json":        `a lighthouse at dusk`,
	}

	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			metadata := &ImageMetadata{}
			if app.parseComfyAPIPrompt(input, metadata) {
				t.Errorf("parseComfyAPIPrompt accepted %s: %+v", name, metadata)
			}
		})
	}
}

func TestParseComfyAPIPromptKeepsExistingValues(t *testing.T) {
	app := &App{}
	metadata := &ImageMetadata{Prompt: "already parsed", Seed: 42}

	if !app.parseComfyAPIPrompt(samplerGraph, metadata) {
		t.Fatal("parseComfyAPIPrompt returned false")
	}
	if metadata.Prompt != "already parsed" {
		t.Errorf("Prompt = %q, want the earlier parser's value to win", metadata.Prompt)
	}
	if metadata.Seed != 42 {
		t.Errorf("Seed = %d, want the earlier parser's value to win", metadata.Seed)
	}
}

// ComfyUI writes non-standard bare floats into some nodes ("is_changed":
// [NaN]); Go rejects those tokens, and without sanitising them the whole graph
// is thrown away and its raw JSON ends up stored as the prompt.
func TestParseComfyAPIPromptToleratesBareNaN(t *testing.T) {
	app := &App{}
	metadata := &ImageMetadata{}

	graph := `{"1": {"class_type": "KSampler", "inputs": {"seed": 1, "positive": ["2", 0]}},
	           "2": {"class_type": "PrimitiveStringMultiline", "inputs": {"value": "a calm sea"}},
	           "3": {"class_type": "RIFEInterpolation", "inputs": {"images": ["9", 0]}, "is_changed": [NaN]}}`
	if !app.parseComfyAPIPrompt(graph, metadata) {
		t.Fatal("parseComfyAPIPrompt returned false for a graph containing bare NaN")
	}
	if metadata.Prompt != "a calm sea" {
		t.Errorf("Prompt = %q", metadata.Prompt)
	}
}

// SamplerCustomAdvanced wires a guider node whose conditioning input carries
// the prompt; there is no positive/negative pair to follow anywhere.
func TestParseComfyAPIPromptFollowsGuiderChain(t *testing.T) {
	app := &App{}
	metadata := &ImageMetadata{}

	graph := `{"13": {"class_type": "SamplerCustomAdvanced", "inputs": {"guider": ["9", 0], "noise": ["12", 0]}},
	           "9": {"class_type": "BasicGuider", "inputs": {"conditioning": ["8", 0]}},
	           "8": {"class_type": "CLIPTextEncode", "inputs": {"text": "a lighthouse at dusk"}}}`
	if !app.parseComfyAPIPrompt(graph, metadata) {
		t.Fatal("parseComfyAPIPrompt returned false for a guider-chain graph")
	}
	if metadata.Prompt != "a lighthouse at dusk" {
		t.Errorf("Prompt = %q", metadata.Prompt)
	}
}

// Subgraph-enabled builds flatten the subgraph's inner nodes into the API
// graph under "<instance>:<node>" ids; the prompt widget then sits far from
// any sampler and has no conventional key.
func TestParseComfyAPIPromptReadsSubgraphStringWidget(t *testing.T) {
	app := &App{}
	metadata := &ImageMetadata{}

	graph := `{"5": {"class_type": "MiniMaxH3ReferenceToVideo", "inputs": {"prompt": ["22:11", 0], "seed": ["16", 0]}},
	           "16": {"class_type": "easy seed", "inputs": {"seed": 459165427935710}},
	           "22:11": {"class_type": "PrimitiveStringMultiline", "inputs": {"value": "a pov shot of a garden wall"}}}`
	if !app.parseComfyAPIPrompt(graph, metadata) {
		t.Fatal("parseComfyAPIPrompt returned false for a subgraph-flattened graph")
	}
	if metadata.Prompt != "a pov shot of a garden wall" {
		t.Errorf("Prompt = %q", metadata.Prompt)
	}
	if metadata.Seed != 459165427935710 {
		t.Errorf("Seed = %d", metadata.Seed)
	}
}

// A JSON blob no parser recognises must be dropped, not stored as the prompt:
// leaking it there is what filled the grid with raw workflow JSON.
func TestParseGenerationParamsIgnoresUnparsableJSON(t *testing.T) {
	app := &App{}
	metadata := &ImageMetadata{}

	app.parseGenerationParams(`{"prompt": "{\"5\": {\"inputs\": {\"prompt\": [\"22:11\", 0]}}}", "unknown_key": true}`, metadata)
	if metadata.Prompt != "" {
		t.Errorf("Prompt = %q, want an unparsable JSON blob to be ignored", metadata.Prompt)
	}
}
