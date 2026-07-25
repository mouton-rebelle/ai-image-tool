package main

import (
	"database/sql"
	"strings"
	"testing"
)

func TestGetModelStatsFiltersByImageCategory(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE models (
			id INTEGER PRIMARY KEY,
			name TEXT,
			version_name TEXT
		);
		CREATE TABLE images (
			id INTEGER PRIMARY KEY,
			model_id INTEGER,
			is_nsfw BOOLEAN NOT NULL
		);
		INSERT INTO models (id, name, version_name) VALUES
			(1, 'Mixed', 'v1'),
			(2, 'SFW only', ''),
			(3, 'NSFW only', 'v2'),
			(4, 'Unused', '');
		INSERT INTO images (id, model_id, is_nsfw) VALUES
			(1, 1, 0),
			(2, 1, 0),
			(3, 1, 0),
			(4, 1, 1),
			(5, 2, 0),
			(6, 2, 0),
			(7, 3, 1),
			(8, 3, 1),
			(9, 3, 1);
	`); err != nil {
		t.Fatalf("seed test database: %v", err)
	}

	app := &App{db: db}

	tests := []struct {
		name   string
		filter string
		want   map[int]int
		others int
	}{
		{name: "all", filter: "all", want: map[int]int{1: 4, 3: 3}, others: 2},
		{name: "sfw", filter: "sfw", want: map[int]int{1: 3}, others: 2},
		{name: "nsfw", filter: "nsfw", want: map[int]int{3: 3}, others: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats, othersCount, err := app.getModelStats(tt.filter)
			if err != nil {
				t.Fatalf("getModelStats(%q): %v", tt.filter, err)
			}
			if othersCount != tt.others {
				t.Errorf("Others count = %d, want %d", othersCount, tt.others)
			}

			got := make(map[int]int, len(stats))
			for _, stat := range stats {
				got[stat.ID] = stat.ImageCount
			}

			if len(got) != len(tt.want) {
				t.Fatalf("got %d models (%v), want %d (%v)", len(got), got, len(tt.want), tt.want)
			}
			for modelID, wantCount := range tt.want {
				if got[modelID] != wantCount {
					t.Errorf("model %d count = %d, want %d", modelID, got[modelID], wantCount)
				}
			}
		})
	}

	t.Run("OTHERS filter uses category-specific counts", func(t *testing.T) {
		expectedCounts := map[string]int{
			"all":  2,
			"sfw":  2,
			"nsfw": 1,
		}

		for filter, wantCount := range expectedCounts {
			condition, args := modelFilterCondition("OTHERS", filter)
			var gotCount int
			whereConditions := []string{condition}
			if categoryCondition := nsfwFilterCondition(filter); categoryCondition != "" {
				whereConditions = append(whereConditions, categoryCondition)
			}
			query := "SELECT COUNT(*) FROM images i WHERE " + strings.Join(whereConditions, " AND ")
			if err := db.QueryRow(query, args...).Scan(&gotCount); err != nil {
				t.Fatalf("query OTHERS for %q: %v", filter, err)
			}
			if gotCount != wantCount {
				t.Errorf("OTHERS count for %q = %d, want %d", filter, gotCount, wantCount)
			}
		}
	})
}
