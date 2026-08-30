package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ComfyUI stores two JSON blobs alongside its outputs: "workflow" (the editor
// graph, for round-tripping) and "prompt" (the executed API graph). The API
// graph is the one that carries the values actually used, so that is what we
// read. It is a flat map of node id -> node.
type comfyAPINode struct {
	Inputs    map[string]json.RawMessage `json:"inputs"`
	ClassType string                     `json:"class_type"`
	Meta      struct {
		Title string `json:"title"`
	} `json:"_meta"`
}

type comfyAPIGraph map[string]comfyAPINode

// Walking the graph is bounded so a pathological workflow cannot spin here.
const (
	comfyMaxLinkHops   = 12
	comfyMaxLinkVisits = 64
)

var comfyPowerLoraKey = regexp.MustCompile(`^lora_\d+$`)

// unwrapComfyPromptBlob pulls the API graph out of the envelope some ComfyUI
// video nodes write: a single JSON object holding "prompt" and "workflow", each
// as an escaped JSON string rather than a nested object.
func unwrapComfyPromptBlob(jsonText string) (string, bool) {
	var envelope struct {
		Prompt json.RawMessage `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonText)), &envelope); err != nil || len(envelope.Prompt) == 0 {
		return "", false
	}

	// The graph is either embedded as a string or nested directly.
	var nested string
	if err := json.Unmarshal(envelope.Prompt, &nested); err == nil {
		return nested, strings.TrimSpace(nested) != ""
	}
	if bytes.HasPrefix(bytes.TrimSpace(envelope.Prompt), []byte("{")) {
		return string(envelope.Prompt), true
	}

	return "", false
}

// decodeComfyAPIGraph parses an API-format ComfyUI prompt graph, unwrapping the
// envelope some video nodes write around it. It returns false for anything
// else, including the editor "workflow" format.
func decodeComfyAPIGraph(jsonText string) (comfyAPIGraph, bool) {
	if graph, ok := decodeComfyAPINodes(jsonText); ok {
		return graph, true
	}

	// Not a bare graph: try one level of envelope, and only one, so a graph
	// that happens to hold a node named "prompt" cannot loop here.
	if unwrapped, ok := unwrapComfyPromptBlob(jsonText); ok {
		return decodeComfyAPINodes(unwrapped)
	}

	return nil, false
}

// decodeComfyAPINodes parses a flat node-id -> node map, rejecting any JSON
// object whose values are not ComfyUI nodes.
func decodeComfyAPINodes(jsonText string) (comfyAPIGraph, bool) {
	trimmed := strings.TrimSpace(jsonText)
	if !strings.HasPrefix(trimmed, "{") {
		return nil, false
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, false
	}
	if len(raw) == 0 {
		return nil, false
	}

	graph := make(comfyAPIGraph, len(raw))
	for id, rawNode := range raw {
		var node comfyAPINode
		if err := json.Unmarshal(rawNode, &node); err != nil {
			return nil, false
		}
		if node.ClassType == "" {
			return nil, false
		}
		graph[id] = node
	}

	return graph, true
}

// sortedNodeIDs returns node ids in numeric order so extraction is
// deterministic across runs.
func (g comfyAPIGraph) sortedNodeIDs() []string {
	ids := make([]string, 0, len(g))
	for id := range g {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool {
		left, leftErr := strconv.Atoi(ids[a])
		right, rightErr := strconv.Atoi(ids[b])
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		return ids[a] < ids[b]
	})
	return ids
}

func (n comfyAPINode) stringInput(key string) (string, bool) {
	raw, exists := n.Inputs[key]
	if !exists {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

func (n comfyAPINode) floatInput(key string) (float64, bool) {
	raw, exists := n.Inputs[key]
	if !exists {
		return 0, false
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

// linkInput resolves an input wired to another node's output, returning the
// upstream node id.
func (n comfyAPINode) linkInput(key string) (string, bool) {
	raw, exists := n.Inputs[key]
	if !exists {
		return "", false
	}
	var link []json.RawMessage
	if err := json.Unmarshal(raw, &link); err != nil || len(link) == 0 {
		return "", false
	}
	var nodeID string
	if err := json.Unmarshal(link[0], &nodeID); err == nil {
		return nodeID, nodeID != ""
	}
	var numericID int
	if err := json.Unmarshal(link[0], &numericID); err == nil {
		return strconv.Itoa(numericID), true
	}
	return "", false
}

// linkedNodeIDs returns every upstream node this node reads from.
func (n comfyAPINode) linkedNodeIDs() []string {
	var ids []string
	for key := range n.Inputs {
		if id, ok := n.linkInput(key); ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// resolveConditioningText walks upstream from a sampler's positive/negative
// input until it finds a node holding the actual prompt text. Workflows
// routinely insert pass-through nodes (conditioning combines, context bundles,
// reroutes) between the text encoder and the sampler.
func (g comfyAPIGraph) resolveConditioningText(startID string) string {
	type queued struct {
		id    string
		depth int
	}

	visited := map[string]bool{startID: true}
	queue := []queued{{id: startID}}
	visits := 0

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		node, exists := g[current.id]
		if !exists {
			continue
		}

		visits++
		if visits > comfyMaxLinkVisits {
			break
		}

		for _, key := range []string{"text", "prompt", "positive_prompt", "string", "value"} {
			if text, ok := node.stringInput(key); ok {
				return text
			}
		}

		if current.depth >= comfyMaxLinkHops {
			continue
		}
		for _, upstream := range node.linkedNodeIDs() {
			if !visited[upstream] {
				visited[upstream] = true
				queue = append(queue, queued{id: upstream, depth: current.depth + 1})
			}
		}
	}

	return ""
}

// comfyPrompts extracts the positive and negative prompt from a graph.
func (g comfyAPIGraph) comfyPrompts() (string, string) {
	var positive, negative string

	// Preferred path: start from a sampler and follow its conditioning inputs.
	// That is the only way to tell two identical text encoders apart.
	for _, id := range g.sortedNodeIDs() {
		node := g[id]
		positiveLink, hasPositive := node.linkInput("positive")
		negativeLink, hasNegative := node.linkInput("negative")
		if !hasPositive && !hasNegative {
			continue
		}
		if hasPositive && positive == "" {
			positive = g.resolveConditioningText(positiveLink)
		}
		if hasNegative && negative == "" {
			negative = g.resolveConditioningText(negativeLink)
		}
		if positive != "" && negative != "" {
			return positive, negative
		}
	}

	// Fallback: nodes that carry the prompt as a plain widget. Video models such
	// as MiniMax H3 or Wan take the prompt directly, with no text encoder and no
	// positive/negative pair on the sampler.
	var encoderTexts []string
	for _, id := range g.sortedNodeIDs() {
		node := g[id]

		if negative == "" {
			for _, key := range []string{"negative_prompt", "neg_prompt", "negative_text"} {
				if text, ok := node.stringInput(key); ok {
					negative = text
					break
				}
			}
		}

		if positive == "" {
			for _, key := range []string{"prompt", "positive_prompt", "positive_text"} {
				if text, ok := node.stringInput(key); ok {
					positive = text
					break
				}
			}
		}

		if strings.Contains(node.ClassType, "TextEncode") {
			if text, ok := node.stringInput("text"); ok {
				encoderTexts = append(encoderTexts, text)
			}
		}
	}

	// Two bare text encoders and nothing to disambiguate them: the longer one is
	// the positive prompt in practice, negatives being short quality boilerplate.
	if positive == "" && len(encoderTexts) > 0 {
		sort.SliceStable(encoderTexts, func(a, b int) bool {
			return len(encoderTexts[a]) > len(encoderTexts[b])
		})
		positive = encoderTexts[0]
		if negative == "" && len(encoderTexts) == 2 {
			negative = encoderTexts[1]
		}
	}

	return positive, negative
}

// comfyLoras collects the enabled LoRAs from every loader style we have seen.
func (g comfyAPIGraph) comfyLoras() []LoraData {
	var loras []LoraData

	appendLora := func(name string, weight float64) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		// Loader values keep the on-disk path; the display only needs the name.
		name = strings.ReplaceAll(name, "\\", "/")
		if index := strings.LastIndex(name, "/"); index != -1 {
			name = name[index+1:]
		}
		name = strings.TrimSuffix(name, ".safetensors")

		rounded, _ := strconv.ParseFloat(fmt.Sprintf("%.2f", weight), 64)
		loras = append(loras, LoraData{Name: name, Weight: rounded})
	}

	for _, id := range g.sortedNodeIDs() {
		node := g[id]

		if strings.Contains(node.ClassType, "LoraLoader") {
			name, ok := node.stringInput("lora_name")
			if !ok {
				continue
			}
			weight, hasWeight := node.floatInput("strength_model")
			if !hasWeight {
				weight, hasWeight = node.floatInput("strength")
			}
			if !hasWeight {
				weight = 1
			}
			appendLora(name, weight)
			continue
		}

		// rgthree's Power Lora Loader packs each slot into an object widget.
		for key, raw := range node.Inputs {
			if !comfyPowerLoraKey.MatchString(key) {
				continue
			}
			var slot struct {
				On       bool    `json:"on"`
				Lora     string  `json:"lora"`
				Strength float64 `json:"strength"`
			}
			if err := json.Unmarshal(raw, &slot); err != nil || !slot.On {
				continue
			}
			appendLora(slot.Lora, slot.Strength)
		}
	}

	sort.SliceStable(loras, func(a, b int) bool { return loras[a].Name < loras[b].Name })
	return loras
}

// applySamplingParams fills in the generation settings found in the graph,
// without overwriting values an earlier parser already provided.
func (g comfyAPIGraph) applySamplingParams(metadata *ImageMetadata) {
	for _, id := range g.sortedNodeIDs() {
		node := g[id]

		if metadata.Seed == 0 {
			for _, key := range []string{"noise_seed", "seed"} {
				if seed, ok := node.floatInput(key); ok && seed != 0 {
					metadata.Seed = int64(seed)
					break
				}
			}
		}

		if metadata.Steps == 0 {
			for _, key := range []string{"steps", "steps_total"} {
				if steps, ok := node.floatInput(key); ok && steps > 0 {
					metadata.Steps = int(steps)
					break
				}
			}
		}

		if metadata.CFGScale == 0 {
			for _, key := range []string{"cfg", "cfg_scale"} {
				if cfg, ok := node.floatInput(key); ok && cfg > 0 {
					metadata.CFGScale = cfg
					break
				}
			}
		}

		if metadata.Sampler == "" {
			if sampler, ok := node.stringInput("sampler_name"); ok {
				metadata.Sampler = sampler
			}
		}

		if metadata.Scheduler == "" {
			if scheduler, ok := node.stringInput("scheduler"); ok {
				metadata.Scheduler = scheduler
			}
		}

		if metadata.Model == "" {
			if model, ok := g.checkpointName(node); ok {
				metadata.Model = model
			}
		}
	}
}

// checkpointName returns the base model a loader node loads, ignoring the other
// loaders (VAE, CLIP, upscalers) that share the same input naming.
func (g comfyAPIGraph) checkpointName(node comfyAPINode) (string, bool) {
	for _, key := range []string{"ckpt_name", "unet_name"} {
		if name, ok := node.stringInput(key); ok {
			return trimModelName(name), true
		}
	}

	class := strings.ToLower(node.ClassType)
	if strings.Contains(class, "checkpoint") || strings.Contains(class, "diffusion") {
		if name, ok := node.stringInput("model_name"); ok {
			return trimModelName(name), true
		}
	}

	return "", false
}

func trimModelName(name string) string {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	if index := strings.LastIndex(name, "/"); index != -1 {
		name = name[index+1:]
	}
	return strings.TrimSuffix(name, ".safetensors")
}

// parseComfyAPIPrompt reads an API-format ComfyUI graph into image metadata.
func (app *App) parseComfyAPIPrompt(jsonText string, metadata *ImageMetadata) bool {
	graph, ok := decodeComfyAPIGraph(jsonText)
	if !ok {
		return false
	}

	positive, negative := graph.comfyPrompts()
	if positive == "" && negative == "" && len(graph.comfyLoras()) == 0 {
		return false
	}

	if positive != "" && metadata.Prompt == "" {
		cleaned, inlineLoRAs := extractLoRAs(positive)
		metadata.Prompt = cleaned
		metadata.LoRAs = append(metadata.LoRAs, inlineLoRAs...)
	}
	if negative != "" && metadata.NegPrompt == "" {
		cleaned, inlineLoRAs := extractLoRAs(negative)
		metadata.NegPrompt = cleaned
		metadata.LoRAs = append(metadata.LoRAs, inlineLoRAs...)
	}

	metadata.LoRAs = append(metadata.LoRAs, graph.comfyLoras()...)
	graph.applySamplingParams(metadata)

	log.Printf("Parsed ComfyUI API graph: %d nodes, prompt=%q, model=%s, seed=%d",
		len(graph), truncateForLog(metadata.Prompt, 50), metadata.Model, metadata.Seed)
	return true
}

func truncateForLog(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
