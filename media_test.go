package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMediaClassification(t *testing.T) {
	tests := []struct {
		filename  string
		isVideo   bool
		sfwDir    string
		nsfwDir   string
		thumbnail string
	}{
		{"12345.jpeg", false, dirImagesSFW, dirImagesNSFW, "12345.jpeg"},
		{"12345.PNG", false, dirImagesSFW, dirImagesNSFW, "12345.PNG"},
		{"12345.mp4", true, dirVideosSFW, dirVideosNSFW, "12345.jpg"},
		{"clip.WEBM", true, dirVideosSFW, dirVideosNSFW, "clip.jpg"},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			if got := isVideoFilename(tt.filename); got != tt.isVideo {
				t.Errorf("isVideoFilename = %t, want %t", got, tt.isVideo)
			}
			if got := mediaDirForFilename(tt.filename, false); got != tt.sfwDir {
				t.Errorf("mediaDirForFilename(sfw) = %q, want %q", got, tt.sfwDir)
			}
			if got := mediaDirForFilename(tt.filename, true); got != tt.nsfwDir {
				t.Errorf("mediaDirForFilename(nsfw) = %q, want %q", got, tt.nsfwDir)
			}
			if got := thumbnailFilename(tt.filename); got != tt.thumbnail {
				t.Errorf("thumbnailFilename = %q, want %q", got, tt.thumbnail)
			}
		})
	}
}

func TestMediaCandidatePathsRejectsTraversal(t *testing.T) {
	if paths := mediaCandidatePaths("../secret.png"); paths != nil {
		t.Errorf("mediaCandidatePaths returned %v for a traversal attempt", paths)
	}
}

func TestMediaCandidatePathsAreUnique(t *testing.T) {
	paths := mediaCandidatePaths("12345.mp4")
	if len(paths) != len(mediaDirs()) {
		t.Fatalf("got %d candidate paths (%v), want %d", len(paths), paths, len(mediaDirs()))
	}
	if paths[0] != filepath.Join(dirVideosSFW, "12345.mp4") {
		t.Errorf("first candidate = %q, want the SFW video library", paths[0])
	}

	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if seen[path] {
			t.Errorf("duplicate candidate path %q", path)
		}
		seen[path] = true
	}
}

// mp4Header is the first 12 bytes of an ISO base media file: a box length, the
// "ftyp" type, then the brand.
func mp4Header(brand string) []byte {
	header := append([]byte{0, 0, 0, 0x18}, []byte("ftyp")...)
	return append(header, []byte(brand)...)
}

func TestSniffVideoExtension(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name    string
		content []byte
		want    string
		isVideo bool
	}{
		{"mp4", mp4Header("isom"), ".mp4", true},
		{"quicktime", mp4Header("qt  "), ".mov", true},
		{"webm", []byte{0x1A, 0x45, 0xDF, 0xA3, 0, 0, 0, 0, 0, 0, 0, 0}, ".webm", true},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0, 0, 0, 0, 0}, "", false},
		{"too short", []byte{0xFF, 0xD8}, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, tt.content, 0644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}

			extension, isVideo := sniffVideoExtension(path)
			if isVideo != tt.isVideo || extension != tt.want {
				t.Errorf("sniffVideoExtension = (%q, %t), want (%q, %t)", extension, isVideo, tt.want, tt.isVideo)
			}
		})
	}
}

func TestNormalizeMediaLibraries(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := ensureMediaDirs(); err != nil {
		t.Fatalf("ensureMediaDirs: %v", err)
	}

	write := func(path string, content []byte) {
		t.Helper()
		if err := os.WriteFile(path, content, 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	// A video with the right extension, one downloaded under an image
	// extension, a real image, and a duplicate of a clip we already have.
	write(filepath.Join(dirImagesSFW, "1.mp4"), mp4Header("isom"))
	write(filepath.Join(dirImagesSFW, "2.jpg"), mp4Header("isom"))
	write(filepath.Join(dirImagesSFW, "3.jpg"), []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0, 0, 0, 0, 0})
	write(filepath.Join(dirImagesNSFW, "4.mp4"), mp4Header("isom"))
	write(filepath.Join(dirVideosNSFW, "4.mp4"), mp4Header("isom"))

	moved, duplicates := normalizeMediaLibraries()
	if moved != 2 {
		t.Errorf("moved = %d, want 2", moved)
	}
	if duplicates != 1 {
		t.Errorf("duplicates = %d, want 1", duplicates)
	}

	for _, path := range []string{
		filepath.Join(dirVideosSFW, "1.mp4"),
		filepath.Join(dirVideosSFW, "2.mp4"), // renamed to match its container
		filepath.Join(dirImagesSFW, "3.jpg"), // a real image, left alone
		filepath.Join("temp", "4.mp4"),       // duplicate parked for review
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to exist: %v", path, err)
		}
	}

	if _, err := os.Stat(filepath.Join(dirImagesSFW, "2.jpg")); !os.IsNotExist(err) {
		t.Errorf("expected the mislabeled video to leave the image library")
	}
}

func TestNormalizeMediaFilter(t *testing.T) {
	tests := map[string]string{
		"":       "all",
		"all":    "all",
		"image":  mediaTypeImage,
		"images": mediaTypeImage,
		"VIDEO":  mediaTypeVideo,
		"videos": mediaTypeVideo,
		"nope":   "all",
	}

	for input, want := range tests {
		if got := normalizeMediaFilter(input); got != want {
			t.Errorf("normalizeMediaFilter(%q) = %q, want %q", input, got, want)
		}
	}

	if got := mediaFilterCondition("all"); got != "" {
		t.Errorf("mediaFilterCondition(all) = %q, want an empty condition", got)
	}
	if got := mediaFilterCondition("video"); got != "i.media_type = 'video'" {
		t.Errorf("mediaFilterCondition(video) = %q", got)
	}
}

func TestIdentifyVideoGenerator(t *testing.T) {
	tests := []struct {
		name string
		tags map[string]string
		want string
	}{
		{
			name: "grok signature",
			tags: map[string]string{"comment": "Signature: mcRamXUOTWuqGDKbRTZfErL7GopT/Wao"},
			want: "Grok Imagine",
		},
		{
			// The blob comes back unpadded and truncated, so only the leading
			// groups can be decoded.
			name: "kling encrypted metadata",
			tags: map[string]string{"metadata0": "ChtzZWN1cml0eS5rbGluZy5tZXRhX2VuY3J5cHQS8AElIIeGqutLGGLhVpOq"},
			want: "Kling",
		},
		{
			name: "comfyui workflow",
			tags: map[string]string{"comment": `{"prompt": "{}"}`},
			want: "",
		},
		{name: "nothing", tags: map[string]string{}, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := identifyVideoGenerator(tt.tags); got != tt.want {
				t.Errorf("identifyVideoGenerator = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestImageMetadataDisplayHelpers(t *testing.T) {
	video := ImageMetadata{MediaType: mediaTypeVideo, Width: 1152, Height: 1792, Duration: 5.166}
	if !video.IsVideo() {
		t.Error("IsVideo() = false for a video")
	}
	if got := video.AspectRatio(); got != "1152 / 1792" {
		t.Errorf("AspectRatio() = %q", got)
	}
	if got := video.DurationLabel(); got != "0:05" {
		t.Errorf("DurationLabel() = %q, want 0:05", got)
	}

	long := ImageMetadata{MediaType: mediaTypeVideo, Duration: 125}
	if got := long.DurationLabel(); got != "2:05" {
		t.Errorf("DurationLabel() = %q, want 2:05", got)
	}

	image := ImageMetadata{MediaType: mediaTypeImage, Duration: 12}
	if image.IsVideo() || image.DurationLabel() != "" {
		t.Error("an image should not report as a video or carry a duration")
	}
	if got := (ImageMetadata{}).AspectRatio(); got != "" {
		t.Errorf("AspectRatio() = %q for unknown dimensions, want empty", got)
	}
}
