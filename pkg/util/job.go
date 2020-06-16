package util

import (
	"context"
	"fmt"

	computenodev1alpha1 "github.com/openstack-k8s-operators/compute-node-operator/pkg/apis/computenode/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CreateJob - creates job
func CreateJob(c client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack, jobName string, volumeMounts []corev1.VolumeMount, volumes []corev1.Volume, command []string, envVars []corev1.EnvVar) error {
	var completions int32 = 1
	var parallelism int32 = 1
	var backoffLimit int32 = 20
	oref := metav1.NewControllerRef(instance, instance.GroupVersionKind())

	// Create job
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            jobName,
			Namespace:       instance.Namespace,
			OwnerReferences: []metav1.OwnerReference{*oref},
		},
		Spec: batchv1.JobSpec{
			Completions:  &completions,
			Parallelism:  &parallelism,
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:            jobName,
					Namespace:       instance.Namespace,
					OwnerReferences: []metav1.OwnerReference{*oref},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  jobName,
						Image: instance.Spec.Drain.DrainPodImage,
						// TODO: mschuppert - Right now this is not a kolla container.
						// When this is moved to a kolla based container, we should use
						// the kolla_start procedure
						Command:      command,
						Env:          envVars,
						VolumeMounts: volumeMounts,
					}},
					Volumes:       volumes,
					RestartPolicy: "OnFailure",
				},
			},
		},
	}

	if err := c.Create(context.TODO(), job); err != nil {
		return err
	}

	return nil
}

// IsJobDone - checks if job with name finished succeeded
func IsJobDone(c client.Client, jobName string, instance *computenodev1alpha1.ComputeNodeOpenStack) (bool, error) {
	job := &batchv1.Job{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: jobName, Namespace: instance.Namespace}, job)
	if err != nil {
		return false, err
	}

	if job.Status.Succeeded == 1 {
		return true, nil
	} else if job.Status.Active >= 1 {
		return false, nil
	}

	// if job is not succeeded or active, return type and reason as error from first conditions
	// to log and reconcile
	conditionsType := ""
	conditionsReason := ""
	if len(job.Status.Conditions) > 0 {
		conditionsType = string(job.Status.Conditions[0].Type)
		conditionsReason = job.Status.Conditions[0].Reason
	}

	return false, fmt.Errorf("job type %v, reason: %v", conditionsType, conditionsReason)
}

// DeleteJobWithName - deletes job with specific name in instance namespace
func DeleteJobWithName(c client.Client, kclient kubernetes.Interface, instance *computenodev1alpha1.ComputeNodeOpenStack, jobName string) error {
	job := &batchv1.Job{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: jobName, Namespace: instance.Namespace}, job)
	if err != nil {
		return err
	}

	if job.Status.Succeeded == 1 {
		background := metav1.DeletePropagationBackground
		err = kclient.BatchV1().Jobs(instance.Namespace).Delete(jobName, &metav1.DeleteOptions{PropagationPolicy: &background})
		if err != nil {
			return fmt.Errorf("failed to delete job: %s, %v", job.Name, err)
		}
	}

	return nil
}
