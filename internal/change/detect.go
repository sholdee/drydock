package change

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/sholdee/drydock/internal/streamcmp"
)

func Detect(baseRoot, currentRoot string) ([]string, error) {
	paths := make(map[string]struct{})
	if err := collectFiles(baseRoot, paths); err != nil {
		return nil, err
	}
	if err := collectFiles(currentRoot, paths); err != nil {
		return nil, err
	}

	relPaths := make([]string, 0, len(paths))
	for rel := range paths {
		relPaths = append(relPaths, rel)
	}
	sort.Strings(relPaths)

	changed := make([]bool, len(relPaths))
	errs := make([]error, len(relPaths))
	workers := detectParallelism(len(relPaths))
	jobs := make(chan int)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := range jobs {
				changed[i], errs[i] = pathChanged(baseRoot, currentRoot, relPaths[i])
			}
		}()
	}
	for i := range relPaths {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	var out []string
	for i, rel := range relPaths {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if changed[i] {
			out = append(out, rel)
		}
	}
	return out, nil
}

const maxDetectWorkers = 16

func detectParallelism(pathCount int) int {
	if pathCount <= 1 {
		return 1
	}
	workers := max(runtime.GOMAXPROCS(0), 1)
	workers = min(workers, maxDetectWorkers)
	return min(workers, pathCount)
}

func pathChanged(baseRoot, currentRoot, rel string) (bool, error) {
	basePath := filepath.Join(baseRoot, filepath.FromSlash(rel))
	currentPath := filepath.Join(currentRoot, filepath.FromSlash(rel))
	baseInfo, baseErr := os.Lstat(basePath)
	if baseErr != nil && !errors.Is(baseErr, fs.ErrNotExist) {
		return false, baseErr
	}
	currentInfo, currentErr := os.Lstat(currentPath)
	if currentErr != nil && !errors.Is(currentErr, fs.ErrNotExist) {
		return false, currentErr
	}
	if baseErr != nil || currentErr != nil {
		return true, nil
	}
	return pathsChanged(basePath, currentPath, baseInfo, currentInfo)
}

func collectFiles(root string, paths map[string]struct{}) error {
	return filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		paths[filepath.ToSlash(rel)] = struct{}{}
		return nil
	})
}

func isRegularFile(info fs.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode()&fs.ModeSymlink == 0
}

func isSymlink(info fs.FileInfo) bool {
	return info.Mode()&fs.ModeSymlink != 0
}

func pathsChanged(basePath, currentPath string, baseInfo, currentInfo fs.FileInfo) (bool, error) {
	switch {
	case isSymlink(baseInfo) && isSymlink(currentInfo):
		return symlinksChanged(basePath, currentPath)
	case !isRegularFile(baseInfo) || !isRegularFile(currentInfo):
		return true, nil
	default:
		return regularFilesChanged(basePath, currentPath, baseInfo, currentInfo)
	}
}

func symlinksChanged(basePath, currentPath string) (bool, error) {
	baseTarget, err := os.Readlink(basePath)
	if err != nil {
		return false, err
	}
	currentTarget, err := os.Readlink(currentPath)
	if err != nil {
		return false, err
	}
	return baseTarget != currentTarget, nil
}

func regularFilesChanged(basePath, currentPath string, baseInfo, currentInfo fs.FileInfo) (bool, error) {
	if baseInfo.Size() != currentInfo.Size() {
		return true, nil
	}
	base, err := os.Open(basePath)
	if err != nil {
		return false, err
	}
	defer base.Close()
	current, err := os.Open(currentPath)
	if err != nil {
		return false, err
	}
	defer current.Close()
	equal, err := streamcmp.Equal(base, current)
	if err != nil {
		return false, err
	}
	return !equal, nil
}
