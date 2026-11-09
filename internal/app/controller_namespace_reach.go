package app

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/project"
)

// namedNamespaces is what the settings candidates say about the controller
// namespace: the namespaces that the counting argocd-cmd-params-cm
// ConfigMaps name, the namespaces of other Argo CD installs, each with the
// sorted paths of its candidates, and the load keys of the settings
// ConfigMaps of those other installs, which are not settings.
type namedNamespaces struct {
	named  map[string][]string
	others map[string][]string
	other  map[string]struct{}
}

// commandParametersNamespaces applies one check to every
// argocd-cmd-params-cm: it counts only when the Argo CD that it configures
// would reconcile an Application that puts it on this cluster, or when no
// Application puts it on this cluster.
//
// A rendered one counts unless only Applications on other clusters render
// it (SettingsCandidate.RemoteOnly), or the render is another install
// (SettingsCandidate.OtherInstall, installReconciles). A committed one
// counts unless the Applications on this cluster that render it all fail
// the check (commandParametersReach), which makes it another install, or
// unless only Applications on other clusters render it. Only a candidate
// that carries a namespace names one. The settings ConfigMaps of another
// install, rendered or committed, are not settings. The result is a set, so
// the order of the candidates does not matter.
func commandParametersNamespaces(walker *committedFileWalker, candidates []discovery.SettingsCandidate) namedNamespaces {
	out := namedNamespaces{named: map[string][]string{}, others: map[string][]string{}, other: map[string]struct{}{}}
	reach := settingsReach{walker: walker, otherOrigins: map[string]bool{}, otherFiles: map[string]bool{}}
	for _, candidate := range candidates {
		if candidate.Tier != discovery.SourceTierStatic && candidate.OtherInstall && candidate.Origin != "" {
			reach.otherOrigins[candidate.Origin] = true
		}
	}
	for _, candidate := range candidates {
		if candidate.Kind == commandParametersCandidateKind && candidate.Tier == discovery.SourceTierStatic {
			reach.addCommitted(&out, candidate)
		}
	}
	for _, candidate := range candidates {
		if discovery.IsSettingsConfigMapKind(candidate.Kind) {
			reach.addSettingsConfigMap(&out, candidate)
		}
	}
	return out
}

// settingsReach collects, for commandParametersNamespaces, the
// Applications whose renders are another install (otherOrigins) and the
// committed argocd-cmd-params-cm files of another install (otherFiles).
type settingsReach struct {
	walker       *committedFileWalker
	otherOrigins map[string]bool
	otherFiles   map[string]bool
}

// addCommitted decides a committed argocd-cmd-params-cm
// (commandParametersReach).
func (r settingsReach) addCommitted(out *namedNamespaces, candidate discovery.SettingsCandidate) {
	verdict := r.walker.commandParametersReach(candidate)
	switch {
	case verdict.counts:
		addNamespacePath(out.named, r.walker.committedLanding(candidate.Path, candidateNamespace(candidate)), candidate)
	case len(verdict.renderers) > 0:
		addNamespacePath(out.others, firstNonEmpty(r.walker.committedLanding(candidate.Path, candidateNamespace(candidate)), verdict.landing), candidate)
		r.otherFiles[filepath.ToSlash(candidate.Path)] = true
		for _, key := range verdict.renderers {
			r.otherOrigins[key] = true
		}
	}
}

// addSettingsConfigMap leaves out a settings ConfigMap of another install
// and lets a rendered argocd-cmd-params-cm that reaches this cluster name
// its namespace.
func (r settingsReach) addSettingsConfigMap(out *namedNamespaces, candidate discovery.SettingsCandidate) {
	rendered := candidate.Kind == commandParametersCandidateKind && candidate.Tier != discovery.SourceTierStatic
	if settingsOfOtherInstall(candidate, r.walker, r.otherOrigins, r.otherFiles) {
		out.other[settingsCandidateLoadKey(candidate)] = struct{}{}
		if rendered {
			addNamespacePath(out.others, firstNonEmpty(candidate.InstallNamespace, candidateNamespace(candidate)), candidate)
		}
		return
	}
	if rendered && !candidate.RemoteOnly {
		addNamespacePath(out.named, candidateNamespace(candidate), candidate)
	}
}

// settingsOfOtherInstall reports a settings ConfigMap of another Argo CD
// install: rendered by one, or a committed file that only such installs on
// this cluster render.
func settingsOfOtherInstall(candidate discovery.SettingsCandidate, walker *committedFileWalker, otherOrigins, otherFiles map[string]bool) bool {
	if candidate.Tier != discovery.SourceTierStatic {
		return candidate.OtherInstall || otherOrigins[candidate.Origin]
	}
	file := filepath.ToSlash(candidate.Path)
	if otherFiles[file] {
		return true
	}
	if len(otherOrigins) == 0 {
		return false
	}
	renderers := walker.localRenderers(file)
	if len(renderers) == 0 {
		return false
	}
	for _, renderer := range renderers {
		if !otherOrigins[applicationKey(renderer.application.Application)] {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func addNamespacePath(namespaces map[string][]string, namespace string, candidate discovery.SettingsCandidate) {
	if namespace == "" {
		return
	}
	file := filepath.ToSlash(candidate.Path)
	if !slices.Contains(namespaces[namespace], file) {
		namespaces[namespace] = append(namespaces[namespace], file)
		sort.Strings(namespaces[namespace])
	}
}

// committedFileReader is sourceRendersCommittedFileWith. Tests replace it
// to count how often drydock asks whether a source renders a committed
// file.
var committedFileReader = sourceRendersCommittedFileWith

// committedFileWalker answers which discovered Applications render a
// committed file (sourceRendersCommittedFile, as the repo-server rules
// ask it). memo keeps each answer for one build side.
type committedFileWalker struct {
	root string
	// provider resolves the sources of the Applications with the
	// --repo-map entries and self-repo facts of the request
	// (settingsInstallProbe).
	provider     localProvider
	local        []discovery.ApplicationFile
	remote       []discovery.ApplicationFile
	gates        sourceTypeGates
	memo         *sync.Map
	destinations map[string]string
}

func newCommittedFileWalker(provider localProvider, applications []discovery.ApplicationFile, settings config.ArgoSettings, memo *sync.Map) *committedFileWalker {
	walker := &committedFileWalker{root: provider.repoRoot, provider: provider, gates: sourceTypeGatesFromSettings(settings), memo: memo, destinations: applicationDestinations(applications)}
	for _, appFile := range applications {
		if appFile.Tier == discovery.SourceTierExternalManifest {
			continue
		}
		if applicationOnOtherCluster(appFile.Application, settings) {
			walker.remote = append(walker.remote, appFile)
		} else {
			walker.local = append(walker.local, appFile)
		}
	}
	return walker
}

// applicationNamespaces returns the namespaces where an Application lives.
// One without a namespace lands in the destination namespace of the
// Application that renders it (controller/state.go:588-590): its parent,
// or the Applications on this cluster that render its committed file, where
// the outermost kustomization namespace on the way wins. An Application
// without a namespace that nothing renders gives "", which passes every
// check.
func (w *committedFileWalker) applicationNamespaces(appFile discovery.ApplicationFile) []string {
	if namespace := appFile.Application.Namespace; namespace != "" {
		return []string{namespace}
	}
	if appFile.ParentKey != "" {
		return []string{w.destinations[appFile.ParentKey]}
	}
	var namespaces []string
	if appFile.Tier == discovery.SourceTierStatic && appFile.Path != "" {
		for _, renderer := range w.localRenderers(filepath.ToSlash(appFile.Path)) {
			namespaces = append(namespaces, firstNonEmpty(renderer.render.kustomizeNamespace, renderer.application.Application.Spec.Destination.Namespace))
		}
	}
	if len(namespaces) == 0 {
		return []string{""}
	}
	return namespaces
}

// commandParametersVerdict is what commandParametersReach decides about a
// committed argocd-cmd-params-cm.
type commandParametersVerdict struct {
	// counts reports that it names its namespace.
	counts bool
	// renderers are the keys of the Applications on this cluster that
	// render it when none of them passes the check: it is another install,
	// which lands in landing.
	renderers []string
	landing   string
}

// commandParametersReach decides a committed argocd-cmd-params-cm. Each
// Application on this cluster that renders it puts it in a namespace: the
// outermost kustomization namespace, else its own, else the destination
// namespace. It counts when the Argo CD there reconciles that Application,
// or when no Application renders it. When only Applications on this
// cluster that fail the check render it, it is another install. When only
// Applications on other clusters render it, it neither counts nor is
// another install. The Applications that its own namespace reconciles, and
// among them those whose source path holds the file, are asked first, so
// a match stops the walk early.
func (w *committedFileWalker) commandParametersReach(candidate discovery.SettingsCandidate) commandParametersVerdict {
	file := filepath.ToSlash(candidate.Path)
	namespace := candidateNamespace(candidate)
	enabled := committedApplicationNamespaces(w.root, candidate)
	var verdict commandParametersVerdict
	// The order only guesses with the namespace that each Application
	// carries, so it walks nothing.
	for _, batch := range committedWalkBatches(w.local, file, func(appFile discovery.ApplicationFile) bool {
		return namespaceReconciled([]string{appFile.Application.Namespace}, namespace, enabled)
	}) {
		for i, render := range w.rendersAll(batch, file) {
			if !render.reads {
				continue
			}
			landing := firstNonEmpty(render.kustomizeNamespace, namespace, batch[i].Application.Spec.Destination.Namespace)
			if namespaceReconciled(w.applicationNamespaces(batch[i]), landing, enabled) {
				return commandParametersVerdict{counts: true}
			}
			verdict.renderers = append(verdict.renderers, applicationKey(batch[i].Application))
			verdict.landing = firstNonEmpty(verdict.landing, landing)
		}
	}
	if len(verdict.renderers) > 0 {
		return verdict
	}
	for _, render := range w.rendersAll(w.remote, file) {
		if render.reads {
			return verdict
		}
	}
	return commandParametersVerdict{counts: true}
}

// committedWalkBatch is how many Applications commandParametersReach asks
// at once before it looks for a match.
const committedWalkBatch = 64

// committedWalkBatches orders applications for commandParametersReach by
// committedWalkRank and splits each rank into batches, so the likely
// renderers come first and a match stops the walk early.
func committedWalkBatches(applications []discovery.ApplicationFile, file string, reconciles func(discovery.ApplicationFile) bool) [][]discovery.ApplicationFile {
	ranks := make([][]discovery.ApplicationFile, 4)
	for _, appFile := range applications {
		rank := committedWalkRank(appFile, file, reconciles)
		ranks[rank] = append(ranks[rank], appFile)
	}
	var batches [][]discovery.ApplicationFile
	for _, group := range ranks {
		for start := 0; start < len(group); start += committedWalkBatch {
			batches = append(batches, group[start:min(start+committedWalkBatch, len(group))])
		}
	}
	return batches
}

func committedWalkRank(appFile discovery.ApplicationFile, file string, reconciles func(discovery.ApplicationFile) bool) int {
	rank := 0
	if !reconciles(appFile) {
		rank += 2
	}
	if !sourcePathHoldsFile(appFile.Application, file) {
		rank++
	}
	return rank
}

func sourcePathHoldsFile(application argoappv1.Application, file string) bool {
	for _, source := range applicationSources(application) {
		sourcePath := path.Clean(filepath.ToSlash(source.Path))
		if source.Path != "" && (sourcePath == "." || strings.HasPrefix(file, sourcePath+"/")) {
			return true
		}
	}
	return false
}

// committedRenderer is an Application on this cluster that renders a
// committed file, with how it renders it.
type committedRenderer struct {
	application discovery.ApplicationFile
	render      committedFileRender
}

// localRenderers returns the Applications on this cluster that render
// file.
func (w *committedFileWalker) localRenderers(file string) []committedRenderer {
	var renderers []committedRenderer
	for i, render := range w.rendersAll(w.local, file) {
		if render.reads {
			renderers = append(renderers, committedRenderer{application: w.local[i], render: render})
		}
	}
	return renderers
}

// committedLanding returns the namespace where a committed object with
// namespace own lands: when Applications on this cluster render its file
// and all put it in one namespace, the outermost kustomization namespace,
// else own, that namespace. When nothing renders the file, or the
// Applications disagree, it is own.
func (w *committedFileWalker) committedLanding(file, own string) string {
	landing := ""
	for _, renderer := range w.localRenderers(filepath.ToSlash(file)) {
		namespace := firstNonEmpty(renderer.render.kustomizeNamespace, own)
		if namespace == "" {
			continue
		}
		if landing != "" && landing != namespace {
			return own
		}
		landing = namespace
	}
	return firstNonEmpty(landing, own)
}

// rendersAll asks, in parallel, whether each Application renders file.
func (w *committedFileWalker) rendersAll(applications []discovery.ApplicationFile, file string) []committedFileRender {
	out := make([]committedFileRender, len(applications))
	workers := min(runtime.GOMAXPROCS(0), len(applications))
	if workers <= 1 {
		for i, appFile := range applications {
			out[i] = applicationRendersCommittedFile(w.provider, file, appFile.Application, w.gates, w.memo)
		}
		return out
	}
	var next sync.Mutex
	index := 0
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				next.Lock()
				i := index
				index++
				next.Unlock()
				if i >= len(applications) {
					return
				}
				out[i] = applicationRendersCommittedFile(w.provider, file, applications[i].Application, w.gates, w.memo)
			}
		})
	}
	wg.Wait()
	return out
}

// committedApplicationNamespaces reads the application.namespaces of a
// committed argocd-cmd-params-cm.
func committedApplicationNamespaces(root string, candidate discovery.SettingsCandidate) string {
	settings, _, err := config.LoadCommandParametersConfigMapDocument(filepath.Join(root, candidate.Path), candidate.DocumentIndex)
	if err != nil {
		return ""
	}
	return settings.ApplicationNamespaces.Value
}

func applicationOnOtherCluster(application argoappv1.Application, settings config.ArgoSettings) bool {
	server, ok := project.ResolveDestinationServer(application.Spec.Destination, settings)
	return ok && server != argoappv1.KubernetesInternalAPIServerAddr
}

// committedFileRender is whether an Application renders a committed file,
// and the outermost kustomization namespace on the way, which the
// kustomize namespace transformer sets over the namespace of the file
// (sourceRendersCommittedFile).
type committedFileRender struct {
	reads              bool
	kustomizeNamespace string
}

func applicationRendersCommittedFile(provider localProvider, file string, application argoappv1.Application, gates sourceTypeGates, memo *sync.Map) committedFileRender {
	root := provider.repoRoot
	sources := applicationSources(application)
	key := ""
	if memo != nil {
		if data, err := json.Marshal(sources); err == nil {
			key = fmt.Sprintf("%s\x00%s\x00%s\x00%+v\x00%s", root, file, applicationKey(application), gates, data)
			if cached, ok := memo.Load(key); ok {
				if render, ok := cached.(committedFileRender); ok {
					return render
				}
			}
		}
	}
	var render committedFileRender
	for _, source := range sources {
		if ok, namespace := committedFileReader(provider, source, file, gates); ok {
			render = committedFileRender{reads: true, kustomizeNamespace: namespace}
			break
		}
	}
	if key != "" {
		memo.Store(key, render)
	}
	return render
}
