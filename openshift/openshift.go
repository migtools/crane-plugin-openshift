package openshift

import (
	"encoding/json"
	"fmt"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/konveyor/crane-lib/transform/util"
	appsv1 "github.com/openshift/api/apps/v1"
	rbacv1 "github.com/openshift/api/authorization/v1"
	buildv1API "github.com/openshift/api/build/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	podReplaceImagePullSecret = `%v%v
{ "op": "replace", "path": "/spec/imagePullSecrets/%v/name", "value": "%v"}`
	podRemoveImagePullSecret = `%v%v
{ "op": "remove", "path": "/spec/imagePullSecrets/%v"}`
	serviceAccountRemoveSecret = `%v%v
{ "op": "remove", "path": "/secrets/%v"}`
	serviceAccountRemoveImagePullSecret = `%v%v
{ "op": "remove", "path": "/imagePullSecrets/%v"}`
	replaceSecretOp = `[
{ "op": "replace", "path": "%v", "value": "%v"}
]`
	opRemove = `[
{ "op": "remove", "path": "%v"}
]`
	buildConfigOutputPushSecret         = "/spec/output/pushSecret"
	buildConfigOutputTo                 = "/spec/output/to/name"
	buildConfigSourceStrategyPullSecret = "/spec/strategy/sourceStrategy/pullSecret"
	buildConfigSourceStrategyFrom       = "/spec/strategy/sourceStrategy/from/name"
	buildConfigDockerStrategyPullSecret = "/spec/strategy/dockerStrategy/pullSecret"
	buildConfigDockerStrategyFrom       = "/spec/strategy/dockerStrategy/from/name"
	buildConfigCustomStrategyPullSecret = "/spec/strategy/customStrategy/pullSecret"
	buildConfigCustomStrategyFrom       = "/spec/strategy/customStrategy/from/name"
	buildConfigSourceImagesPullSecret   = "/spec/source/images/%v/pullSecret"
	buildConfigSourceImagesFrom         = "/spec/source/images/%v/from/name"
	roleBindingSubject                  = "/subjects/%d/namespace"
)

var defaultPullSecrets = []string{"builder-dockercfg-", "default-dockercfg-", "deployer-dockercfg-"}

func updateBuildConfigImageReference(
	imgRef v1.ObjectReference,
	imgPath string,
	fields openshiftOptionalFields,
) (jsonpatch.Patch, error) {
	patch := jsonpatch.Patch{}
	var err error
	if imgRef.Kind != "DockerImage" {
		return patch, nil
	}
	if fields.RegistryReplacement == nil || len(fields.RegistryReplacement) == 0 {
		return patch, nil
	}
	updatedImageRef, update := util.UpdateImageRegistry(fields.RegistryReplacement, imgRef.Name)
	if update {
		patch, err = util.UpdateImage(imgPath, updatedImageRef)
		if err != nil {
			return nil, err
		}
	}
	return patch, nil
}

func UpdateDefaultPullSecrets(u unstructured.Unstructured, fields openshiftOptionalFields) (jsonpatch.Patch, error) {
	return updateSecretsForSlice(getPullSecrets(u), podReplaceImagePullSecret, podRemoveImagePullSecret, fields)
}

func updateSecretsForSlice(
	pullSecrets []v1.LocalObjectReference,
	replaceOp string,
	removeOp string,
	fields openshiftOptionalFields) (jsonpatch.Patch, error) {
	var err error

	replacePatch := jsonpatch.Patch{}
	removePatch := jsonpatch.Patch{}
	replaceJSON := `[`
	removeJSON := `[`

	// iterate in reverse order since jsonpatch array element removal shifts
	// later elements up, which would break later remove patch entries otherwise
	var priorReplace, priorRemove bool
	for i := len(pullSecrets) - 1; i >= 0; i-- {
		newSecret, ok := fields.PullSecretReplacement[pullSecrets[i].Name]
		// replacement found
		if ok {
			replaceJSON = fmt.Sprintf(
				replaceOp,
				replaceJSON,
				nonInitialDelimiter(priorReplace),
				i,
				newSecret,
			)
			priorReplace = true
		} else if fields.StripDefaultPullSecrets && isDefault(pullSecrets[i].Name) {
			removeJSON = fmt.Sprintf(
				removeOp,
				removeJSON,
				nonInitialDelimiter(priorRemove),
				i,
			)
			priorRemove = true
		}
	}
	replaceJSON = fmt.Sprintf("%v]", replaceJSON)
	removeJSON = fmt.Sprintf("%v]", removeJSON)

	if priorReplace {
		replacePatch, err = jsonpatch.DecodePatch([]byte(replaceJSON))
		if err != nil {
			return nil, err
		}
	}
	if priorRemove {
		removePatch, err = jsonpatch.DecodePatch([]byte(removeJSON))
		if err != nil {
			return nil, err
		}
	}
	return append(replacePatch, removePatch...), nil
}

func UpdateRoleBinding(u unstructured.Unstructured) (jsonpatch.Patch, error) {
	jsonPatch := jsonpatch.Patch{}

	js, err := u.MarshalJSON()
	if err != nil {
		return nil, err
	}
	rb := &rbacv1.RoleBinding{}

	err = json.Unmarshal(js, rb)
	if err != nil {
		return nil, err
	}
	for i, subj := range rb.Subjects {
		if subj.Kind == "ServiceAccount" && subj.Namespace == rb.Namespace {
			subjPath := fmt.Sprintf(roleBindingSubject, i)
			patch, err := jsonpatch.DecodePatch([]byte(fmt.Sprintf(opRemove, subjPath)))
			if err != nil {
				return nil, err
			}
			jsonPatch = append(jsonPatch, patch...)
		}
	}
	return jsonPatch, nil
}

func updateSecret(
	pullSecret *v1.LocalObjectReference,
	secretPath string,
	fields openshiftOptionalFields) (jsonpatch.Patch, error) {
	var err error

	patch := jsonpatch.Patch{}
	var patchJSON string

	if pullSecret == nil || len(pullSecret.Name) == 0 {
		return patch, nil
	}

	newSecret, ok := fields.PullSecretReplacement[pullSecret.Name]
	// replacement found
	if ok {
		patchJSON = fmt.Sprintf(replaceSecretOp, secretPath+"/name", newSecret)
	} else if fields.StripDefaultPullSecrets && isDefault(pullSecret.Name) {
		patchJSON = fmt.Sprintf(opRemove, secretPath)
	}

	if len(patchJSON) > 0 {
		patch, err = jsonpatch.DecodePatch([]byte(patchJSON))
		if err != nil {
			return nil, err
		}
	}
	return patch, nil
}

func nonInitialDelimiter(priorEntries bool) string {
	if priorEntries {
		return ","
	} else {
		return ""
	}
}
func UpdateServiceAccount(u unstructured.Unstructured) (jsonpatch.Patch, error) {
	jsonPatch := jsonpatch.Patch{}
	check := u.GetName() + "-dockercfg-"
	var err error

	pullSecrets := getPullSecretReferencesServiceAccount(u)
	pullSecretsPatch := jsonpatch.Patch{}
	pullSecretsJSON := `[`
	var priorPullSecret bool
	for i := len(pullSecrets) - 1; i >= 0; i-- {
		if strings.HasPrefix(pullSecrets[i].Name, check) {
			pullSecretsJSON = fmt.Sprintf(
				serviceAccountRemoveImagePullSecret,
				pullSecretsJSON,
				nonInitialDelimiter(priorPullSecret),
				i,
			)
			priorPullSecret = true
		}
	}
	pullSecretsJSON = fmt.Sprintf("%v]", pullSecretsJSON)
	if priorPullSecret {
		pullSecretsPatch, err = jsonpatch.DecodePatch([]byte(pullSecretsJSON))
		if err != nil {
			return jsonPatch, err
		}
		jsonPatch = append(jsonPatch, pullSecretsPatch...)
	}

	secrets := getSecretReferencesServiceAccount(u)
	secretsPatch := jsonpatch.Patch{}
	secretsJSON := `[`
	var priorSecret bool
	for i := len(secrets) - 1; i >= 0; i-- {
		if strings.HasPrefix(secrets[i].Name, check) {
			secretsJSON = fmt.Sprintf(
				serviceAccountRemoveSecret,
				secretsJSON,
				nonInitialDelimiter(priorSecret),
				i,
			)
			priorSecret = true
		}
	}
	secretsJSON = fmt.Sprintf("%v]", secretsJSON)
	if priorSecret {
		secretsPatch, err = jsonpatch.DecodePatch([]byte(secretsJSON))
		if err != nil {
			return jsonPatch, err
		}
		jsonPatch = append(jsonPatch, secretsPatch...)
	}

	return jsonPatch, nil
}

func UpdateRoute(u unstructured.Unstructured) (jsonpatch.Patch, error) {
	var patch jsonpatch.Patch
	var err error
	annotations := u.GetAnnotations()
	if annotations != nil && annotations["openshift.io/host.generated"] == "true" {
		patchJSON := fmt.Sprintf(`[
{ "op": "remove", "path": "/spec/host"}
]`)

		patch, err = jsonpatch.DecodePatch([]byte(patchJSON))
		if err != nil {
			return nil, err
		}
	}
	return patch, nil
}

func isDefault(name string) bool {
	for _, d := range defaultPullSecrets {
		if strings.Contains(name, d) {
			return true
		}
	}
	return false
}

func UpdateBuildConfig(u unstructured.Unstructured, fields openshiftOptionalFields) (jsonpatch.Patch, error) {
	jsonPatch := jsonpatch.Patch{}
	js, err := u.MarshalJSON()
	if err != nil {
		return jsonPatch, err
	}

	buildConfig := &buildv1API.BuildConfig{}

	err = json.Unmarshal(js, buildConfig)
	if err != nil {
		return jsonPatch, err
	}
	patch, err := updateSecret(buildConfig.Spec.Output.PushSecret, buildConfigOutputPushSecret, fields)
	if err != nil {
		return nil, err
	}
	jsonPatch = append(jsonPatch, patch...)
	if buildConfig.Spec.Output.To != nil {
		patch, err := updateBuildConfigImageReference(*buildConfig.Spec.Output.To, buildConfigOutputTo, fields)
		if err != nil {
			return jsonPatch, err
		}
		jsonPatch = append(jsonPatch, patch...)
	}

	if buildConfig.Spec.Strategy.SourceStrategy != nil {
		patch, err := updateSecret(buildConfig.Spec.Strategy.SourceStrategy.PullSecret, buildConfigSourceStrategyPullSecret, fields)
		if err != nil {
			return nil, err
		}
		jsonPatch = append(jsonPatch, patch...)
		patch, err = updateBuildConfigImageReference(buildConfig.Spec.Strategy.SourceStrategy.From, buildConfigSourceStrategyFrom, fields)
		if err != nil {
			return jsonPatch, err
		}
		jsonPatch = append(jsonPatch, patch...)
	}

	if buildConfig.Spec.Strategy.DockerStrategy != nil {
		patch, err := updateSecret(buildConfig.Spec.Strategy.DockerStrategy.PullSecret, buildConfigDockerStrategyPullSecret, fields)
		if err != nil {
			return nil, err
		}
		if buildConfig.Spec.Strategy.DockerStrategy != nil && buildConfig.Spec.Strategy.DockerStrategy.From != nil {
			jsonPatch = append(jsonPatch, patch...)
			patch, err := updateBuildConfigImageReference(*buildConfig.Spec.Strategy.DockerStrategy.From, buildConfigDockerStrategyFrom, fields)
			if err != nil {
				return jsonPatch, err
			}
			jsonPatch = append(jsonPatch, patch...)
		}
	}

	if buildConfig.Spec.Strategy.CustomStrategy != nil {
		patch, err := updateSecret(buildConfig.Spec.Strategy.CustomStrategy.PullSecret, buildConfigCustomStrategyPullSecret, fields)
		if err != nil {
			return nil, err
		}
		jsonPatch = append(jsonPatch, patch...)
		patch, err = updateBuildConfigImageReference(buildConfig.Spec.Strategy.CustomStrategy.From, buildConfigCustomStrategyFrom, fields)
		if err != nil {
			return jsonPatch, err
		}
		jsonPatch = append(jsonPatch, patch...)
	}

	if buildConfig.Spec.Source.Images != nil {
		for i, imageSource := range buildConfig.Spec.Source.Images {
			patch, err := updateSecret(imageSource.PullSecret, fmt.Sprintf(buildConfigSourceImagesPullSecret, i), fields)
			if err != nil {
				return nil, err
			}
			jsonPatch = append(jsonPatch, patch...)
			patch, err = updateBuildConfigImageReference(imageSource.From, fmt.Sprintf(buildConfigSourceImagesFrom, i), fields)
			if err != nil {
				return jsonPatch, err
			}
			jsonPatch = append(jsonPatch, patch...)
		}
	}
	return jsonPatch, nil
}

func UpdateDeploymentConfig(u unstructured.Unstructured, fields openshiftOptionalFields) (jsonpatch.Patch, error) {
	js, err := u.MarshalJSON()
	if err != nil {
		return nil, err
	}
	deploymentConfig := &appsv1.DeploymentConfig{}
	err = json.Unmarshal(js, deploymentConfig)

	patches, err := util.RenamePVCs(deploymentConfig.Spec.Template.Spec.Volumes, fields.PVCRenameMap, util.PVCPathGenericString)
	if err != nil {
		return nil, err
	}
	return patches, nil
}

func getPullSecrets(u unstructured.Unstructured) []v1.LocalObjectReference {
	js, err := u.MarshalJSON()
	if err != nil {
		return nil
	}

	pod := &v1.Pod{}

	err = json.Unmarshal(js, pod)
	if err != nil {
		return nil
	}

	return pod.Spec.ImagePullSecrets
}

func getPullSecretReferencesServiceAccount(u unstructured.Unstructured) []v1.LocalObjectReference {
	js, err := u.MarshalJSON()
	if err != nil {
		return nil
	}

	sa := &v1.ServiceAccount{}

	err = json.Unmarshal(js, sa)
	if err != nil {
		return nil
	}

	return sa.ImagePullSecrets
}

func getSecretReferencesServiceAccount(u unstructured.Unstructured) []v1.ObjectReference {
	js, err := u.MarshalJSON()
	if err != nil {
		return nil
	}

	sa := &v1.ServiceAccount{}

	err = json.Unmarshal(js, sa)
	if err != nil {
		return nil
	}

	return sa.Secrets
}

// stripSecurityContext removes cluster-specific runtime security context values
// that are injected by the SCC admission controller. This prevents SCC validation
// failures when migrating between OpenShift clusters with different namespace UID ranges.
func stripSecurityContext(u unstructured.Unstructured) (jsonpatch.Patch, error) {
	kind := u.GetKind()

	// Only process workload resources
	if kind != "Pod" && kind != "Deployment" && kind != "StatefulSet" &&
		kind != "DaemonSet" && kind != "Job" && kind != "CronJob" &&
		kind != "ReplicaSet" && kind != "ReplicationController" {
		return jsonpatch.Patch{}, nil
	}

	// Create a copy to modify
	modified := u.DeepCopy()

	// Remove pod-level spec.securityContext
	unstructured.RemoveNestedField(modified.Object, "spec", "securityContext")

	// For workload controllers, remove spec.template.spec.securityContext
	if kind != "Pod" {
		if kind == "CronJob" {
			// CronJob has spec.jobTemplate.spec.template.spec
			unstructured.RemoveNestedField(modified.Object, "spec", "jobTemplate", "spec", "template", "spec", "securityContext")
		} else {
			// Deployment/StatefulSet/DaemonSet/Job/ReplicaSet/ReplicationController have spec.template.spec
			unstructured.RemoveNestedField(modified.Object, "spec", "template", "spec", "securityContext")
		}
	}

	// Helper function to strip container securityContext
	stripContainerSecurityContext := func(containersPath ...string) {
		containers, found, _ := unstructured.NestedSlice(modified.Object, containersPath...)
		if found {
			for i, c := range containers {
				if container, ok := c.(map[string]interface{}); ok {
					delete(container, "securityContext")
					containers[i] = container
				}
			}
			unstructured.SetNestedSlice(modified.Object, containers, containersPath...)
		}
	}

	// Determine base path for containers
	var basePath []string
	if kind == "Pod" {
		basePath = []string{"spec"}
	} else if kind == "CronJob" {
		basePath = []string{"spec", "jobTemplate", "spec", "template", "spec"}
	} else {
		basePath = []string{"spec", "template", "spec"}
	}

	// Remove container-level securityContext
	containersPath := append(basePath, "containers")
	stripContainerSecurityContext(containersPath...)

	// Remove initContainers securityContext
	initContainersPath := append(basePath, "initContainers")
	stripContainerSecurityContext(initContainersPath...)

	// Remove ephemeralContainers securityContext (if present)
	ephemeralContainersPath := append(basePath, "ephemeralContainers")
	stripContainerSecurityContext(ephemeralContainersPath...)

	// Generate patch between original and modified
	originalJSON, err := u.MarshalJSON()
	if err != nil {
		return nil, err
	}

	modifiedJSON, err := modified.MarshalJSON()
	if err != nil {
		return nil, err
	}

	patch, err := jsonpatch.CreatePatch(originalJSON, modifiedJSON)
	if err != nil {
		return nil, err
	}

	return patch, nil
}
