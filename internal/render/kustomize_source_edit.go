package render

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/sholdee/drydock/internal/pathsafety"
	kustomizeutil "sigs.k8s.io/kustomize/api/pkg/util"
	"sigs.k8s.io/kustomize/api/types"
)

// The Argo CD v3.6.1 repo-server applies most spec.source.kustomize options
// by running `kustomize edit` commands on its copy of the source before
// `kustomize build` (util/kustomize/kustomize.go:174-401). This file ports
// those commands from kustomize v5.8.3 (sigs.k8s.io/kustomize/kustomize/v5,
// commands/edit), the kustomize that Argo CD v3.6.1 ships, so drydock merges
// each option the way the command does, with the same errors.

// kustomizeEditFlag is a boolean flag of a `kustomize edit` command.
type kustomizeEditFlag struct {
	name      string
	shorthand byte
}

// kustomizeEditCommonFlags are the flags of every `kustomize edit` command:
// the cobra help flag and the root --stack-trace flag.
var kustomizeEditCommonFlags = []kustomizeEditFlag{{name: "help", shorthand: 'h'}, {name: "stack-trace"}}

// kustomizeEditLabelFlags and kustomizeEditAnnotationFlags are the flags of
// `kustomize edit add label` and `kustomize edit add annotation`
// (commands/edit/add/addmetadata.go:46-91).
var (
	kustomizeEditLabelFlags = append([]kustomizeEditFlag{
		{name: "force", shorthand: 'f'}, {name: "without-selector"}, {name: "include-templates"},
	}, kustomizeEditCommonFlags...)
	kustomizeEditAnnotationFlags = append([]kustomizeEditFlag{{name: "force", shorthand: 'f'}}, kustomizeEditCommonFlags...)
)

// kustomizeEditCommandLine is the outcome of reading the arguments of one
// `kustomize edit` command.
type kustomizeEditCommandLine struct {
	// args are the positional arguments.
	args []string
	// help reports that the help flag is set: cobra then prints the help
	// and the command changes nothing.
	help bool
}

// parseKustomizeEditArgs reads the arguments that Argo CD passes to a
// `kustomize edit` command as the kustomize CLI reads them (spf13/pflag
// v1.0.10 under cobra v1.10.2, flag.go:980-1160). An argument that starts
// with "-" and has more characters is a flag: "--" ends the flags, an
// unknown flag fails the command, and an argument that starts with
// "-test." is dropped. Every flag of these commands is a boolean flag.
// Argo CD passes most option values as arguments without a "--" before
// them, so a value that starts with "-" is read as a flag.
func parseKustomizeEditArgs(command string, args []string, flags []kustomizeEditFlag) (kustomizeEditCommandLine, error) {
	var line kustomizeEditCommandLine
	set := func(flag kustomizeEditFlag, value string) error {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			name := "--" + flag.name
			if flag.shorthand != 0 {
				name = "-" + string(flag.shorthand) + ", " + name
			}
			return fmt.Errorf("kustomize edit %s: invalid argument %q for %q flag: %w", command, value, name, err)
		}
		if flag.name == "help" {
			line.help = parsed
		}
		return nil
	}
	for i, arg := range args {
		if len(arg) <= 1 || arg[0] != '-' {
			line.args = append(line.args, arg)
			continue
		}
		if arg == "--" {
			line.args = append(line.args, args[i+1:]...)
			break
		}
		if arg[1] == '-' {
			if err := parseKustomizeEditLongFlag(command, arg, flags, set); err != nil {
				return kustomizeEditCommandLine{}, err
			}
			continue
		}
		if err := parseKustomizeEditShortFlags(command, arg, flags, set); err != nil {
			return kustomizeEditCommandLine{}, err
		}
	}
	return line, nil
}

func parseKustomizeEditLongFlag(command, arg string, flags []kustomizeEditFlag, set func(kustomizeEditFlag, string) error) error {
	name := arg[2:]
	if name[0] == '-' || name[0] == '=' {
		return fmt.Errorf("kustomize edit %s: bad flag syntax: %s", command, arg)
	}
	name, value, hasValue := strings.Cut(name, "=")
	index := slices.IndexFunc(flags, func(flag kustomizeEditFlag) bool { return flag.name == name })
	if index < 0 {
		return fmt.Errorf("kustomize edit %s: unknown flag: --%s", command, name)
	}
	if !hasValue {
		value = "true"
	}
	return set(flags[index], value)
}

func parseKustomizeEditShortFlags(command, arg string, flags []kustomizeEditFlag, set func(kustomizeEditFlag, string) error) error {
	shorthands := arg[1:]
	for len(shorthands) > 0 {
		if strings.HasPrefix(shorthands, "test.") {
			return nil
		}
		index := slices.IndexFunc(flags, func(flag kustomizeEditFlag) bool { return flag.shorthand == shorthands[0] })
		if index < 0 {
			return fmt.Errorf("kustomize edit %s: unknown shorthand flag: %q in -%s", command, rune(shorthands[0]), shorthands)
		}
		value := "true"
		if len(shorthands) > 2 && shorthands[1] == '=' {
			value = shorthands[2:]
			shorthands = ""
		} else {
			shorthands = shorthands[1:]
		}
		if err := set(flags[index], value); err != nil {
			return err
		}
	}
	return nil
}

// kustomizeEditImagePreserve is the value that keeps the new name, new tag
// or digest of the image already in the kustomization
// (commands/edit/set/setimage.go:26).
const kustomizeEditImagePreserve = "*"

var errKustomizeEditImageInvalid = errors.New(`invalid format of image, use one of the following options:
- <image>=<newimage>:<newtag>
- <image>=<newimage>@<digest>
- <image>=<newimage>:<newtag>@<digest>
- <image>=<newimage>
- <image>:<newtag>
- <image>:<newtag>@<digest>
- <image>@<digest>`)

// editSetKustomizeImages mirrors `kustomize edit set image <args>`
// (commands/edit/set/setimage.go:106-196). The arguments replace the images
// of the same name, and the last argument for a name wins. A "*" new name,
// new tag or digest keeps the value of the image already in the
// kustomization, or clears it when there is none. Then every image is
// sorted by name, one per name, so an image the arguments add can come
// before an image it is renamed to.
func editSetKustomizeImages(kustomization *types.Kustomization, args []string) error {
	line, err := parseKustomizeEditArgs("set image", args, kustomizeEditCommonFlags)
	if err != nil || line.help {
		return err
	}
	if len(line.args) == 0 {
		return errors.New("kustomize edit set image: no image specified")
	}
	byName := make(map[string]types.Image, len(line.args))
	for _, arg := range line.args {
		image, err := parseKustomizeEditImage(arg)
		if err != nil {
			return fmt.Errorf("kustomize edit set image: %w", err)
		}
		byName[image.Name] = image
	}
	for _, existing := range kustomization.Images {
		override, ok := byName[existing.Name]
		if !ok {
			byName[existing.Name] = existing
			continue
		}
		if override.NewName == kustomizeEditImagePreserve {
			override = types.Image{Name: override.Name, NewName: existing.NewName, NewTag: override.NewTag, Digest: override.Digest}
		}
		if override.NewTag == kustomizeEditImagePreserve {
			override = types.Image{Name: override.Name, NewName: override.NewName, NewTag: existing.NewTag, Digest: override.Digest}
		}
		if override.Digest == kustomizeEditImagePreserve {
			override = types.Image{Name: override.Name, NewName: override.NewName, NewTag: override.NewTag, Digest: existing.Digest}
		}
		byName[existing.Name] = override
	}
	images := make([]types.Image, 0, len(byName))
	for _, image := range byName {
		if image.NewName == kustomizeEditImagePreserve {
			image = types.Image{Name: image.Name, NewTag: image.NewTag, Digest: image.Digest}
		}
		if image.NewTag == kustomizeEditImagePreserve {
			image = types.Image{Name: image.Name, NewName: image.NewName, Digest: image.Digest}
		}
		if image.Digest == kustomizeEditImagePreserve {
			image = types.Image{Name: image.Name, NewName: image.NewName, NewTag: image.NewTag}
		}
		images = append(images, image)
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Name < images[j].Name })
	kustomization.Images = images
	return nil
}

// parseKustomizeEditImage mirrors parse in commands/edit/set/setimage.go:
// 222-276. Without "=", the argument names the image and sets its tag or
// digest. With "=", the text before it names the image and the text after
// it is the new name with an optional tag and digest. An argument that
// changes nothing, such as a bare name, fails.
func parseKustomizeEditImage(arg string) (types.Image, error) {
	key, value, hasKey := strings.Cut(arg, "=")
	if !hasKey {
		value = arg
	}
	name, tag, digest := kustomizeutil.SplitImageName(value)
	if name == arg {
		return types.Image{}, errKustomizeEditImageInvalid
	}
	image := types.Image{NewTag: tag, Digest: digest}
	if hasKey {
		image.Name = key
		image.NewName = name
	} else {
		image.Name = name
	}
	return image, nil
}

// editSetKustomizeReplicas mirrors `kustomize edit set replicas <args>`
// (commands/edit/set/setreplicas.go:56-128). Each argument is
// "<name>=<count>"; the last argument for a name wins, an argument replaces
// the replica of the same name, and every replica is sorted by name, one
// per name.
func editSetKustomizeReplicas(kustomization *types.Kustomization, args []string) error {
	line, err := parseKustomizeEditArgs("set replicas", args, kustomizeEditCommonFlags)
	if err != nil || line.help {
		return err
	}
	if len(line.args) == 0 {
		return errors.New("kustomize edit set replicas: no replicas specified")
	}
	byName := make(map[string]types.Replica, len(line.args))
	for _, arg := range line.args {
		parts := strings.Split(arg, "=")
		if len(parts) != 2 {
			return errors.New("kustomize edit set replicas: invalid format of replica, use the following format: <name>=<count>")
		}
		count, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return errors.New("kustomize edit set replicas: invalid format of replica, use the following format: <name>=<count>")
		}
		byName[parts[0]] = types.Replica{Name: parts[0], Count: count}
	}
	for _, existing := range kustomization.Replicas {
		if _, ok := byName[existing.Name]; !ok {
			byName[existing.Name] = existing
		}
	}
	replicas := make([]types.Replica, 0, len(byName))
	for _, replica := range byName {
		replicas = append(replicas, replica)
	}
	sort.Slice(replicas, func(i, j int) bool { return replicas[i].Name < replicas[j].Name })
	kustomization.Replicas = replicas
	return nil
}

// kustomizeEditMetadataArgs returns the arguments Argo CD passes to `kustomize
// edit add label` or `kustomize edit add annotation`: one "<key>:<value>"
// per entry (mapToEditAddArgs, util/kustomize/kustomize.go:105-123, for
// kustomize v3.8.5 and later).
func kustomizeEditMetadataArgs(values map[string]string) []string {
	args := make([]string, 0, len(values))
	for key, value := range values {
		args = append(args, key+":"+value)
	}
	// Argo CD passes them in Go map order. Sort them, so that an error
	// that more than one entry can cause names the same entry every time.
	sort.Strings(args)
	return args
}

// parseKustomizeEditMetadata mirrors util.ConvertSliceToMap
// (commands/internal/util/util.go:77-112): each argument splits at its
// first ":", and a value in double quotes loses them.
func parseKustomizeEditMetadata(kind string, args []string) (map[string]string, error) {
	out := make(map[string]string, len(args))
	for _, arg := range args {
		key, value, found := strings.Cut(arg, ":")
		switch {
		case found && key == "":
			return nil, fmt.Errorf("invalid %s: '%s' (need k:v pair where v may be quoted)", kind, arg)
		case !found:
			out[arg] = ""
		default:
			if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
				value = value[1 : len(value)-1]
			}
			out[key] = value
		}
	}
	return out, nil
}

// kustomizeEditMetadataOptions are the flags Argo CD passes to `kustomize
// edit add label` (forceCommonLabels, labelWithoutSelector,
// labelIncludeTemplates) or `kustomize edit add annotation`
// (forceCommonAnnotations).
type kustomizeEditMetadataOptions struct {
	force            bool
	withoutSelector  bool
	includeTemplates bool
}

// editAddKustomizeLabels mirrors `kustomize edit add label` with the flags
// Argo CD sets (commands/edit/add/addmetadata.go:95-216). Without
// --without-selector, the labels go to commonLabels. With it, they go to
// the first labels entry that does not include selectors and includes
// templates only under --include-templates, or to a new such entry. A key
// already in that map fails unless --force is set, also when its value is
// the same. --include-templates without --without-selector fails.
func editAddKustomizeLabels(kustomization *types.Kustomization, labels map[string]string, options kustomizeEditMetadataOptions) error {
	line, err := parseKustomizeEditArgs("add label", kustomizeEditMetadataArgs(labels), kustomizeEditLabelFlags)
	if err != nil || line.help {
		return err
	}
	if len(line.args) == 0 {
		return errors.New("kustomize edit add label: must specify label")
	}
	if !options.withoutSelector && options.includeTemplates {
		return errors.New("kustomize edit add label: --without-selector flag must be specified for --include-templates to work")
	}
	parsed, err := parseKustomizeEditMetadata("label", line.args)
	if err != nil {
		return fmt.Errorf("kustomize edit add label: %w", err)
	}
	if !options.withoutSelector {
		if kustomization.CommonLabels == nil { //nolint:staticcheck // Argo CD adds source labels to commonLabels.
			kustomization.CommonLabels = map[string]string{} //nolint:staticcheck // Argo CD adds source labels to commonLabels.
		}
		return writeKustomizeEditMetadata(kustomization.CommonLabels, parsed, "label", options.force) //nolint:staticcheck // Argo CD adds source labels to commonLabels.
	}
	index := slices.IndexFunc(kustomization.Labels, func(label types.Label) bool {
		return !label.IncludeSelectors && label.IncludeTemplates == options.includeTemplates
	})
	if index < 0 {
		kustomization.Labels = append(kustomization.Labels, types.Label{
			Pairs:            map[string]string{},
			IncludeTemplates: options.includeTemplates,
		})
		index = len(kustomization.Labels) - 1
	}
	if kustomization.Labels[index].Pairs == nil {
		kustomization.Labels[index].Pairs = map[string]string{}
	}
	return writeKustomizeEditMetadata(kustomization.Labels[index].Pairs, parsed, "label", options.force)
}

// editAddKustomizeAnnotations mirrors `kustomize edit add annotation`
// (commands/edit/add/addmetadata.go:136-142): the annotations go to
// commonAnnotations, and a key already there fails unless --force is set.
func editAddKustomizeAnnotations(kustomization *types.Kustomization, annotations map[string]string, force bool) error {
	line, err := parseKustomizeEditArgs("add annotation", kustomizeEditMetadataArgs(annotations), kustomizeEditAnnotationFlags)
	if err != nil || line.help {
		return err
	}
	if len(line.args) == 0 {
		return errors.New("kustomize edit add annotation: must specify annotation")
	}
	parsed, err := parseKustomizeEditMetadata("annotation", line.args)
	if err != nil {
		return fmt.Errorf("kustomize edit add annotation: %w", err)
	}
	if kustomization.CommonAnnotations == nil {
		kustomization.CommonAnnotations = map[string]string{}
	}
	return writeKustomizeEditMetadata(kustomization.CommonAnnotations, parsed, "annotation", force)
}

// writeKustomizeEditMetadata mirrors writeToMap and writeToMapEntry
// (commands/edit/add/addmetadata.go:151-166). kustomize writes the keys in
// map order and fails at the first key already there; drydock checks them
// in sorted order, so the error names the same key every time.
func writeKustomizeEditMetadata(target, values map[string]string, kind string, force bool) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := target[key]; ok && !force {
			return fmt.Errorf("kustomize edit add %s: %s %s already in kustomization file. Use --force to override.", kind, kind, key) //nolint:staticcheck // kustomize's own error text.
		}
		target[key] = values[key]
	}
	return nil
}

// editAddKustomizeComponents mirrors `kustomize edit add component <args>`
// (commands/edit/add/addcomponent.go:48-83), run in the source directory.
// Each argument is a glob (see expandKustomizeComponentPatterns). A match
// that is the kustomization file itself, or a component the kustomization
// already lists, is not added again. dir and boundaryRoot are the source
// directory and repository root that the globs read.
func editAddKustomizeComponents(kustomization *types.Kustomization, kustomizationFileName, dir, boundaryRoot string, args []string) error {
	line, err := parseKustomizeEditArgs("add component", args, kustomizeEditCommonFlags)
	if err != nil || line.help {
		return err
	}
	if len(line.args) == 0 {
		return errors.New("kustomize edit add component: must specify a component file")
	}
	components, err := expandKustomizeComponentPatterns(dir, boundaryRoot, line.args)
	if err != nil {
		return err
	}
	for _, component := range components {
		if component == kustomizationFileName || slices.Contains(kustomization.Components, component) {
			continue
		}
		kustomization.Components = append(kustomization.Components, component)
	}
	return nil
}

// expandKustomizeComponentPatterns returns the components that `kustomize
// edit add component` reads from patterns, in order. It mirrors
// util.GlobPatternsWithLoader (commands/internal/util/util.go:36-70) with
// the on-disk Glob of kyaml v0.21.3 (filesys/fsondisk.go:91-103): each
// pattern is a filepath.Glob from dir, and a match whose base name starts
// with "." is dropped unless the pattern's base name does too.
//
// A pattern that matches nothing is kept as written when kustomize can load
// it as a root (loader.New, kustomize/api v0.21.3,
// internal/loader/fileloader.go:172-209): a remote ref, which the later
// steps fetch, or a local directory. Any other such pattern fails with
// "has no match". An absolute pattern is kept as written, because drydock
// never reads absolute paths; the later steps reject it.
//
// The globs read only inside boundaryRoot, through an os.Root. A directory
// they cannot list there, such as one outside the repository, one reached
// through a symlink that leaves it, or a .git directory, adds no matches,
// as a directory that cannot be read adds none to filepath.Glob.
func expandKustomizeComponentPatterns(dir, boundaryRoot string, patterns []string) ([]string, error) {
	var out []string
	var root *os.Root
	defer func() {
		if root != nil {
			_ = root.Close()
		}
	}()
	for _, pattern := range patterns {
		if filepath.IsAbs(pattern) {
			out = append(out, pattern)
			continue
		}
		if root == nil {
			opened, err := os.OpenRoot(boundaryRoot)
			if err != nil {
				return nil, fmt.Errorf("open repository root for kustomize components: %w", err)
			}
			root = opened
		}
		glob := kustomizeComponentGlob{root: root, boundaryRoot: filepath.Clean(boundaryRoot), dir: filepath.Clean(dir)}
		matches, err := glob.glob(pattern, 0)
		if err != nil {
			return nil, fmt.Errorf("kustomize edit add component: error checking the filesystem: %w", err)
		}
		if !strings.HasPrefix(filepath.Base(pattern), ".") {
			matches = slices.DeleteFunc(matches, func(match string) bool {
				return strings.HasPrefix(filepath.Base(match), ".")
			})
		}
		if len(matches) == 0 {
			if !isRemoteKustomizeRef(pattern) {
				if err := glob.confirmDir(pattern); err != nil {
					return nil, fmt.Errorf("kustomize edit add component: %s has no match: %w", pattern, err)
				}
			}
			out = append(out, pattern)
			continue
		}
		out = append(out, matches...)
	}
	return out, nil
}

// kustomizeComponentGlob runs filepath.Glob (Go path/filepath/match.go)
// with paths relative to dir, reading through root.
type kustomizeComponentGlob struct {
	root         *os.Root
	boundaryRoot string
	dir          string
}

// kustomizeGlobDepthLimit is the recursion limit of filepath.Glob.
const kustomizeGlobDepthLimit = 10000

func (g kustomizeComponentGlob) glob(pattern string, depth int) ([]string, error) {
	if depth == kustomizeGlobDepthLimit {
		return nil, filepath.ErrBadPattern
	}
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, err
	}
	if !kustomizeGlobHasMeta(pattern) {
		// filepath.Glob drops a path that Lstat cannot read.
		if !g.exists(pattern) {
			return nil, nil
		}
		return []string{pattern}, nil
	}
	dirPart, file := filepath.Split(pattern)
	dirPart = kustomizeCleanGlobPath(dirPart)
	if !kustomizeGlobHasMeta(dirPart) {
		return g.globDir(dirPart, file, nil)
	}
	if dirPart == pattern {
		return nil, filepath.ErrBadPattern
	}
	dirs, err := g.glob(dirPart, depth+1)
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, match := range dirs {
		matches, err = g.globDir(match, file, matches)
		if err != nil {
			return nil, err
		}
	}
	return matches, nil
}

// globDir mirrors glob in path/filepath: it adds the names in dirPath that
// match pattern, in lexical order. A directory it cannot read adds none.
func (g kustomizeComponentGlob) globDir(dirPath, pattern string, matches []string) ([]string, error) {
	rel, ok := g.rootPath(dirPath)
	if !ok {
		return matches, nil
	}
	info, err := g.root.Stat(rel)
	if err != nil || !info.IsDir() {
		return matches, nil
	}
	file, err := g.root.Open(rel)
	if err != nil {
		return matches, nil
	}
	defer func() { _ = file.Close() }()
	names, _ := file.Readdirnames(-1)
	slices.Sort(names)
	for _, name := range names {
		matched, err := filepath.Match(pattern, name)
		if err != nil {
			return matches, err
		}
		if matched {
			matches = append(matches, filepath.Join(dirPath, name))
		}
	}
	return matches, nil
}

// confirmDir mirrors the local branch of loader.New: path must have no
// element that holds ".." next to other characters, and it must be a
// directory.
func (g kustomizeComponentGlob) confirmDir(path string) error {
	for element := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if element != "" && element != "." && element != ".." && strings.Contains(element, "..") {
			return fmt.Errorf("path %q is ambiguous: resolves to %q after normalization, which is likely not the intended target; check for YAML indentation errors in your kustomization file", path, filepath.Clean(path))
		}
	}
	rel, ok := g.rootPath(path)
	if !ok {
		return errors.New("must build at directory: not a valid directory")
	}
	info, err := g.root.Stat(rel)
	if err != nil || !info.IsDir() {
		return errors.New("must build at directory: not a valid directory")
	}
	return nil
}

// exists reports whether Lstat reads path.
func (g kustomizeComponentGlob) exists(path string) bool {
	rel, ok := g.rootPath(path)
	if !ok {
		return false
	}
	_, err := g.root.Lstat(rel)
	return err == nil
}

// rootPath returns path, relative to dir, as a path relative to the
// repository root for root. The ".." elements stay in place, so the
// os.Root resolves them after any symlink, as the OS does. ok is false for
// a path that leaves the repository or enters a .git directory.
func (g kustomizeComponentGlob) rootPath(path string) (string, bool) {
	clean := filepath.Clean(filepath.Join(g.dir, path))
	rel, err := filepath.Rel(g.boundaryRoot, clean)
	if err != nil || pathsafety.RelEscapes(rel) || pathsafety.RelEntersGit(rel) {
		return "", false
	}
	dirRel, err := filepath.Rel(g.boundaryRoot, g.dir)
	if err != nil || pathsafety.RelEscapes(dirRel) {
		return "", false
	}
	if dirRel == "." {
		return path, true
	}
	return dirRel + string(filepath.Separator) + path, true
}

// kustomizeGlobHasMeta mirrors hasMeta in path/filepath on Unix.
func kustomizeGlobHasMeta(path string) bool {
	return strings.ContainsAny(path, `*?[\`)
}

// kustomizeCleanGlobPath mirrors cleanGlobPath in path/filepath on Unix.
func kustomizeCleanGlobPath(path string) string {
	switch path {
	case "":
		return "."
	case string(filepath.Separator):
		return path
	default:
		return path[:len(path)-1]
	}
}

// kustomizeComponentGlobDir returns the directory, under dir, that the
// globs of a component pattern list: its leading elements without a glob
// character. ok is false for a pattern without one, which lists nothing.
func kustomizeComponentGlobDir(dir, pattern string) (string, bool) {
	if filepath.IsAbs(pattern) || !kustomizeGlobHasMeta(pattern) {
		return "", false
	}
	elements := strings.Split(pattern, string(filepath.Separator))
	for i, element := range elements {
		if kustomizeGlobHasMeta(element) {
			return filepath.Clean(filepath.Join(dir, filepath.Join(elements[:i]...))), true
		}
	}
	return "", false
}
