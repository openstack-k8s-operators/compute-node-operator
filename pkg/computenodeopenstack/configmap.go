package computenodeopenstack

import (
	computenodev1alpha1 "github.com/openstack-k8s-operators/compute-node-operator/pkg/apis/computenode/v1alpha1"
	util "github.com/openstack-k8s-operators/lib-common/pkg/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ScriptsConfigMap - scripts config map
func ScriptsConfigMap(cr *computenodev1alpha1.ComputeNodeOpenStack, cmName string) *corev1.ConfigMap {

	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: cr.Namespace,
		},
		Data: map[string]string{
			"disablecomputeservice.sh": util.ExecuteTemplateFile("drainpod/bin/disablecomputeservice.sh", nil),
			"scaledownpre.sh":          util.ExecuteTemplateFile("drainpod/bin/scaledownpre.sh", nil),
			"scaledownpost.sh":         util.ExecuteTemplateFile("drainpod/bin/scaledownpost.sh", nil),
		},
	}

	return cm
}
