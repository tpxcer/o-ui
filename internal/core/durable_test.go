package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicJSONCommitResult(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "selection.json")
	written, err := atomicJSON(file, map[string]string{"selected": DefaultVersion})
	if err != nil || !written {
		t.Fatal("private document and directory were not committed", err)
	}
	data, err := os.ReadFile(file)
	var value map[string]string
	info, statErr := os.Stat(file)
	if err != nil || statErr != nil || json.Unmarshal(data, &value) != nil || value["selected"] != DefaultVersion || info.Mode().Perm() != 0600 {
		t.Fatal("committed document invalid")
	}
	for _, item := range []struct {
		name, path string
		value      any
	}{
		{"encoding", file, make(chan bool)},
		{"missing-parent", filepath.Join(root, "missing", "selection.json"), value},
		{"rename-to-directory", root, value},
	} {
		t.Run(item.name, func(t *testing.T) {
			if written, err := atomicJSON(item.path, item.value); err == nil || written {
				t.Fatal("uncommitted write reported as committed")
			}
			current, err := os.ReadFile(file)
			if err != nil || string(current) != string(data) {
				t.Fatal("rejected write changed original document")
			}
		})
	}
}
