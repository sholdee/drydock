package cache

import "path/filepath"

func GitEntryPath(root, key string) string {
	return filepath.Join(root, key)
}

func ChartKindRoot(root, kind string) string {
	return filepath.Join(root, kind)
}

// ChartVersionsRecordDir holds the recorded version lists of the charts of
// one repository kind, which resolve version ranges under --offline. Its
// name is not a cache key, so the entry listers skip it.
func ChartVersionsRecordDir(root, kind string) string {
	return filepath.Join(ChartKindRoot(root, kind), "versions")
}

func ChartEntryPath(root, kind, key string) string {
	return filepath.Join(ChartKindRoot(root, kind), key)
}

func RemoteEntryPath(root, key string) string {
	return filepath.Join(root, key)
}

func OCIEntryPath(root, key string) string {
	return filepath.Join(root, key)
}

func RemoteHTTPFilePath(root, key string) string {
	return filepath.Join(RemoteEntryPath(root, key), "resource.yaml")
}

func RemoteGitRepoPath(root, key string) string {
	return filepath.Join(RemoteEntryPath(root, key), "repo")
}
