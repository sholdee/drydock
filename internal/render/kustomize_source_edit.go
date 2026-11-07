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
	// and the command changes nothing, and it does not write the
	// kustomization file.
	help bool
	// flags holds the value of each flag that the arguments set.
	flags map[string]bool
}

// parseKustomizeEditArgs reads the arguments that Argo CD passes to a
// `kustomize edit` command as the kustomize CLI reads them (spf13/pflag
// v1.0.10 under cobra v1.10.2, flag.go:980-1160). An argument that starts
// with "-" and has more characters is a flag: "--" ends the flags, an
// unknown flag fails the command, and an argument that starts with
// "-test." is dropped. In a group of shorthand flags such as "-ftest.x",
// pflag reads "-f" and then drops the rest, which starts with "test.".
// Every flag of these commands is a boolean flag.
// Argo CD passes most option values as arguments without a "--" before
// them, so a value that starts with "-" is read as a flag.
func parseKustomizeEditArgs(command string, args []string, flags []kustomizeEditFlag) (kustomizeEditCommandLine, error) {
	line := kustomizeEditCommandLine{flags: map[string]bool{}}
	set := func(flag kustomizeEditFlag, value string) error {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			name := "--" + flag.name
			if flag.shorthand != 0 {
				name = "-" + string(flag.shorthand) + ", " + name
			}
			return fmt.Errorf("kustomize edit %s: invalid argument %q for %q flag: %w", command, value, name, err)
		}
		line.flags[flag.name] = parsed
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
// before an image it is renamed to. written reports whether the command
// writes the kustomization file: it does not when the help flag is set.
func editSetKustomizeImages(kustomization *types.Kustomization, args []string) (written bool, err error) {
	line, err := parseKustomizeEditArgs("set image", args, kustomizeEditCommonFlags)
	if err != nil || line.help {
		return false, err
	}
	if len(line.args) == 0 {
		return false, errors.New("kustomize edit set image: no image specified")
	}
	byName := make(map[string]types.Image, len(line.args))
	for _, arg := range line.args {
		image, err := parseKustomizeEditImage(arg)
		if err != nil {
			return false, fmt.Errorf("kustomize edit set image: %w", err)
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
	return true, nil
}

// parseKustomizeEditImage mirrors parse in commands/edit/set/setimage.go:
// 222-276. Without "=", the argument names the image and sets its tag or
// digest. With "=", the text before it names the image and the text after
// it is the new name with an optional tag and digest. An empty name before
// "=" counts as no "=", so "=nginx:1.2" sets the tag of nginx. An argument
// that changes nothing, such as a bare name, fails.
func parseKustomizeEditImage(arg string) (types.Image, error) {
	key, value, hasKey := strings.Cut(arg, "=")
	if !hasKey {
		key, value = "", arg
	}
	name, tag, digest := kustomizeutil.SplitImageName(value)
	if name == arg {
		return types.Image{}, errKustomizeEditImageInvalid
	}
	image := types.Image{NewTag: tag, Digest: digest}
	if key == "" {
		image.Name = name
	} else {
		image.Name = key
		image.NewName = name
	}
	return image, nil
}

// editSetKustomizeReplicas mirrors `kustomize edit set replicas <args>`
// (commands/edit/set/setreplicas.go:56-128). Each argument is
// "<name>=<count>"; the last argument for a name wins, an argument replaces
// the replica of the same name, and every replica is sorted by name, one
// per name. written reports whether the command writes the kustomization
// file.
func editSetKustomizeReplicas(kustomization *types.Kustomization, args []string) (written bool, err error) {
	line, err := parseKustomizeEditArgs("set replicas", args, kustomizeEditCommonFlags)
	if err != nil || line.help {
		return false, err
	}
	if len(line.args) == 0 {
		return false, errors.New("kustomize edit set replicas: no replicas specified")
	}
	byName := make(map[string]types.Replica, len(line.args))
	for _, arg := range line.args {
		parts := strings.Split(arg, "=")
		if len(parts) != 2 {
			return false, errors.New("kustomize edit set replicas: invalid format of replica, use the following format: <name>=<count>")
		}
		count, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return false, errors.New("kustomize edit set replicas: invalid format of replica, use the following format: <name>=<count>")
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
	return true, nil
}

// kustomizeEditMetadataArgs returns the arguments Argo CD passes to `kustomize
// edit add label` or `kustomize edit add annotation`: the flags of options,
// then one "<key>:<value>" per entry (util/kustomize/kustomize.go:233-262,
// and mapToEditAddArgs at 105-123 for kustomize v3.8.5 and later).
func kustomizeEditMetadataArgs(values map[string]string, options kustomizeEditMetadataOptions) []string {
	var args []string
	if options.force {
		args = append(args, "--force")
	}
	if options.withoutSelector {
		args = append(args, "--without-selector")
	}
	if options.includeTemplates {
		args = append(args, "--include-templates")
	}
	entries := make([]string, 0, len(values))
	for key, value := range values {
		entries = append(entries, key+":"+value)
	}
	// Argo CD passes them in Go map order. Sort them, so that an error
	// that more than one entry can cause names the same entry every time.
	sort.Strings(entries)
	return append(args, entries...)
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
// the same. --include-templates without --without-selector fails. The
// flags come from the arguments, as kustomize reads them, so a label key
// such as "-ftest.x" sets --force. written reports whether the command
// writes the kustomization file.
func editAddKustomizeLabels(kustomization *types.Kustomization, labels map[string]string, options kustomizeEditMetadataOptions) (written bool, err error) {
	line, err := parseKustomizeEditArgs("add label", kustomizeEditMetadataArgs(labels, options), kustomizeEditLabelFlags)
	if err != nil || line.help {
		return false, err
	}
	if len(line.args) == 0 {
		return false, errors.New("kustomize edit add label: must specify label")
	}
	force, withoutSelector, includeTemplates := line.flags["force"], line.flags["without-selector"], line.flags["include-templates"]
	if !withoutSelector && includeTemplates {
		return false, errors.New("kustomize edit add label: --without-selector flag must be specified for --include-templates to work")
	}
	parsed, err := parseKustomizeEditMetadata("label", line.args)
	if err != nil {
		return false, fmt.Errorf("kustomize edit add label: %w", err)
	}
	if !withoutSelector {
		if kustomization.CommonLabels == nil { //nolint:staticcheck // Argo CD adds source labels to commonLabels.
			kustomization.CommonLabels = map[string]string{} //nolint:staticcheck // Argo CD adds source labels to commonLabels.
		}
		return true, writeKustomizeEditMetadata(kustomization.CommonLabels, parsed, "label", force) //nolint:staticcheck // Argo CD adds source labels to commonLabels.
	}
	index := slices.IndexFunc(kustomization.Labels, func(label types.Label) bool {
		return !label.IncludeSelectors && label.IncludeTemplates == includeTemplates
	})
	if index < 0 {
		kustomization.Labels = append(kustomization.Labels, types.Label{
			Pairs:            map[string]string{},
			IncludeTemplates: includeTemplates,
		})
		index = len(kustomization.Labels) - 1
	}
	if kustomization.Labels[index].Pairs == nil {
		// kustomize writes into the nil map and panics
		// (commands/edit/add/addmetadata.go:163), so the command fails.
		return false, fmt.Errorf("kustomize edit add label: labels entry %d has no pairs; kustomize v5.8.3 fails on it with a nil map panic", index)
	}
	return true, writeKustomizeEditMetadata(kustomization.Labels[index].Pairs, parsed, "label", force)
}

// editAddKustomizeAnnotations mirrors `kustomize edit add annotation`
// (commands/edit/add/addmetadata.go:136-142): the annotations go to
// commonAnnotations, and a key already there fails unless --force is set.
// written reports whether the command writes the kustomization file.
func editAddKustomizeAnnotations(kustomization *types.Kustomization, annotations map[string]string, force bool) (written bool, err error) {
	line, err := parseKustomizeEditArgs("add annotation", kustomizeEditMetadataArgs(annotations, kustomizeEditMetadataOptions{force: force}), kustomizeEditAnnotationFlags)
	if err != nil || line.help {
		return false, err
	}
	if len(line.args) == 0 {
		return false, errors.New("kustomize edit add annotation: must specify annotation")
	}
	parsed, err := parseKustomizeEditMetadata("annotation", line.args)
	if err != nil {
		return false, fmt.Errorf("kustomize edit add annotation: %w", err)
	}
	if kustomization.CommonAnnotations == nil {
		kustomization.CommonAnnotations = map[string]string{}
	}
	return true, writeKustomizeEditMetadata(kustomization.CommonAnnotations, parsed, "annotation", line.flags["force"])
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
// "has no match", with the text of loader.New: an empty pattern, an
// absolute pattern, a path with an ambiguous element, or a path that is no
// directory. drydock never reads absolute paths, so an absolute pattern
// always fails as one without a match.
//
// The globs read only inside boundaryRoot, through an os.Root, which
// follows symlinks that stay inside it. A directory they cannot list
// there, such as one outside the repository or one reached through a
// symlink that leaves it, adds no matches, as a directory that cannot be
// read adds none to filepath.Glob. A path whose text enters .git adds no
// matches either. Each path is resolved element by element (resolve), so
// a ".." after a symlink goes up from its target. A symlink that leads out
// of the repository or into .git fails with drydock's symlink error before
// anything there is listed or read. Any other symlink is followed: when it
// leads to no match it adds nothing, and a match through it fails with the
// symlink error, because drydock rejects a component path that passes
// through a symlink. Argo CD follows every such symlink.
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
			return nil, fmt.Errorf("kustomize edit add component: %s has no match: new root '%s' cannot be absolute", pattern, pattern)
		}
		if root == nil {
			opened, err := os.OpenRoot(boundaryRoot)
			if err != nil {
				return nil, fmt.Errorf("open repository root for kustomize components: %w", err)
			}
			root = opened
		}
		glob := newKustomizeComponentGlob(root, boundaryRoot, dir)
		matches, err := glob.glob(pattern, 0)
		var symlinkErr kustomizeComponentSymlinkError
		switch {
		case errors.As(err, &symlinkErr):
			return nil, fmt.Errorf("kustomize components %q: %w", pattern, symlinkErr)
		case err != nil:
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
	// realRoot is boundaryRoot with its symlinks resolved. An absolute
	// symlink target can name the repository root either way.
	realRoot string
	dir      string
	// beforeOpen, when set, runs between the resolve and the open of a
	// directory that globDir lists. Tests use it to change the tree there.
	beforeOpen func(rel string)
}

func newKustomizeComponentGlob(root *os.Root, boundaryRoot, dir string) kustomizeComponentGlob {
	boundaryRoot = filepath.Clean(boundaryRoot)
	realRoot, err := filepath.EvalSymlinks(boundaryRoot)
	if err != nil {
		realRoot = boundaryRoot
	}
	return kustomizeComponentGlob{root: root, boundaryRoot: boundaryRoot, realRoot: realRoot, dir: filepath.Clean(dir)}
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
		exists, err := g.exists(pattern)
		if err != nil || !exists {
			return nil, err
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
// match pattern, in lexical order. A directory it cannot read adds none. A
// directory reached through a symlink is listed only when the symlink stays
// in the repository and out of .git; a match there fails with the symlink
// error, and no match adds nothing.
func (g kustomizeComponentGlob) globDir(dirPath, pattern string, matches []string) ([]string, error) {
	resolved, err := g.resolve(dirPath, true)
	if err != nil || !resolved.ok {
		return matches, err
	}
	if g.beforeOpen != nil {
		g.beforeOpen(resolved.rel)
	}
	file, err := g.root.Open(resolved.rel)
	if err != nil {
		return matches, nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return matches, nil
	}
	// The os.Root follows symlinks inside the repository, .git too. So
	// make sure that the open directory is still the one that resolve
	// found, without a symlink on the way, before it is listed.
	if err := g.sameResolvedDir(dirPath, resolved, info); err != nil {
		return matches, err
	}
	names, _ := file.Readdirnames(-1)
	slices.Sort(names)
	found := len(matches)
	for _, name := range names {
		matched, err := filepath.Match(pattern, name)
		if err != nil {
			return matches, err
		}
		if matched {
			matches = append(matches, filepath.Join(dirPath, name))
		}
	}
	if resolved.symlink != "" && len(matches) > found {
		return matches, g.symlinkError(dirPath, resolved.symlink)
	}
	return matches, nil
}

// sameResolvedDir fails with the symlink error when dirPath no longer
// resolves to the directory that was opened, info.
func (g kustomizeComponentGlob) sameResolvedDir(dirPath string, resolved kustomizeResolvedPath, info os.FileInfo) error {
	again, err := g.resolve(dirPath, true)
	if err != nil {
		return err
	}
	current, statErr := g.root.Lstat(again.rel)
	if !again.ok || again.rel != resolved.rel || again.symlink != resolved.symlink || statErr != nil || !os.SameFile(info, current) {
		clean := filepath.Clean(filepath.Join(g.dir, dirPath))
		return kustomizeComponentSymlinkError{err: fmt.Errorf("path %q changed while drydock read it", clean)}
	}
	return nil
}

// confirmDir mirrors the local branch of loader.New: path must have no
// element that holds ".." next to other characters, and it must be a
// directory. Like filesys.ConfirmDir, it cleans the path as text first and
// then follows its symlinks, so "file.yaml/../a" names the folder a.
func (g kustomizeComponentGlob) confirmDir(path string) error {
	if path == "" {
		return errors.New("new root cannot be empty")
	}
	for element := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if element != "" && element != "." && element != ".." && strings.Contains(element, "..") {
			return fmt.Errorf("path %q is ambiguous: resolves to %q after normalization, which is likely not the intended target; check for YAML indentation errors in your kustomization file", path, filepath.Clean(path))
		}
	}
	rel, err := filepath.Rel(g.boundaryRoot, filepath.Clean(filepath.Join(g.dir, path)))
	if err != nil || pathsafety.RelEscapes(rel) {
		return errors.New("must build at directory: not a valid directory")
	}
	resolved, err := g.resolveElements(strings.Split(rel, string(filepath.Separator)), path, true)
	if err != nil {
		return err
	}
	if !resolved.ok {
		return errors.New("must build at directory: not a valid directory")
	}
	info, err := g.root.Stat(resolved.rel)
	if err != nil || !info.IsDir() {
		return errors.New("must build at directory: not a valid directory")
	}
	return nil
}

// exists reports whether Lstat reads path. Lstat fails on an empty path.
// A path that exists through a symlink fails with the symlink error.
func (g kustomizeComponentGlob) exists(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	resolved, err := g.resolve(path, false)
	if err != nil || !resolved.ok || !g.lstatReads(resolved.rel) {
		return false, err
	}
	if resolved.symlink != "" {
		return false, g.symlinkError(path, resolved.symlink)
	}
	return true, nil
}

// lstatReads reports whether Lstat reads rel.
func (g kustomizeComponentGlob) lstatReads(rel string) bool {
	_, err := g.root.Lstat(rel)
	return err == nil
}

// kustomizeComponentSymlinkError is the error of a component glob that
// must read a path through a symlink.
type kustomizeComponentSymlinkError struct {
	err error
}

func (e kustomizeComponentSymlinkError) Error() string {
	return e.err.Error()
}

// symlinkError is drydock's symlink error for path, relative to dir, which
// passes through the symlink element.
func (g kustomizeComponentGlob) symlinkError(path, element string) error {
	clean := filepath.Clean(filepath.Join(g.dir, path))
	return kustomizeComponentSymlinkError{err: fmt.Errorf("path %q includes symlink component %q", clean, element)}
}

// kustomizeResolvedPath is a glob path resolved inside the repository.
type kustomizeResolvedPath struct {
	// rel is the path relative to the repository root, with every symlink
	// resolved.
	rel string
	// ok is false for a path that leaves the repository or enters .git
	// without a symlink: the glob adds no matches there.
	ok bool
	// symlink is the first symlink element that the path follows, or "".
	symlink string
}

// kustomizeSymlinkHopLimit is the number of symlinks a path may follow, as
// the OS limits it.
const kustomizeSymlinkHopLimit = 40

// kustomizePathWalk is the state of one resolve call.
type kustomizePathWalk struct {
	queue    []string
	stack    []string
	resolved kustomizeResolvedPath
	hops     int
}

// resolve resolves path, relative to dir, element by element as the OS
// does: each symlink is read and followed before the next element, so a
// ".." after a symlink goes up from its target. followLast reports whether
// a symlink in the last element is followed, as Stat follows it and Lstat
// does not. resolve reads only symlinks: their names and targets. It never
// looks up anything in .git. When a symlink leads out of the repository or
// into .git, resolve fails with the symlink error before anything there is
// listed or read.
func (g kustomizeComponentGlob) resolve(path string, followLast bool) (kustomizeResolvedPath, error) {
	dirRel, ok := g.dirRel()
	if !ok {
		return kustomizeResolvedPath{}, nil
	}
	return g.resolveElements(append(strings.Split(dirRel, string(filepath.Separator)), strings.Split(path, string(filepath.Separator))...), path, followLast)
}

// resolveElements resolves the path elements, from the repository root,
// as resolve does. path names the path in errors.
func (g kustomizeComponentGlob) resolveElements(elements []string, path string, followLast bool) (kustomizeResolvedPath, error) {
	walk := &kustomizePathWalk{queue: elements}
	for len(walk.queue) > 0 {
		element := walk.queue[0]
		walk.queue = walk.queue[1:]
		done, err := g.resolveElement(walk, path, element, followLast)
		if done || err != nil {
			return kustomizeResolvedPath{}, err
		}
	}
	walk.resolved.rel = filepath.Join(walk.stack...)
	if walk.resolved.rel == "" {
		walk.resolved.rel = "."
	}
	walk.resolved.ok = true
	return walk.resolved, nil
}

// resolveElement resolves one element of path in walk. done reports that
// the path adds no matches.
func (g kustomizeComponentGlob) resolveElement(walk *kustomizePathWalk, path, element string, followLast bool) (done bool, err error) {
	switch {
	case element == "" || element == ".":
		return false, nil
	case element == "..":
		if len(walk.stack) == 0 {
			return true, g.afterSymlink(path, walk.resolved)
		}
		walk.stack = walk.stack[:len(walk.stack)-1]
		return false, nil
	case strings.EqualFold(element, ".git"):
		// Nothing in .git is looked up: a path whose text enters it adds
		// no matches, and a symlink that leads into it fails.
		return true, g.afterSymlink(path, walk.resolved)
	}
	candidate := filepath.Join(append(append([]string(nil), walk.stack...), element)...)
	target, follow, done := g.lookupElement(candidate, len(walk.queue) != 0, followLast)
	if done {
		return true, nil
	}
	if !follow {
		walk.stack = append(walk.stack, element)
		return false, nil
	}
	walk.hops++
	if walk.hops > kustomizeSymlinkHopLimit {
		return true, nil
	}
	if walk.resolved.symlink == "" {
		walk.resolved.symlink = element
	}
	targetElements := strings.Split(target, string(filepath.Separator))
	if filepath.IsAbs(target) {
		rest, inside := g.underRoot(targetElements)
		if !inside {
			return true, g.afterSymlink(path, walk.resolved)
		}
		walk.stack = nil
		targetElements = rest
	}
	walk.queue = append(targetElements, walk.queue...)
	return false, nil
}

// lookupElement looks up candidate, a path element with more elements
// after it when more is set, as the OS does. done reports that the path
// ends with no match: a missing element (ENOENT) or a file (ENOTDIR) with
// more elements after it, so a ".." after it does not go back, or a
// symlink that cannot be read. follow reports a symlink to follow, with
// its target.
func (g kustomizeComponentGlob) lookupElement(candidate string, more, followLast bool) (target string, follow, done bool) {
	info, statErr := g.root.Lstat(candidate)
	switch {
	case statErr != nil:
		return "", false, more
	case info.Mode()&os.ModeSymlink == 0 || (!more && !followLast):
		return "", false, more && !info.IsDir() && info.Mode()&os.ModeSymlink == 0
	}
	target, readErr := g.root.Readlink(candidate)
	if readErr != nil || target == "" {
		return "", false, true
	}
	return target, true, false
}

// underRoot returns the elements of an absolute symlink target after the
// repository root, as written or with its symlinks resolved. The elements
// are compared as written, without cleaning, so a ".." stays for the walk
// to resolve after any symlink before it. inside is false for a target
// that does not start with the repository root: it leads outside.
func (g kustomizeComponentGlob) underRoot(target []string) (rest []string, inside bool) {
	target = slices.DeleteFunc(slices.Clone(target), func(element string) bool { return element == "" || element == "." })
	for _, root := range []string{g.boundaryRoot, g.realRoot} {
		rootElements := slices.DeleteFunc(strings.Split(root, string(filepath.Separator)), func(element string) bool { return element == "" })
		if len(target) >= len(rootElements) && slices.Equal(target[:len(rootElements)], rootElements) {
			return target[len(rootElements):], true
		}
	}
	return nil, false
}

// dirRel returns dir relative to the repository root.
func (g kustomizeComponentGlob) dirRel() (string, bool) {
	rel, err := filepath.Rel(g.boundaryRoot, g.dir)
	if err != nil || pathsafety.RelEscapes(rel) {
		return "", false
	}
	return rel, true
}

// afterSymlink is the outcome of a path that leaves the repository or
// enters .git: no matches without a symlink, the symlink error after one.
func (g kustomizeComponentGlob) afterSymlink(path string, resolved kustomizeResolvedPath) error {
	if resolved.symlink != "" {
		return g.symlinkError(path, resolved.symlink)
	}
	return nil
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
