package appset

import (
	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func renderApplicationTemplate(appset argoappv1.ApplicationSet, params map[string]any) (argoappv1.Application, error) {
	meta := appset.Spec.Template.ApplicationSetTemplateMeta
	templateApp, err := cloneTemplateApp(argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:        meta.Name,
			Namespace:   meta.Namespace,
			Labels:      meta.Labels,
			Annotations: meta.Annotations,
			Finalizers:  meta.Finalizers,
		},
		Spec: appset.Spec.Template.Spec,
	})
	if err != nil {
		return argoappv1.Application{}, err
	}

	rendered, err := (&appsetutils.Render{}).RenderTemplateParams(
		&templateApp,
		appset.Spec.SyncPolicy,
		params,
		appset.Spec.GoTemplate,
		appset.Spec.GoTemplateOptions,
	)
	if err != nil {
		return argoappv1.Application{}, err
	}

	return *rendered, nil
}
