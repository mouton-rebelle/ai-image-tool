# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Deno TypeScript application that fetches image metadata from the Civitai API and extracts prompts to a text file. The application specifically targets images from a user named "moutonrebelle" and saves all unique prompts to `prompts.txt`.

## Technology Stack

- **Runtime**: Deno (TypeScript)
- **API**: Civitai REST API v1
- **Output**: Plain text file

## Development Commands

```bash
# Run the application
deno run --allow-net --allow-write --allow-env --allow-read main.ts

# Type check
deno check main.ts

# Format code
deno fmt

# Lint code
deno lint
```

## Architecture

The application follows a simple single-file architecture:

- `main.ts`: Main application entry point containing:
  - API fetching logic with pagination
  - Prompt extraction and deduplication
  - File writing operations
- `prompts.txt`: Output file containing extracted prompts (one per line)

## Environment Variables

- `CIVITAI_TOKEN`: Required API token for Civitai API authentication

## Key Implementation Details

- Uses pagination to fetch all available images (100 items per page)
- Deduplicates prompts using a Set
- Replaces newlines in prompts with periods for consistent formatting
- Handles API errors gracefully with try-catch blocks
- Uses Deno's native file operations for output

## API Integration

The application integrates with Civitai's REST API:
- Endpoint: `https://civitai.com/api/v1/images`
- Authentication: Token-based via query parameter
- Pagination: Uses `nextPage` from response metadata