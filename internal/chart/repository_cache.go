package chart

import (
	"fmt"
	"os"
	"path/filepath"
)

func publishChartCache(keyDir, tmpKeyDir string) error {
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
		if err := os.Rename(keyDir, backupDir); err != nil {
			return fmt.Errorf("backup chart cache %s: %w", keyDir, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat chart cache %s: %w", keyDir, err)
	}

	if err := os.Rename(tmpKeyDir, keyDir); err != nil {
		if backupDir != "" {
			_ = os.Rename(backupDir, keyDir)
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
