# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Deno TypeScript application that fetches image metadata from the Civitai API, downloads images, and extracts cleaned positive and negative prompt pairs. It includes advanced prompt cleaning features like LoRA removal, excluded word filtering, and smart image downloading with resume capability.

## Technology Stack

### Data Collection (Deno)
- **Runtime**: Deno (TypeScript)
- **API**: Civitai REST API v1
- **Output**: Cleaned prompts in plain text format + downloaded images

### Web Interface (Go)
- **Runtime**: Go 1.21+
- **Database**: SQLite3
- **Web Framework**: Gorilla Mux
- **Frontend**: HTMX with vanilla CSS + Masonry.js
- **Image Processing**: Go imaging libraries (resize, EXIF)
- **Layout**: Responsive masonry grid with infinite scroll

## Development Commands

### Deno Application (Data Collection)
```bash
# Run the application (basic)
deno run --allow-net --allow-write --allow-env --allow-read main.ts

# Run with custom configuration
CIVITAI_USERNAME=username CIVITAI_SORT="Most Recent" deno run --allow-net --allow-write --allow-env --allow-read main.ts

# Type check
deno check main.ts

# Format code
deno fmt

# Lint code
deno lint
```

### Go Web Server
```bash
# Install dependencies
go mod tidy

# Run the web server (development with auto-restart)
air

# Run the web server (manual)
go run main.go

# Build for production
go build -o ai-generated-image-viewer *.go

# Import images from Civitai API
CIVITAI_USERNAME=username ./ai-generated-image-viewer -import-civitai

# Development: Clear images and LoRAs tables (preserves models)
# Option 1: Using shell script
./clear_images.sh

# Option 2: Using application flag
./ai-generated-image-viewer -clear-images

# Access web interface
open http://localhost:8081
```

## Architecture

The project consists of two main components:

### 1. Data Collection Layer (Deno/TypeScript)
Single-file architecture with TypeScript interfaces:

- `main.ts`: Main application entry point containing:
  - Configuration management via environment variables
  - Type definitions for API responses and image data
  - API fetching logic with error handling and pagination
  - Data processing and deduplication
  - Prompt cleaning and filtering functionality
  - Image download with ID-based naming
- Output files:
  - `prompts_sfw.txt`: Text file with unique, cleaned SFW prompt pairs (positive|||negative format)
  - `prompts_nsfw.txt`: Text file with unique, cleaned NSFW prompt pairs (positive|||negative format)
  - `images/`: Directory containing downloaded SFW images with ID-based filenames
  - `images_nsfw/`: Directory containing downloaded NSFW images with ID-based filenames
- Supporting files:
  - `excluded_words.txt`: Comma-separated list of words to exclude from prompts

### 2. Web Interface Layer (Go)
HTTP server with SQLite backend:

- `main.go`: Web server application containing:
  - SQLite database initialization and schema with NSFW support
  - EXIF metadata extraction with multiline prompt parsing
  - Thumbnail generation (400x600px max, maintain aspect ratio)
  - HTTP handlers for web interface and API endpoints
  - HTMX-powered infinite scroll with responsive masonry layout
- Database:
  - `images.db`: SQLite database storing image metadata
  - Tables: `images` with full metadata including prompts, generation parameters, NSFW classification
- Generated files:
  - `thumbnails/`: Auto-generated image thumbnails (400x600px max)
  - `go.mod`: Go module dependencies
- Templates:
  - `templates/layout.html`: Main layout with lightbox and masonry functionality
  - `templates/image-grid.html`: Responsive grid template with page separation
- Static files:
  - `static/styles.css`: Responsive CSS with masonry layout

## Environment Variables

- `CIVITAI_TOKEN`: API token for Civitai API authentication (optional but recommended for higher rate limits)
- `CIVITAI_USERNAME`: Target username to fetch images from (default: "moutonrebelle")
- `CIVITAI_SORT`: Sort order for images (default: "Most Reactions", options: "Most Recent", "Most Reactions", "Most Comments", "Most Liked")
- `CIVITAI_PERIOD`: Time period filter (default: "AllTime", options: "AllTime", "Year", "Month", "Week", "Day")
- `CIVITAI_NSFW`: Include NSFW content (default: "true", set to "false" to exclude)

## Key Implementation Details

- **Pagination**: Web interface uses 300 items per page with infinite scroll
- **Data Collection**: Fetches all available images (100 items per API page)
- **Data Deduplication**: Prompt files are deduplicated after all images are processed on startup
- **Error Handling**: Comprehensive error handling with meaningful messages
- **Rate Limiting**: Built-in delays between requests to respect API limits
- **Type Safety**: Full TypeScript interfaces for API responses
- **NSFW Segregation**: Separate directories and database classification for NSFW/SFW content
- **Multiline Prompts**: Advanced parsing to extract complete multiline prompts from EXIF data
- **Prompt Cleaning**: Removes LoRA tags and excluded words
- **Metadata Extraction**: Captures generation parameters, statistics, and user data
- **Smart Image Downloads**: Downloads images with ID-based naming and skip logic for resuming

## API Integration

The application integrates with Civitai's REST API v1:
- **Base Endpoint**: `https://civitai.com/api/v1/images`
- **Authentication**: Bearer token or query parameter
- **Parameters**: Supports filtering by username, sort order, time period, NSFW content
- **Pagination**: Automatic handling via `nextPage` metadata
- **Response Fields**: Comprehensive image metadata including dimensions, stats, generation parameters, positive and negative prompts

## Prompt Cleaning Features

1. **LoRA Removal**: Automatically removes LoRA tags in format `<lora:name:weight>` using regex from both positive and negative prompts
2. **Excluded Words**: Filters out words listed in `excluded_words.txt` (comma-separated) from both prompt types
3. **Text Normalization**: Cleans whitespace, removes newlines, and deduplicates prompt pairs
4. **Prompt Pairing**: Combines positive and negative prompts using `|||` separator
5. **Quality Control**: Filters out pairs where positive prompt is empty after cleaning (negative can be empty)
6. **Database Integration**: Prompts are written to files when images are inserted into the database (both local and Civitai images)
7. **Startup Deduplication**: After all images are processed on startup, prompt files are deduplicated to ensure uniqueness

## Image Download Features

1. **ID-Based Naming**: Images saved as `{image_id}.{extension}` (e.g., `12345.jpeg`)
2. **Resume Capability**: Skips already downloaded images when script is re-run
3. **Auto Directory Creation**: Creates `images/` directory automatically
4. **File Extension Detection**: Automatically detects proper file extension from URL
5. **Download Progress**: Shows real-time download and skip status
6. **Error Handling**: Graceful handling of download failures with detailed logging

## Go Web Server Features

### Database Schema
- **Images Table**: Stores comprehensive metadata extracted from EXIF data
  - ID, filename, dimensions (width/height)
  - AI generation parameters (model, prompt, negative prompt, steps, CFG scale, sampler, scheduler, seed)
  - NSFW classification and thumbnail path
  - Creation timestamp and indexes on model and prompt fields for fast searching
  - Note: Prompts are cleaned of LoRA tags before storage
- **LoRAs Table**: Stores LoRA information extracted from prompts
  - ID, image_id (foreign key), name, weight, creation timestamp
  - Indexes on image_id and name for efficient lookups
- **Models Table**: Stores model information from Civitai API
  - ID, hash, name, version_name, type, NSFW flag, description, base_model, creation timestamp

### EXIF Metadata Extraction
- **Multi-field parsing**: Checks UserComment, ImageDescription, Software, Artist, Copyright fields
- **Unicode text cleaning**: Handles space-separated and null-separated Unicode encoding
- **Multiline prompt support**: Extracts complete multiline prompts instead of just first line
- **AI parameter detection**: Automatically parses:
  - Positive and negative prompts (multiline support)
  - Generation steps, CFG scale, sampler type, scheduler
  - Model name and random seed
  - Custom parameter formats

### Web Interface
- **Responsive masonry layout**: 4/3/2/1 columns based on screen size using Masonry.js
- **Infinite scroll pagination**: 300 items per page with separate grids to prevent reordering
- **Real-time search**: HTMX-powered search with 500ms debounce (model and prompt fields only)
- **NSFW filtering**: Toggle between All/SFW/NSFW with Ctrl+D shortcut
- **Lightbox viewer**: Full-screen image viewing with arrow navigation and metadata display
- **Metadata display**: Model, steps, CFG, sampler, scheduler, seed with copy functionality
- **Seed copying**: Dedicated copy button for seed values in lightbox
- **Prompt copying**: Copy complete prompts including negative prompts

### API Endpoints
- `/`: Main web interface with HTMX integration
- `/api/images`: Paginated image listing (JSON/HTML hybrid)
- `/search`: Search functionality across model and prompt fields
- `/images/*`: Static file serving for full-resolution images
- `/thumbnails/*`: Static file serving for generated thumbnails

### Performance Features
- **Automatic thumbnail generation**: Creates 400x600px max thumbnails with Lanczos3 resampling
- **Skip logic**: Processes only new images, skips already-indexed files
- **Efficient pagination**: 300 items per page with LIMIT/OFFSET queries
- **Search optimization**: LIKE queries with indexed fields (model and prompt only)
- **Responsive layout**: Masonry.js with debounced resize handling
- **Image loading**: Wait for all images to load before initializing masonry layout
- **Page separation**: Each page maintains its own masonry grid to prevent reordering