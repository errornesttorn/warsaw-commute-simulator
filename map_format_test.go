package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRoadMaskPathAllowsMissingSingleFile(t *testing.T) {
	base := t.TempDir()
	got, err := resolveRoadMaskPath(base, "road_masks/new.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(base, "road_masks", "new.json")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestResolveRoadMaskPathRejectsMultipleLegacyMatches(t *testing.T) {
	base := t.TempDir()
	maskDir := filepath.Join(base, "road_masks")
	if err := os.MkdirAll(maskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.json", "b.json"} {
		if err := os.WriteFile(filepath.Join(maskDir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, err := resolveRoadMaskPath(base, "", []string{"road_masks/*.json"})
	if err == nil || !strings.Contains(err.Error(), "resolved to 2 files") {
		t.Fatalf("error = %v, want multiple-match error", err)
	}
}
