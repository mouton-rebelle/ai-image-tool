package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Video probing and poster extraction shell out to ffprobe/ffmpeg. They are the
// only external dependencies of the viewer; without them videos are skipped
// during indexing instead of breaking the import.
const (
	ffprobeTimeout = 30 * time.Second
	ffmpegTimeout  = 2 * time.Minute

	// Grab the poster a little into the clip: the very first frame of a
	// generated video is often a dark fade-in.
	posterSeekSeconds = 0.5

	posterMaxWidth  = 400
	posterMaxHeight = 600
	posterQuality   = 4 // ffmpeg -q:v scale, 2 (best) to 31 (worst)
)

// VideoInfo holds everything we read from a video file in a single probe.
type VideoInfo struct {
	Width    int
	Height   int
	Duration float64
	HasAudio bool
	// Tags holds the container-level metadata. ComfyUI writes its API prompt
	// graph to "prompt" and the editor graph to "workflow".
	Tags map[string]string
}

type ffprobeOutput struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Duration  string `json:"duration"`
	} `json:"streams"`
	Format struct {
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
}

var (
	ffmpegCheckOnce sync.Once
	ffmpegPath      string
	ffprobePath     string
)

func resolveFFmpegTools() {
	ffmpegCheckOnce.Do(func() {
		if path, err := exec.LookPath("ffmpeg"); err == nil {
			ffmpegPath = path
		}
		if path, err := exec.LookPath("ffprobe"); err == nil {
			ffprobePath = path
		}
		if ffmpegPath == "" || ffprobePath == "" {
			log.Printf("Warning: ffmpeg/ffprobe not found in PATH; videos will not be indexed. Install ffmpeg to enable video support.")
		}
	})
}

// videoToolsAvailable reports whether ffmpeg and ffprobe can be used.
func videoToolsAvailable() bool {
	resolveFFmpegTools()
	return ffmpegPath != "" && ffprobePath != ""
}

// probeVideo reads dimensions, duration, audio presence and container metadata
// from a video file.
func probeVideo(videoPath string) (*VideoInfo, error) {
	resolveFFmpegTools()
	if ffprobePath == "" {
		return nil, fmt.Errorf("ffprobe is not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), ffprobeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		videoPath,
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("probe %s: %w", filepath.Base(videoPath), err)
	}

	var probe ffprobeOutput
	if err := json.Unmarshal(output, &probe); err != nil {
		return nil, fmt.Errorf("decode ffprobe output for %s: %w", filepath.Base(videoPath), err)
	}

	info := &VideoInfo{Tags: make(map[string]string, len(probe.Format.Tags))}
	for key, value := range probe.Format.Tags {
		info.Tags[strings.ToLower(key)] = value
	}

	for _, stream := range probe.Streams {
		switch stream.CodecType {
		case "video":
			if info.Width == 0 && stream.Width > 0 {
				info.Width = stream.Width
				info.Height = stream.Height
			}
			if info.Duration == 0 {
				info.Duration = parseSeconds(stream.Duration)
			}
		case "audio":
			info.HasAudio = true
		}
	}

	if formatDuration := parseSeconds(probe.Format.Duration); formatDuration > 0 {
		info.Duration = formatDuration
	}

	if info.Width == 0 || info.Height == 0 {
		return nil, fmt.Errorf("no video stream found in %s", filepath.Base(videoPath))
	}

	return info, nil
}

func parseSeconds(value string) float64 {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return seconds
}

// extractVideoPoster writes a JPEG poster frame for a video. The frame is scaled
// to the same envelope as image thumbnails so both fit the masonry grid.
func extractVideoPoster(videoPath, posterPath string, duration float64) error {
	resolveFFmpegTools()
	if ffmpegPath == "" {
		return fmt.Errorf("ffmpeg is not installed")
	}

	if err := os.MkdirAll(filepath.Dir(posterPath), 0755); err != nil {
		return fmt.Errorf("create thumbnail directory: %w", err)
	}

	seek := posterSeekSeconds
	if duration > 0 && duration <= posterSeekSeconds {
		seek = 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), ffmpegTimeout)
	defer cancel()

	// Write to a temporary file so a failed or interrupted run never leaves a
	// truncated poster that the "already generated" check would accept.
	tempPath := fmt.Sprintf("%s.part-%d", posterPath, time.Now().UnixNano())
	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-nostdin",
		"-v", "error",
		"-ss", strconv.FormatFloat(seek, 'f', 3, 64),
		"-i", videoPath,
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", posterMaxWidth, posterMaxHeight),
		"-q:v", strconv.Itoa(posterQuality),
		// The temporary name has no usable extension, so the format has to be
		// spelled out instead of inferred from it.
		"-f", "image2",
		"-vcodec", "mjpeg",
		"-y",
		tempPath,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("extract poster for %s: %w: %s", filepath.Base(videoPath), err, strings.TrimSpace(string(output)))
	}

	if info, err := os.Stat(tempPath); err != nil || info.Size() == 0 {
		os.Remove(tempPath)
		return fmt.Errorf("ffmpeg produced no poster for %s", filepath.Base(videoPath))
	}

	if err := os.Rename(tempPath, posterPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("finalize poster for %s: %w", filepath.Base(videoPath), err)
	}

	return nil
}

// extractVideoFrameJPEG returns a JPEG frame from a video, used to feed the
// prompt generator an image it can look at.
func extractVideoFrameJPEG(videoPath string) ([]byte, error) {
	resolveFFmpegTools()
	if ffmpegPath == "" {
		return nil, fmt.Errorf("ffmpeg is not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), ffmpegTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-nostdin",
		"-v", "error",
		"-ss", strconv.FormatFloat(posterSeekSeconds, 'f', 3, 64),
		"-i", videoPath,
		"-frames:v", "1",
		"-f", "image2",
		"-vcodec", "mjpeg",
		"-q:v", "2",
		"pipe:1",
	)

	frame, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("extract frame for %s: %w", filepath.Base(videoPath), err)
	}
	if len(frame) == 0 {
		return nil, fmt.Errorf("ffmpeg produced no frame for %s", filepath.Base(videoPath))
	}

	return frame, nil
}

// Hosted video generators do not write a workflow; they leave an opaque
// provenance blob instead. It carries nothing we can read, but its shape
// identifies the tool, which is enough to label and filter the clip.
const (
	// Grok Imagine writes "Signature: <base64>" into the MP4 comment tag.
	grokSignaturePrefix = "Signature: "
	// Kling writes an encrypted protobuf whose first field is this key name.
	klingMetadataMarker = "security.kling.meta_encrypt"
)

// identifyVideoGenerator names the tool that produced a video from the traces
// it leaves in the container metadata. It returns an empty string when nothing
// recognisable is there.
func identifyVideoGenerator(tags map[string]string) string {
	if strings.HasPrefix(strings.TrimSpace(tags["comment"]), grokSignaturePrefix) {
		return "Grok Imagine"
	}

	for _, key := range []string{"metadata0", "metadata1"} {
		if strings.Contains(decodeBase64Prefix(tags[key], 64), klingMetadataMarker) {
			return "Kling"
		}
	}

	return ""
}

// decodeBase64Prefix decodes the leading whole base64 groups of a value. These
// provenance blobs come back unpadded and sometimes truncated, so decoding the
// whole string fails; the marker we look for sits at the very start anyway.
func decodeBase64Prefix(value string, maxChars int) string {
	if value == "" {
		return ""
	}
	if len(value) > maxChars {
		value = value[:maxChars]
	}
	value = value[:len(value)/4*4]

	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return ""
	}
	return string(decoded)
}
