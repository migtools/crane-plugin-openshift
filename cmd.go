package main

import (
	"strconv"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/konveyor/crane-lib/transform"
	"github.com/konveyor/crane-lib/transform/cli"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	logger        logrus.FieldLogger
	roleBindingGK = schema.GroupKind{Group: "authorization.openshift.io", Kind: "RoleBinding"}
)

const Version = "v0.0.3"

const (
	// flags
	StripDefaultRBACFlag        = "strip-default-rbac"
	StripDefaultCABundleFlag    = "strip-default-cabundle"
	StripDefaultPullSecretsFlag = "strip-default-pull-secrets"
	PullSecretReplacementFlag   = "pull-secret-replacement"
	RegistryReplacementflag     = "registry-replacement"
)

func main() {
	logger = logrus.New()
	// TODO: add plumbing for logger in the cli-library and instantiate here
	fields := []transform.OptionalFields{
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
			FlagName: RegistryReplacementflag,
			Help:     "Map of image registry paths to swap on transform, in the format original-registry1=target-registry1,original-registry2=target-registry2...",
			Example:  "docker-registry.default.svc:5000=image-registry.openshift-image-registry.svc:5000,docker.io/foo=quay.io/bar",
		},
	}
	cli.RunAndExit(cli.NewCustomPlugin("OpenShiftPlugin", Version, fields, Run))
}

type openshiftOptionalFields struct {
	StripDefaultRBAC        bool
	StripDefaultCABundle    bool
	StripDefaultPullSecrets bool
	PullSecretReplacement   map[string]string
	RegistryReplacement     map[string]string
}

func getOptionalFields(extras map[string]string) (openshiftOptionalFields, error) {
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
	if len(extras[RegistryReplacementflag]) > 0 {
		fields.RegistryReplacement = transform.ParseOptionalFieldMapVal(extras[RegistryReplacementflag])
	}
	return fields, nil
}

func Run(request transform.PluginRequest) (transform.PluginResponse, error) {
	u := request.Unstructured
	var patch jsonpatch.Patch
	whiteOut := false
	inputFields, err := getOptionalFields(request.Extras)
	if err != nil {
		return transform.PluginResponse{}, err
	}

	switch u.GetKind() {
	case "Build":
		logger.Info("found build, adding to whiteout")
		whiteOut = true
	case "BuildConfig":
		logger.Info("found build config, processing")
		patch, err = UpdateBuildConfig(u, inputFields)
	case "Pod":
		logger.Info("found pod, processing update default pull secret")
		patch, err = UpdateDefaultPullSecrets(u, inputFields)
	case "Route":
		logger.Info("found route, processing")
		patch, err = UpdateRoute(u)
	case "ServiceAccount":
		if inputFields.StripDefaultRBAC && (u.GetName() == "builder" || u.GetName() == "deployer") {
			whiteOut = true
		} else {
			logger.Info("found service account, processing")
			patch, err = UpdateServiceAccount(u)
		}
	case "Secret":
		if inputFields.StripDefaultRBAC {
			if sa, ok := u.GetAnnotations()["kubernetes.io/service-account.name"]; ok && (sa == "builder" || sa == "deployer" || sa == "pipeline") {
				whiteOut = true
			}
		}
	case "RoleBinding":
		logger.Info("found role binding, processing")
		if roleBindingGK == u.GetObjectKind().GroupVersionKind().GroupKind() {
			if inputFields.StripDefaultRBAC && (u.GetName() == "admin" ||
				u.GetName() == "system:deployers" ||
				u.GetName() == "system:image-builders" ||
				u.GetName() == "system:image-pullers") {
				whiteOut = true
			} else {
				patch, err = UpdateRoleBinding(u)
			}
		}
	case "ConfigMap":
		if inputFields.StripDefaultCABundle && u.GetName() == "openshift-service-ca.crt" {
			whiteOut = true
		}
	case "ClusterServiceVersion":
		if _, ok := u.GetLabels()["olm.copiedFrom"]; ok {
			logger.Info("found copied ClusterServiceVersion, adding to whiteout")
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
