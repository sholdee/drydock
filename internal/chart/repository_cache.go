package chart

import (
	"fmt"
	"os"
	"path/filepath"
)

// publishRename is os.Rename, which a test replaces to act as another
// writer.
var publishRename = os.Rename

// publishChartCache moves the fetched entry tmpKeyDir to keyDir, and moves
// an existing entry aside first. When the move fails because another writer
// made keyDir ready at the same time, it keeps that entry: both hold the
// same chart version. It accepts the entry only when keyDir is not the old
// entry that it moved back. leaf is the chart directory in the entry.
func publishChartCache(keyDir, tmpKeyDir, leaf string) error {
	parent := filepath.Dir(keyDir)
	base := filepath.Base(keyDir)
	var backupDir string
	if _, err := os.Lstat(keyDir); err == nil {
		var err error
		backupDir, err = os.MkdirTemp(parent, "."+base+".old-")
		if err != nil {
			return fmt.Errorf("create chart cache backup %s: %w", parent, err)
		}
		if err := os.Remove(backupDir); err != nil {
			return fmt.Errorf("prepare chart cache backup %s: %w", backupDir, err)
		}
		if err := publishRename(keyDir, backupDir); err != nil {
			return fmt.Errorf("backup chart cache %s: %w", keyDir, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat chart cache %s: %w", keyDir, err)
	}

	if err := publishRename(tmpKeyDir, keyDir); err != nil {
		if backupDir != "" {
			if restoreErr := publishRename(backupDir, keyDir); restoreErr == nil {
				// keyDir holds the old entry again, not a fetched one.
				return fmt.Errorf("publish chart cache %s: %w", keyDir, err)
			}
		}
		// Another writer owns keyDir now.
		if chartDirReady(filepath.Join(keyDir, leaf)) {
			if backupDir != "" {
				_ = os.RemoveAll(backupDir)
			}
			return nil
		}
		return fmt.Errorf("publish chart cache %s: %w", keyDir, err)
	}
	if backupDir != "" {
		if err := os.RemoveAll(backupDir); err != nil {
			return fmt.Errorf("remove old chart cache %s: %w", backupDir, err)
		}
	}
	return nil
}
