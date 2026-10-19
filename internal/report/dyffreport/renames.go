package dyffreport

import (
	"strings"

	"github.com/sholdee/drydock/internal/diff"
)

// Item is one unit of dyff output: a single resource result, or a removed and
// an added result that may be one generated resource whose name hash changed.
type Item struct {
	results []diff.Result
}

// Items groups results into dyff items, pairing a removed and an added
// Kustomize-generated ConfigMap that differ only by name hash. dyff detects a
// rename only when both documents are in one comparison; per-result runs
// report the whole ConfigMap as removed and added.
//
// Results are paired only inside one Application source, for one group, kind
// and namespace, and only when exactly one removed and one added result share
// a name base. Only ConfigMaps are paired: Kustomize hashes generated
// ConfigMap and Secret names, but the diff engine redacts each side of a Secret
// separately, so Secret values never look alike, and renaming any other kind is
// a real delete and create that must stay visible. A pair is only a candidate;
// rendering keeps it together only when dyff reports a rename and otherwise
// renders the two results separately. Results are not modified, and a pair
// takes the position of its first result.
func Items(results []diff.Result) []Item {
	type bucketKey struct {
		parent                diff.Parent
		group, kind, ns, base string
	}
	type bucket struct{ removed, added []int }
	buckets := make(map[bucketKey]*bucket)
	for i, result := range results {
		if result.Change != diff.ChangeRemoved && result.Change != diff.ChangeAdded ||
			result.Resource.Group != "" || result.Resource.Kind != "ConfigMap" {
			continue
		}
		base, ok := generatedNameBase(result.Resource.Name)
		if !ok {
			continue
		}
		key := bucketKey{result.Parent, result.Resource.Group, result.Resource.Kind, result.Resource.Namespace, base}
		b := buckets[key]
		if b == nil {
			b = &bucket{}
			buckets[key] = b
		}
		if result.Change == diff.ChangeRemoved {
			b.removed = append(b.removed, i)
		} else {
			b.added = append(b.added, i)
		}
	}

	partner := make(map[int]int)
	for _, b := range buckets {
		if len(b.removed) == 1 && len(b.added) == 1 {
			partner[b.removed[0]] = b.added[0]
			partner[b.added[0]] = b.removed[0]
		}
	}

	items := make([]Item, 0, len(results))
	for i, result := range results {
		other, paired := partner[i]
		switch {
		case !paired:
			items = append(items, Item{results: []diff.Result{result}})
		case other > i:
			items = append(items, Item{results: []diff.Result{result, results[other]}})
		}
	}
	return items
}

// kustomizeHashAlphabet holds the characters Kustomize's hasher can emit: the
// first ten hex digits of a SHA-256 with 0, 1, 3, a and e replaced by g, h, k,
// m and t.
const kustomizeHashAlphabet = "2456789bcdfghkmt"

const kustomizeHashLength = 10

// generatedNameBase returns the name without its Kustomize hash suffix.
func generatedNameBase(name string) (string, bool) {
	cut := len(name) - kustomizeHashLength - 1
	if cut < 1 || name[cut] != '-' {
		return "", false
	}
	for _, r := range name[cut+1:] {
		if !strings.ContainsRune(kustomizeHashAlphabet, r) {
			return "", false
		}
	}
	return name[:cut], true
}
