package appset

import (
	"encoding/json"
	"fmt"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
)

func renderApplicationTemplateWithTemplate(appset argoappv1.ApplicationSet, template argoappv1.ApplicationSetTemplate, params map[string]any) (argoappv1.Application, error) {
	meta := template.ApplicationSetTemplateMeta
	templateApp, err := cloneTemplateApp(argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:        meta.Name,
			Namespace:   meta.Namespace,
			Labels:      meta.Labels,
			Annotations: meta.Annotations,
			Finalizers:  meta.Finalizers,
		},
		Spec: template.Spec,
	})
	if err != nil {
		return argoappv1.Application{}, err
	}

	renderer := &appsetutils.Render{}
	rendered, err := renderer.RenderTemplateParams(
		&templateApp,
		appset.Spec.SyncPolicy,
		params,
		appset.Spec.GoTemplate,
		appset.Spec.GoTemplateOptions,
	)
	if err != nil {
		return argoappv1.Application{}, err
	}

	if appset.Spec.TemplatePatch != nil {
		patched, err := renderTemplatePatch(renderer, rendered, appset, params)
		if err != nil {
			return argoappv1.Application{}, err
		}
		rendered = patched
	}

	return *rendered, nil
}

func renderTemplatePatch(renderer appsetutils.Renderer, app *argoappv1.Application, appset argoappv1.ApplicationSet, params map[string]any) (*argoappv1.Application, error) {
	replacedTemplate, err := renderer.Replace(*appset.Spec.TemplatePatch, params, appset.Spec.GoTemplate, appset.Spec.GoTemplateOptions)
	if err != nil {
		return nil, fmt.Errorf("error replacing values in templatePatch: %w", err)
	}

	return applyTemplatePatch(app, replacedTemplate)
}

func applyTemplatePatch(app *argoappv1.Application, templatePatch string) (*argoappv1.Application, error) {
	appData, err := json.Marshal(app)
	if err != nil {
		return nil, fmt.Errorf("error while marshalling Application: %w", err)
	}

	convertedTemplatePatch, err := appsetutils.ConvertYAMLToJSON(templatePatch)
	if err != nil {
		return nil, fmt.Errorf("error while converting template to json %q: %w", convertedTemplatePatch, err)
	}

	if err := json.Unmarshal([]byte(convertedTemplatePatch), &argoappv1.Application{}); err != nil {
		return nil, fmt.Errorf("invalid templatePatch %q: %w", convertedTemplatePatch, err)
	}

	data, err := strategicpatch.StrategicMergePatch(appData, []byte(convertedTemplatePatch), argoappv1.Application{})
	if err != nil {
		return nil, fmt.Errorf("error while applying templatePatch template to json %q: %w", convertedTemplatePatch, err)
	}

	var finalApp argoappv1.Application
	if err := json.Unmarshal(data, &finalApp); err != nil {
		return nil, fmt.Errorf("error while unmarshalling patched application: %w", err)
	}

	finalApp.Spec.Project = app.Spec.Project
	return &finalApp, nil
}
