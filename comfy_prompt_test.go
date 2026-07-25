package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func TestHandleComfyGeneratePrompt(t *testing.T) {
	generator := &recordingPromptGenerator{}
	app := &App{promptGenerator: generator}

	requestBody, err := json.Marshal(comfyGeneratePromptRequest{
		Prompt:      "comfy source prompt",
		ImageBase64: base64.StdEncoding.EncodeToString(comfyTestPNG(t, 2048, 1024)),
		TargetModel: "krea-2",
		Concept:     "next",
		Steering:    "  two seconds later  ",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	recorder := httptest.NewRecorder()
	app.handleComfyGeneratePrompt(recorder, httptest.NewRequest(http.MethodPost, "/api/comfy/generate-prompt", bytes.NewReader(requestBody)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if generator.input.SourcePrompt != "comfy source prompt" {
		t.Errorf("unexpected source prompt: %q", generator.input.SourcePrompt)
	}
	if generator.input.Steering != "two seconds later" {
		t.Errorf("unexpected creative direction: %q", generator.input.Steering)
	}
	if !strings.Contains(generator.input.SystemPrompt, "Krea 2") ||
		!strings.Contains(strings.ToLower(generator.input.SystemPrompt), "creative operation — next") {
		t.Errorf("Krea 2 Next profile was not selected: %q", generator.input.SystemPrompt)
	}
	if generator.input.Image == nil || generator.input.Image.MediaType != "image/jpeg" {
		t.Fatalf("unexpected prompt image: %#v", generator.input.Image)
	}
	prepared, format, err := image.Decode(bytes.NewReader(generator.input.Image.Data))
	if err != nil {
		t.Fatalf("decode prepared image: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("unexpected prepared format: %q", format)
	}
	if got := prepared.Bounds().Size(); got.X != 1536 || got.Y != 768 {
		t.Errorf("unexpected prepared image size: %v", got)
	}

	var response generatePromptResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Prompt != "generated result" {
		t.Errorf("unexpected response prompt: %q", response.Prompt)
	}
}

func TestHandleComfyGeneratePromptAcceptsDataURLAndNoPrompt(t *testing.T) {
	generator := &recordingPromptGenerator{}
	app := &App{promptGenerator: generator}

	requestBody, err := json.Marshal(comfyGeneratePromptRequest{
		ImageBase64: "data:image/png;base64," + base64.StdEncoding.EncodeToString(comfyTestPNG(t, 320, 240)),
		TargetModel: "anima",
		Concept:     "describe",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	recorder := httptest.NewRecorder()
	app.handleComfyGeneratePrompt(recorder, httptest.NewRequest(http.MethodPost, "/api/comfy/generate-prompt", bytes.NewReader(requestBody)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if generator.input.Image == nil {
		t.Fatal("the data URL image was not forwarded to the generator")
	}
	if generator.input.SourcePrompt != "" {
		t.Errorf("unexpected source prompt: %q", generator.input.SourcePrompt)
	}
}

func TestHandleComfyGeneratePromptGeneratesWithoutImage(t *testing.T) {
	generator := &recordingPromptGenerator{}
	app := &App{promptGenerator: generator}

	recorder := httptest.NewRecorder()
	app.handleComfyGeneratePrompt(recorder, httptest.NewRequest(http.MethodPost, "/api/comfy/generate-prompt", strings.NewReader(
		`{"prompt":"text only","target_model":"anima","concept":"remix"}`,
	)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if generator.input.Image != nil {
		t.Errorf("unexpected prompt image: %#v", generator.input.Image)
	}
}

func TestHandleComfyGeneratePromptRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected int
	}{
		{name: "empty request", body: `{"target_model":"anima","concept":"remix"}`, expected: http.StatusBadRequest},
		{name: "unknown target model", body: `{"prompt":"a","target_model":"sdxl","concept":"remix"}`, expected: http.StatusBadRequest},
		{name: "unknown concept", body: `{"prompt":"a","target_model":"anima","concept":"invent"}`, expected: http.StatusBadRequest},
		{name: "unknown field", body: `{"prompt":"a","target_model":"anima","image_url":"http://x/y.png"}`, expected: http.StatusBadRequest},
		{name: "undecodable image", body: `{"prompt":"a","target_model":"anima","concept":"remix","image_base64":"!!!"}`, expected: http.StatusBadRequest},
		{name: "non image payload", body: `{"prompt":"a","target_model":"anima","concept":"remix","image_base64":"aGVsbG8="}`, expected: http.StatusBadRequest},
		{
			name:     "steering too long",
			body:     `{"prompt":"a","target_model":"anima","concept":"remix","steering":"` + strings.Repeat("a", maxPromptSteeringCharacters+1) + `"}`,
			expected: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generator := &recordingPromptGenerator{}
			app := &App{promptGenerator: generator}
			recorder := httptest.NewRecorder()

			app.handleComfyGeneratePrompt(recorder, httptest.NewRequest(http.MethodPost, "/api/comfy/generate-prompt", strings.NewReader(tt.body)))

			if recorder.Code != tt.expected {
				t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
			}
			if generator.calls != 0 {
				t.Errorf("generator was called %d times for an invalid request", generator.calls)
			}
		})
	}
}

func TestHandleComfyGeneratePromptRequiresConfiguredGenerator(t *testing.T) {
	app := &App{}
	recorder := httptest.NewRecorder()

	app.handleComfyGeneratePrompt(recorder, httptest.NewRequest(http.MethodPost, "/api/comfy/generate-prompt", strings.NewReader(
		`{"prompt":"a","target_model":"anima","concept":"remix"}`,
	)))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
}

// The bridge node posts a full JPEG, so make sure the wired-up route accepts a
// payload far larger than the in-app prompt generation request allows.
func TestComfyGeneratePromptRouteAcceptsLargePayload(t *testing.T) {
	generator := &recordingPromptGenerator{}
	app := &App{promptGenerator: generator}
	router := mux.NewRouter()
	app.setupRoutes(router)

	server := httptest.NewServer(router)
	defer server.Close()

	requestBody, err := json.Marshal(comfyGeneratePromptRequest{
		Prompt:      "comfy source prompt",
		ImageBase64: base64.StdEncoding.EncodeToString(comfyTestJPEG(t, 1536, 768)),
		TargetModel: "anima",
		Concept:     "remix",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if len(requestBody) <= maxPromptRequestBytes {
		t.Fatalf("test payload is too small to be meaningful: %d bytes", len(requestBody))
	}

	response, err := server.Client().Post(server.URL+"/api/comfy/generate-prompt", "application/json", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, body)
	}
	if generator.calls != 1 {
		t.Errorf("generator was called %d times", generator.calls)
	}
	if generator.input.Image == nil {
		t.Error("the image was not forwarded to the generator")
	}
}

func TestDecodeBase64ImageStripsWhitespace(t *testing.T) {
	decoded, err := decodeBase64Image("aGVs\nbG8=\n")
	if err != nil {
		t.Fatalf("decodeBase64Image returned an error: %v", err)
	}
	if string(decoded) != "hello" {
		t.Errorf("unexpected decoded payload: %q", decoded)
	}
}

func comfyTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, comfyTestImage(width, height), &jpeg.Options{Quality: 88}); err != nil {
		t.Fatalf("encode image: %v", err)
	}
	return encoded.Bytes()
}

func comfyTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, comfyTestImage(width, height)); err != nil {
		t.Fatalf("encode image: %v", err)
	}
	return encoded.Bytes()
}

func comfyTestImage(width, height int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 64, A: 255})
		}
	}
	return img
}
