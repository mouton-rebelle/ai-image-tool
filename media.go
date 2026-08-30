package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Media types stored in images.media_type.
const (
	mediaTypeImage = "image"
	mediaTypeVideo = "video"
)

// Media libraries on disk. Images and videos live in separate directories so a
// directory listing stays meaningful, but both are indexed in the same table and
// mixed in the same grid.
const (
	dirImagesSFW  = "images"
	dirImagesNSFW = "images_nsfw"
	dirVideosSFW  = "videos"
	dirVideosNSFW = "videos_nsfw"
)

// videoExtensions lists the containers we index as video.
var videoExtensions = map[string]bool{
	".mp4":  true,
	".webm": true,
	".mov":  true,
	".m4v":  true,
	".mkv":  true,
}

// isVideoFilename reports whether a filename should be treated as a video.
func isVideoFilename(filename string) bool {
	return videoExtensions[strings.ToLower(filepath.Ext(filename))]
}

// mediaTypeForFilename maps a filename to the value stored in images.media_type.
func mediaTypeForFilename(filename string) string {
	if isVideoFilename(filename) {
		return mediaTypeVideo
	}
	return mediaTypeImage
}

// mediaDir returns the library directory a file belongs in.
func mediaDir(isNSFW, isVideo bool) string {
	switch {
	case isVideo && isNSFW:
		return dirVideosNSFW
	case isVideo:
		return dirVideosSFW
	case isNSFW:
		return dirImagesNSFW
	default:
		return dirImagesSFW
	}
}

// mediaDirForFilename returns the library directory for a filename, deriving the
// media type from its extension.
func mediaDirForFilename(filename string, isNSFW bool) string {
	return mediaDir(isNSFW, isVideoFilename(filename))
}

// mediaDirs returns every library directory, SFW first.
func mediaDirs() []string {
	return []string{dirImagesSFW, dirVideosSFW, dirImagesNSFW, dirVideosNSFW}
}

// mediaDirIsNSFW reports whether files in a library directory are NSFW.
func mediaDirIsNSFW(dir string) bool {
	return dir == dirImagesNSFW || dir == dirVideosNSFW
}

// ensureMediaDirs creates every library directory.
func ensureMediaDirs() error {
	for _, dir := range mediaDirs() {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return nil
}

// mediaCandidatePaths lists every place a file with this name could live, most
// likely first. Callers that move, delete or read a file walk this list so a
// file that has not been migrated to its canonical directory yet is still found.
func mediaCandidatePaths(filename string) []string {
	if filename == "" || filepath.Base(filename) != filename {
		return nil
	}

	preferred := mediaDirForFilename(filename, false)
	preferredNSFW := mediaDirForFilename(filename, true)

	paths := []string{
		filepath.Join(preferred, filename),
		filepath.Join(preferredNSFW, filename),
	}
	for _, dir := range mediaDirs() {
		path := filepath.Join(dir, filename)
		if path != paths[0] && path != paths[1] {
			paths = append(paths, path)
		}
	}
	return paths
}

// findMediaPath returns the first existing path for a filename across every
// library directory.
func findMediaPath(filename string) (string, bool) {
	for _, path := range mediaCandidatePaths(filename) {
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	return "", false
}

// thumbnailFilename returns the poster filename for a media file. Videos get a
// JPEG poster frame, so their thumbnail cannot keep the video extension.
func thumbnailFilename(filename string) string {
	if isVideoFilename(filename) {
		return strings.TrimSuffix(filename, filepath.Ext(filename)) + ".jpg"
	}
	return filename
}

// thumbnailPathFor returns the on-disk thumbnail path for a media file.
func thumbnailPathFor(filename string) string {
	return filepath.Join("thumbnails", thumbnailFilename(filename))
}

// sniffVideoExtension reads a file's magic bytes and reports the extension its
// container should carry. The extension on disk cannot be trusted: Civitai
// serves some of its videos from URLs ending in .jpg, and a video saved under
// an image extension is served with the wrong content type and refuses to play.
func sniffVideoExtension(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()

	header := make([]byte, 12)
	if _, err := io.ReadFull(file, header); err != nil {
		return "", false
	}

	// ISO base media (MP4 / MOV): "ftyp" box at offset 4, brand right after.
	if string(header[4:8]) == "ftyp" {
		if string(header[8:12]) == "qt  " {
			return ".mov", true
		}
		return ".mp4", true
	}

	// Matroska / WebM: EBML header.
	if header[0] == 0x1A && header[1] == 0x45 && header[2] == 0xDF && header[3] == 0xA3 {
		return ".webm", true
	}

	return "", false
}
