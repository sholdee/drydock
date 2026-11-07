package chart

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	"sigs.k8s.io/yaml"
)

// helmIndexFile is repo.IndexFile (helm v4.3.1 pkg/repo/v1/index.go:81-93).
// It has every field of helm's type, because helm decodes a YAML index
// strictly and fails on a field it does not know.
type helmIndexFile struct {
	ServerInfo  map[string]any               `json:"serverInfo,omitempty"`
	APIVersion  string                       `json:"apiVersion"`
	Generated   time.Time                    `json:"generated"`
	Entries     map[string]helmChartVersions `json:"entries"`
	PublicKeys  []string                     `json:"publicKeys,omitempty"`
	Annotations map[string]string            `json:"annotations,omitempty"`
}

// helmChartVersion is repo.ChartVersion (helm v4.3.1
// pkg/repo/v1/index.go:285-311), with every field of helm's type.
type helmChartVersion struct {
	*chartv2.Metadata
	URLs                    []string  `json:"urls"`
	Created                 time.Time `json:"created"`
	Removed                 bool      `json:"removed,omitempty"`
	Digest                  string    `json:"digest,omitempty"`
	ChecksumDeprecated      string    `json:"checksum,omitempty"`
	EngineDeprecated        string    `json:"engine,omitempty"`
	TillerVersionDeprecated string    `json:"tillerVersion,omitempty"`
	URLDeprecated           string    `json:"url,omitempty"`
}

// helmChartVersions is repo.ChartVersions (helm v4.3.1
// pkg/repo/v1/index.go:58-79), with its sort order.
type helmChartVersions []*helmChartVersion

func (c helmChartVersions) Len() int      { return len(c) }
func (c helmChartVersions) Swap(i, j int) { c[i], c[j] = c[j], c[i] }

// Less is ChartVersions.Less: a version that does not parse sorts first, so
// it ends last in the reverse sort.
func (c helmChartVersions) Less(a, b int) bool {
	i, err := semver.NewVersion(c[a].Version)
	if err != nil {
		return true
	}
	j, err := semver.NewVersion(c[b].Version)
	if err != nil {
		return false
	}
	return i.LessThan(j)
}

// errHelmNoAPIVersion and errHelmEmptyIndex are repo.ErrNoAPIVersion and
// repo.ErrEmptyIndexYaml.
var (
	errHelmNoAPIVersion = errors.New("no API version specified")
	errHelmEmptyIndex   = errors.New("empty index.yaml file")
)

// loadHelmIndex reads an index as helm loads it (loadIndex and
// jsonOrYamlUnmarshal, helm v4.3.1 pkg/repo/v1/index.go:359-415): it
// decodes a JSON index leniently and a YAML index strictly, so a YAML
// index with a field that helm does not know fails, drops each entry that
// fails Metadata.Validate, such as one whose version is not semver, sorts
// each chart's entries with the same unstable sort.Sort(sort.Reverse(...))
// call on the same input order, and fails an index without an apiVersion.
// So drydock picks the same entry as helm among semver-equal versions such
// as 1.2.0, v1.2.0, and 1.2.0+a.
func loadHelmIndex(data []byte) (map[string][]repositoryChartVersion, error) {
	if len(data) == 0 {
		return nil, errHelmEmptyIndex
	}
	var index helmIndexFile
	var err error
	if json.Valid(data) {
		err = json.Unmarshal(data, &index)
	} else {
		err = yaml.UnmarshalStrict(data, &index)
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string][]repositoryChartVersion, len(index.Entries))
	for name, cvs := range index.Entries {
		for idx, v := range slices.Backward(cvs) {
			if v == nil {
				cvs = append(cvs[:idx], cvs[idx+1:]...)
				continue
			}
			if v.Metadata == nil {
				v.Metadata = &chartv2.Metadata{}
			}
			if v.APIVersion == "" {
				v.APIVersion = chartv2.APIVersionV1
			}
			if err := v.Validate(); ignoreSkippableChartValidationError(err) != nil {
				cvs = append(cvs[:idx], cvs[idx+1:]...)
			}
		}
		sort.Sort(sort.Reverse(cvs))
		entries := make([]repositoryChartVersion, 0, len(cvs))
		for _, v := range cvs {
			entries = append(entries, repositoryChartVersion{Version: v.Version, URLs: v.URLs})
		}
		out[name] = entries
	}
	if index.APIVersion == "" {
		return nil, errHelmNoAPIVersion
	}
	return out, nil
}

// ignoreSkippableChartValidationError is the helper of the same name (helm
// v4.3.1 pkg/repo/v1/index.go:423-435): an index entry with two
// dependencies of one name stays.
func ignoreSkippableChartValidationError(err error) error {
	var verr chartv2.ValidationError
	if !errors.As(err, &verr) {
		return err
	}
	if strings.HasPrefix(verr.Error(), "validation: more than one dependency with name or alias") {
		return nil
	}
	return err
}
