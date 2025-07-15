#!/bin/bash

# Clear images and loras tables for development testing
# This preserves the models table to avoid re-fetching from Civitai API

echo "Clearing images and loras tables..."

sqlite3 images.db << 'EOF'
BEGIN TRANSACTION;

-- Clear loras table (must be done first due to foreign key constraint)
DELETE FROM loras;

-- Clear images table
DELETE FROM images;

-- Reset auto-increment sequences
DELETE FROM sqlite_sequence WHERE name IN ('loras');

-- Show remaining counts
SELECT 'Models remaining: ' || COUNT(*) FROM models;
SELECT 'Images remaining: ' || COUNT(*) FROM images;
SELECT 'LoRAs remaining: ' || COUNT(*) FROM loras;

COMMIT;
EOF

echo "Done! Tables cleared. Models table preserved."
echo "You can now run the application to re-process images."