package app

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
)

// controllerNamespaceScope is what a settings load needs to decide the
// Argo CD controller namespace: the --argocd-namespace flag, the namespace
// of a restarted discovery run, and the per-run memo of the committed
// files that Applications render.
type controllerNamespaceScope struct {
	flag    string
	hint    string
	renders *sync.Map
}

func (request BuildRequest) namespaceScope() controllerNamespaceScope {
	return controllerNamespaceScope{flag: request.ArgoCDNamespace, hint: request.controllerNamespaceHint, renders: request.committedRenderMemo}
}

// apply sets the flag and the hint on settings.
func (scope controllerNamespaceScope) apply(settings config.ArgoSettings) config.ArgoSettings {
	settings.ArgoCDNamespace = scope.flag
	settings.ControllerNamespaceHint = scope.hint
	return settings
}

// namespaceRecorder records the controller namespaces that the renders of
// one discovery run use, so drydock can tell whether a render used another
// namespace than the one that the run decides at its end.
type namespaceRecorder struct {
	mu   sync.Mutex
	used []string
}

func (r *namespaceRecorder) record(namespace string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.used, namespace) {
		r.used = append(r.used, namespace)
	}
}

// onlyUsed reports whether every recorded render used namespace.
func (r *namespaceRecorder) onlyUsed(namespace string) bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, used := range r.used {
		if used != namespace {
			return false
		}
	}
	return true
}

// controllerNamespaceUnsettledCode warns that a restarted discovery run
// still ends with another controller namespace than the one it used. Like
// settings.appset-generation-cycle, it reports what drydock cannot settle
// offline, so --strict does not fail on it.
const controllerNamespaceUnsettledCode = "settings.controller-namespace-unsettled"

// discoverWithControllerNamespace discovers the repository once, and once
// more when a render of that run used another controller namespace than
// the one that the settings at its end give. Fleet discovery can find the
// argocd-cmd-params-cm that names the namespace late, in a rendered Argo CD
// install, after it rendered other Applications, the plugin policy
// bootstrap entrypoints among them, with the default instance names. The
// second run uses the final namespace from its start
// (BuildRequest.controllerNamespaceHint) and reuses the renders of the
// first run. With --argocd-namespace, or when every render used the final
// namespace, as in a repository whose Argo CD runs in argocd, there is no
// second run. There is never a third run: when the second run ends with yet
// another namespace, it keeps its result and warns.
func (o Orchestrator) discoverWithControllerNamespace(ctx context.Context, root string, request BuildRequest, ociFetcher *appsetOCIArtifactFetcher) (discovery.Result, []diagnostic.Diagnostic, []cacheevent.Event, *applicationRenderCache, string, error) {
	recorder := &namespaceRecorder{}
	request.renderNamespaces = recorder
	discovered, diags, events, renderCache, renderSig, err := o.discoverRepositoryWithOCI(ctx, root, request, ociFetcher)
	if err != nil || request.ArgoCDNamespace != "" {
		return discovered, diags, events, renderCache, renderSig, err
	}
	final, err := finalControllerNamespace(root, request, discovered)
	if err != nil || recorder.onlyUsed(final) {
		return discovered, diags, events, renderCache, renderSig, err
	}
	restartRequest := request
	restartRequest.controllerNamespaceHint = final
	restartRequest.renderCache = renderCache
	restartRequest.renderNamespaces = nil
	restarted, restartDiags, restartEvents, renderCache, renderSig, err := o.discoverRepositoryWithOCI(ctx, root, restartRequest, ociFetcher)
	events = append(events, restartEvents...)
	if err != nil {
		return restarted, restartDiags, events, renderCache, renderSig, err
	}
	again, err := finalControllerNamespace(root, request, restarted)
	if err == nil && again != final {
		restartDiags = append(restartDiags, unsettledControllerNamespaceDiagnostic(final, again))
	}
	return restarted, restartDiags, events, renderCache, renderSig, err
}

// finalControllerNamespace is the controller namespace that the settings
// of discovered give, without a restart hint.
func finalControllerNamespace(root string, request BuildRequest, discovered discovery.Result) (string, error) {
	request.controllerNamespaceHint = ""
	settings, _, err := loadSettingsFromDiscovery(root, request.namespaceScope(), discovered)
	if err != nil {
		return "", err
	}
	return settings.ControllerNamespace(""), nil
}

func unsettledControllerNamespaceDiagnostic(used, final string) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     controllerNamespaceUnsettledCode,
		Severity: diagnostic.SeverityWarning,
		Category: "settings",
		Message: fmt.Sprintf("the Argo CD controller namespace does not settle: discovery ran again with %q, and its settings then give %q; drydock keeps the run with %q; pass --argocd-namespace to name the namespace where Argo CD runs",
			used, final, used),
	}
}
