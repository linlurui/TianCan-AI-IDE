package turncap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/memdb"
)

// countBlobFiles counts the blob files on disk (refs are released by deleting
// the row and the file together).
func countBlobFiles(t *testing.T, db *memdb.DB) int {
	t.Helper()
	root := db.BlobsDir()
	n := 0
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub, err := os.ReadDir(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		n += len(sub)
	}
	return n
}
