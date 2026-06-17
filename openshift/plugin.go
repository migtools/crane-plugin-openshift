package openshift

import (
	"strconv"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/konveyor/crane-lib/transform"
	"github.com/konveyor/crane-lib/transform/util"
	"github.com/sirupsen/logrus"
)

const PluginVersion = "v0.1.0"

const (
	StripDefaultRBACFlag        = "strip-default-rbac"
	StripDefaultCABundleFlag    = "strip-default-cabundle"
	StripDefaultPullSecretsFlag = "strip-default-pull-secrets"
	PullSecretReplacementFlag   = "pull-secret-replacement"
	PVCRenameMapFlag            = "pvc-rename-map"
	RegistryReplacementFlag     = "registry-replacement"
)

var authorizationGroup = "authorization.openshift.io"

// OpenShiftTransformPlugin implements transform.Plugin for OpenShift-specific transformations.
type OpenShiftTransformPlugin struct {
	Log logrus.FieldLogger
}

func (o *OpenShiftTransformPlugin) Metadata() transform.PluginMetadata {
	return transform.PluginMetadata{
		Name:    "OpenShiftPlugin",
		Version: PluginVersion,
		OptionalFields: []transform.OptionalFields{
			{
				FlagName: StripDefaultRBACFlag,
				Help:     "Whether to strip default RBAC including builder and deployers serviceAccounts, roleBindings for admin, builders, and deployers (default: true)",
				Example:  "true",
			},
			{
				FlagName: StripDefaultCABundleFlag,
				Help:     "Whether to strip default CA Bundle (default: true)",
				Example:  "true",
			},
			{
				FlagName: StripDefaultPullSecretsFlag,
				Help:     "Whether to strip Pod and BuildConfig default pull secrets (beginning with builder/default/deployer-dockercfg-) that aren't replaced by the map param " + PullSecretReplacementFlag + " (default: true)",
				Example:  "true",
			},
			{
				FlagName: PullSecretReplacementFlag,
				Help:     "Map of pull secrets to replace in Pods and BuildConfigs while transforming in format secret1=destsecret1,secret2=destsecret2[...]",
				Example:  "default-dockercfg-h4n7g=default-dockercfg-12345,builder-dockercfg-abcde=builder-dockercfg-12345",
			},
			{
				FlagName: RegistryReplacementFlag,
				Help:     "Map of image registry paths to swap on transform, in the format original-registry1=target-registry1,original-registry2=target-registry2...",
				Example:  "docker-registry.default.svc:5000=image-registry.openshift-image-registry.svc:5000,docker.io/foo=quay.io/bar",
			},
			{
				FlagName: PVCRenameMapFlag,
				Help:     "A comma-separated list of colon separated pvc renames.",
				Example:  "old-pvc1-name:new-pvc1-name,old-pvc2-name:new-pvc2-name",
			},
		},
		RequestVersion:  []transform.Version{transform.V1},
		ResponseVersion: []transform.Version{transform.V1},
	}
}

func (o *OpenShiftTransformPlugin) Run(request transform.PluginRequest) (transform.PluginResponse, error) {
	u := request.Unstructured
	var patch jsonpatch.Patch
	whiteOut := false
	inputFields, err := parseOptionalFields(request.Extras)
	if err != nil {
		return transform.PluginResponse{}, err
	}

	if authorizationGroup == u.GetObjectKind().GroupVersionKind().GroupKind().Group {
		return transform.PluginResponse{
			Version:    string(transform.V1),
			IsWhiteOut: true,
			Patches:    patch,
		}, nil
	}

	switch u.GetKind() {
	case "Build":
		o.log().Info("found build, adding to whiteout")
		whiteOut = true
	case "ImageStream":
		namespace := u.GetNamespace()
		name := u.GetName()
		o.log().Warnf("ImageStream '%s/%s' detected - images from internal registry are NOT migrated automatically", namespace, name)
		o.log().Info("To migrate internal registry images, use tools like skopeo. Example: skopeo sync --src docker --dest docker SOURCE_REGISTRY/REPO DEST_REGISTRY/REPO")
		o.log().Info("For more information, see: https://github.com/migtools/crane/issues/452")
		whiteOut = true
	case "ImageStreamTag":
		o.log().Info("found ImageStreamTag sub-resource, adding to whiteout")
		whiteOut = true
	case "ImageTag":
		o.log().Info("found ImageTag sub-resource, adding to whiteout")
		whiteOut = true
	case "BuildConfig":
		o.log().Info("found build config, processing")
		patch, err = UpdateBuildConfig(u, inputFields)
	case "DeploymentConfig":
		o.log().Info("found deployment config, processing")
		patch, err = UpdateDeploymentConfig(u, inputFields)
	case "Pod":
		o.log().Info("found pod, processing update default pull secret")
		patch, err = UpdateDefaultPullSecrets(u, inputFields)
	case "Route":
		o.log().Info("found route, processing")
		patch, err = UpdateRoute(u)
	case "ServiceAccount":
		if inputFields.StripDefaultRBAC && (u.GetName() == "builder" || u.GetName() == "deployer") {
			whiteOut = true
		} else {
			o.log().Info("found service account, processing")
			patch, err = UpdateServiceAccount(u)
		}
	case "Secret":
		if inputFields.StripDefaultRBAC {
			if sa, ok := u.GetAnnotations()["kubernetes.io/service-account.name"]; ok && (sa == "builder" || sa == "deployer" || sa == "pipeline") {
				whiteOut = true
			}
		}
	case "RoleBinding":
		o.log().Info("found role binding, processing")
		if inputFields.StripDefaultRBAC && (u.GetName() == "admin" ||
			u.GetName() == "system:deployers" ||
			u.GetName() == "system:image-builders" ||
			u.GetName() == "system:image-pullers") {
			whiteOut = true
		} else {
			patch, err = UpdateRoleBinding(u)
		}
	case "ConfigMap":
		if inputFields.StripDefaultCABundle && u.GetName() == "openshift-service-ca.crt" {
			whiteOut = true
		}
	case "ClusterServiceVersion":
		if _, ok := u.GetLabels()["olm.copiedFrom"]; ok {
			o.log().Info("found copied ClusterServiceVersion, adding to whiteout")
			whiteOut = true
		}
	}

	if err != nil {
		return transform.PluginResponse{}, err
	}
	return transform.PluginResponse{
		Version:    string(transform.V1),
		IsWhiteOut: whiteOut,
		Patches:    patch,
	}, nil
}

func (o *OpenShiftTransformPlugin) log() logrus.FieldLogger {
	if o.Log != nil {
		return o.Log
	}
	return logrus.New()
}

type openshiftOptionalFields struct {
	StripDefaultRBAC        bool
	StripDefaultCABundle    bool
	StripDefaultPullSecrets bool
	PullSecretReplacement   map[string]string
	PVCRenameMap            map[string]string
	RegistryReplacement     map[string]string
}

func parseOptionalFields(extras map[string]string) (openshiftOptionalFields, error) {
	fields := openshiftOptionalFields{
		StripDefaultRBAC:        true,
		StripDefaultCABundle:    true,
		StripDefaultPullSecrets: true,
	}
	var err error
	if len(extras[StripDefaultRBACFlag]) > 0 {
		fields.StripDefaultRBAC, err = strconv.ParseBool(extras[StripDefaultRBACFlag])
		if err != nil {
			return fields, err
		}
	}
	if len(extras[StripDefaultCABundleFlag]) > 0 {
		fields.StripDefaultCABundle, err = strconv.ParseBool(extras[StripDefaultCABundleFlag])
		if err != nil {
			return fields, err
		}
	}
	if len(extras[StripDefaultPullSecretsFlag]) > 0 {
		fields.StripDefaultPullSecrets, err = strconv.ParseBool(extras[StripDefaultPullSecretsFlag])
		if err != nil {
			return fields, err
		}
	}
	if len(extras[PullSecretReplacementFlag]) > 0 {
		fields.PullSecretReplacement = transform.ParseOptionalFieldMapVal(extras[PullSecretReplacementFlag])
	}
	if len(extras[RegistryReplacementFlag]) > 0 {
		fields.RegistryReplacement = transform.ParseOptionalFieldMapVal(extras[RegistryReplacementFlag])
	}
	if len(extras[PVCRenameMapFlag]) > 0 {
		pvcMap, err := util.ProcessPVCMap(extras[PVCRenameMapFlag])
		if err != nil {
			return fields, err
		}
		fields.PVCRenameMap = pvcMap
	}
	return fields, nil
}
