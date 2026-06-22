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

	// SCCNamespaceUIDMin is the minimum UID value for OpenShift SCC-injected namespace UID ranges.
	// UIDs >= this value are considered SCC-injected and should be stripped during migration.
	SCCNamespaceUIDMin int64 = 1000000000
)

var defaultPullSecrets = []string{"builder-dockercfg-", "default-dockercfg-", "deployer-dockercfg-"}

func updateBuildConfigImageReference(
	imgRef v1.ObjectReference,
	imgPath string,
	fields OpenshiftOptionalFields,
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

func UpdateDefaultPullSecrets(u unstructured.Unstructured, fields OpenshiftOptionalFields) (jsonpatch.Patch, error) {
	return updateSecretsForSlice(getPullSecrets(u), podReplaceImagePullSecret, podRemoveImagePullSecret, fields)
}

func updateSecretsForSlice(
	pullSecrets []v1.LocalObjectReference,
	replaceOp string,
	removeOp string,
	fields OpenshiftOptionalFields) (jsonpatch.Patch, error) {
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
	fields OpenshiftOptionalFields) (jsonpatch.Patch, error) {
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

func UpdateBuildConfig(u unstructured.Unstructured, fields OpenshiftOptionalFields) (jsonpatch.Patch, error) {
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

func UpdateDeploymentConfig(u unstructured.Unstructured, fields OpenshiftOptionalFields) (jsonpatch.Patch, error) {
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

// StripSecurityContext removes SCC-injected security context values while preserving
// user-configured values. This prevents SCC validation failures when migrating between
// OpenShift clusters with different namespace UID ranges.
//
// Only strips:
//   - runAsUser when >= SCCNamespaceUIDMin (SCC-injected namespace UID range)
//   - fsGroup when >= SCCNamespaceUIDMin (SCC-injected namespace UID range)
//   - seLinuxOptions.level (always SCC-injected)
//
// Preserves all other security context values (capabilities, readOnlyRootFilesystem, etc.)
//
// Example:
//
//	Before:
//	  securityContext:
//	    runAsUser: 1000560000          # SCC-injected (>= SCCNamespaceUIDMin)
//	    fsGroup: 1000560000            # SCC-injected
//	    runAsNonRoot: true             # User-configured
//	    seLinuxOptions:
//	      level: s0:c26,c5             # SCC-injected
//	      type: spc_t                  # User-configured
//
//	After:
//	  securityContext:
//	    runAsNonRoot: true             # Preserved
//	    seLinuxOptions:
//	      type: spc_t                  # Preserved
func StripSecurityContext(u unstructured.Unstructured) (jsonpatch.Patch, error) {
	kind := u.GetKind()

	// Only process workload resources
	if kind != "Pod" && kind != "Deployment" && kind != "StatefulSet" &&
		kind != "DaemonSet" && kind != "Job" && kind != "CronJob" &&
		kind != "ReplicaSet" && kind != "ReplicationController" {
		return jsonpatch.Patch{}, nil
	}

	// Create a copy to modify
	modified := u.DeepCopy()

	// Determine base path for pod spec
	var basePath []string
	if kind == "Pod" {
		basePath = []string{"spec"}
	} else if kind == "CronJob" {
		basePath = []string{"spec", "jobTemplate", "spec", "template", "spec"}
	} else {
		basePath = []string{"spec", "template", "spec"}
	}

	// Strip SCC-injected values from pod-level securityContext
	stripPodSecurityContext := func(scPath ...string) error {
		sc, found, _ := unstructured.NestedMap(modified.Object, scPath...)
		if !found || sc == nil {
			return nil
		}

		// Strip runAsUser if >= 1000000000
		if runAsUser, ok := sc["runAsUser"].(int64); ok && runAsUser >= SCCNamespaceUIDMin {
			delete(sc, "runAsUser")
		}

		// Strip fsGroup if >= 1000000000
		if fsGroup, ok := sc["fsGroup"].(int64); ok && fsGroup >= SCCNamespaceUIDMin {
			delete(sc, "fsGroup")
		}

		// Strip seLinuxOptions.level (always SCC-injected)
		if seLinuxOpts, ok := sc["seLinuxOptions"].(map[string]interface{}); ok {
			delete(seLinuxOpts, "level")
			// If seLinuxOptions is now empty, remove it entirely
			if len(seLinuxOpts) == 0 {
				delete(sc, "seLinuxOptions")
			} else {
				sc["seLinuxOptions"] = seLinuxOpts
			}
		}

		// If securityContext is now empty, remove it entirely
		if len(sc) == 0 {
			unstructured.RemoveNestedField(modified.Object, scPath...)
		} else {
			if err := unstructured.SetNestedMap(modified.Object, sc, scPath...); err != nil {
				return err
			}
		}
		return nil
	}

	// Strip pod-level security context
	podSecurityContextPath := append(basePath, "securityContext")
	if err := stripPodSecurityContext(podSecurityContextPath...); err != nil {
		return nil, err
	}

	// Helper function to strip container securityContext
	stripContainerSecurityContext := func(containersPath ...string) error {
		containers, found, _ := unstructured.NestedSlice(modified.Object, containersPath...)
		if !found {
			return nil
		}

		for i, c := range containers {
			container, ok := c.(map[string]interface{})
			if !ok {
				continue
			}

			sc, ok := container["securityContext"].(map[string]interface{})
			if !ok || sc == nil {
				continue
			}

			// Strip runAsUser if >= 1000000000
			if runAsUser, ok := sc["runAsUser"].(int64); ok && runAsUser >= SCCNamespaceUIDMin {
				delete(sc, "runAsUser")
			}

			// Strip fsGroup if >= 1000000000
			if fsGroup, ok := sc["fsGroup"].(int64); ok && fsGroup >= SCCNamespaceUIDMin {
				delete(sc, "fsGroup")
			}

			// Strip seLinuxOptions.level (always SCC-injected)
			if seLinuxOpts, ok := sc["seLinuxOptions"].(map[string]interface{}); ok {
				delete(seLinuxOpts, "level")
				// If seLinuxOptions is now empty, remove it entirely
				if len(seLinuxOpts) == 0 {
					delete(sc, "seLinuxOptions")
				} else {
					sc["seLinuxOptions"] = seLinuxOpts
				}
			}

			// If securityContext is now empty, remove it entirely
			if len(sc) == 0 {
				delete(container, "securityContext")
			} else {
				container["securityContext"] = sc
			}

			containers[i] = container
		}
		if err := unstructured.SetNestedSlice(modified.Object, containers, containersPath...); err != nil {
			return err
		}
		return nil
	}

	// Strip container-level securityContext
	containersPath := append(basePath, "containers")
	if err := stripContainerSecurityContext(containersPath...); err != nil {
		return nil, err
	}

	// Strip initContainers securityContext
	initContainersPath := append(basePath, "initContainers")
	if err := stripContainerSecurityContext(initContainersPath...); err != nil {
		return nil, err
	}

	// Strip ephemeralContainers securityContext (if present)
	ephemeralContainersPath := append(basePath, "ephemeralContainers")
	if err := stripContainerSecurityContext(ephemeralContainersPath...); err != nil {
		return nil, err
	}

	// Generate patch by comparing original and modified
	// Build JSON patch operations for fields that were removed
	var patchOps []string

	// Helper to build path string
	buildPath := func(parts ...string) string {
		result := ""
		for _, part := range parts {
			if part != "" {
				result += "/" + part
			}
		}
		return result
	}

	// Check what changed in pod-level securityContext
	origSC, _, _ := unstructured.NestedMap(u.Object, append(basePath, "securityContext")...)
	modSC, _, _ := unstructured.NestedMap(modified.Object, append(basePath, "securityContext")...)

	if origSC != nil && modSC != nil {
		scPath := buildPath(basePath...) + "/securityContext"
		// Check each field
		if _, origHas := origSC["runAsUser"]; origHas {
			if _, modHas := modSC["runAsUser"]; !modHas {
				patchOps = append(patchOps, fmt.Sprintf(`{"op":"remove","path":"%s/runAsUser"}`, scPath))
			}
		}
		if _, origHas := origSC["fsGroup"]; origHas {
			if _, modHas := modSC["fsGroup"]; !modHas {
				patchOps = append(patchOps, fmt.Sprintf(`{"op":"remove","path":"%s/fsGroup"}`, scPath))
			}
		}
		if origOpts, ok := origSC["seLinuxOptions"].(map[string]interface{}); ok {
			if modOpts, ok := modSC["seLinuxOptions"].(map[string]interface{}); ok {
				if _, origHas := origOpts["level"]; origHas {
					if _, modHas := modOpts["level"]; !modHas {
						patchOps = append(patchOps, fmt.Sprintf(`{"op":"remove","path":"%s/seLinuxOptions/level"}`, scPath))
					}
				}
			} else if len(origOpts) > 0 {
				// seLinuxOptions was removed entirely
				if _, hasLevel := origOpts["level"]; hasLevel && len(origOpts) == 1 {
					patchOps = append(patchOps, fmt.Sprintf(`{"op":"remove","path":"%s/seLinuxOptions"}`, scPath))
				}
			}
		}
	} else if origSC != nil && modSC == nil {
		// Entire securityContext was removed
		patchOps = append(patchOps, fmt.Sprintf(`{"op":"remove","path":"%s"}`, buildPath(basePath...)+"/securityContext"))
	}

	// Check container changes
	checkContainerChanges := func(containerType string) {
		origContainers, _, _ := unstructured.NestedSlice(u.Object, append(basePath, containerType)...)
		modContainers, _, _ := unstructured.NestedSlice(modified.Object, append(basePath, containerType)...)

		for i := 0; i < len(origContainers) && i < len(modContainers); i++ {
			origC, ok1 := origContainers[i].(map[string]interface{})
			modC, ok2 := modContainers[i].(map[string]interface{})
			if !ok1 || !ok2 {
				continue
			}

			origSC, _ := origC["securityContext"].(map[string]interface{})
			modSC, ok2 := modC["securityContext"].(map[string]interface{})

			containerPath := buildPath(basePath...) + "/" + containerType + "/" + fmt.Sprintf("%d", i) + "/securityContext"

			if origSC != nil && modSC != nil {
				if _, origHas := origSC["runAsUser"]; origHas {
					if _, modHas := modSC["runAsUser"]; !modHas {
						patchOps = append(patchOps, fmt.Sprintf(`{"op":"remove","path":"%s/runAsUser"}`, containerPath))
					}
				}
				if _, origHas := origSC["fsGroup"]; origHas {
					if _, modHas := modSC["fsGroup"]; !modHas {
						patchOps = append(patchOps, fmt.Sprintf(`{"op":"remove","path":"%s/fsGroup"}`, containerPath))
					}
				}
				if origOpts, ok := origSC["seLinuxOptions"].(map[string]interface{}); ok {
					if modOpts, ok := modSC["seLinuxOptions"].(map[string]interface{}); ok {
						if _, origHas := origOpts["level"]; origHas {
							if _, modHas := modOpts["level"]; !modHas {
								patchOps = append(patchOps, fmt.Sprintf(`{"op":"remove","path":"%s/seLinuxOptions/level"}`, containerPath))
							}
						}
					} else if len(origOpts) > 0 {
						if _, hasLevel := origOpts["level"]; hasLevel && len(origOpts) == 1 {
							patchOps = append(patchOps, fmt.Sprintf(`{"op":"remove","path":"%s/seLinuxOptions"}`, containerPath))
						}
					}
				}
			} else if origSC != nil && !ok2 {
				patchOps = append(patchOps, fmt.Sprintf(`{"op":"remove","path":"%s"}`, containerPath))
			}
		}
	}

	checkContainerChanges("containers")
	checkContainerChanges("initContainers")
	checkContainerChanges("ephemeralContainers")

	if len(patchOps) == 0 {
		return jsonpatch.Patch{}, nil
	}

	patchJSON := "[" + strings.Join(patchOps, ",") + "]"
	patch, err := jsonpatch.DecodePatch([]byte(patchJSON))
	if err != nil {
		return nil, err
	}

	return patch, nil
}
