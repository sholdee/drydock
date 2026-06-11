package app

import "sync"

type discoveryPathVerdict struct {
	matches bool
	err     error
}

// pathMayContainDiscoveryObjectsCached memoizes per source root for one
// discoverRepository invocation. Source trees are static during discovery;
// renders write only to temporary workspaces.
func pathMayContainDiscoveryObjectsCached(memo *sync.Map, root string) (bool, error) {
	if memo == nil {
		return pathMayContainDiscoveryObjects(root)
	}
	if cached, ok := memo.Load(root); ok {
		if verdict, ok := cached.(discoveryPathVerdict); ok {
			return verdict.matches, verdict.err
		}
	}
	matches, err := pathMayContainDiscoveryObjects(root)
	memo.Store(root, discoveryPathVerdict{matches: matches, err: err})
	return matches, err
}
