# crane-plugin-openshift

OpenShift plugin for [crane](https://github.com/konveyor/crane) - handles OpenShift-specific resource transformations during cluster migrations.

## Features

This plugin provides transformations for OpenShift-specific resources including:

- **BuildConfigs**: Updates pull secrets and registry references
- **DeploymentConfigs**: Handles PVC renames and can optionally convert them to Kubernetes Deployments
- **Routes**: Removes auto-generated hostnames
- **ServiceAccounts**: Strips default secrets and pull secrets
- **RoleBindings**: Removes namespace references for ServiceAccount subjects
- **ImageStreams**: Detects usage (see limitations below)
- Automatic whiteout of OpenShift-specific resources (Builds, ImageStreamTags, ImageTags)
- Optional stripping of default RBAC, CA bundles, and pull secrets

## Limitations

### Internal Image Registry Migration

**Important**: This plugin does **NOT** migrate container images stored in OpenShift's internal image registry.

When the plugin detects `ImageStream` resources during migration, it will log warnings like:

```text
WARNING: ImageStream 'my-namespace/my-app' detected - images from internal registry are NOT migrated automatically
INFO: To migrate internal registry images, use tools like skopeo. Example: skopeo sync --src docker --dest docker SOURCE_REGISTRY/REPO DEST_REGISTRY/REPO
```

#### Why aren't images migrated?

- Crane focuses on Kubernetes resource manifests (YAML)
- Container images are data stored separately in container registries
- Internal registry images require specialized tools for migration

#### How to migrate images manually

Use [skopeo](https://github.com/containers/skopeo) to copy images between registries:

```bash
# Example: Copy a single image
skopeo copy \
  docker://source-registry.example.com:5000/namespace/image:tag \
  docker://dest-registry.example.com:5000/namespace/image:tag

# Example: Sync multiple images
skopeo sync \
  --src docker --dest docker \
  source-registry.example.com:5000/namespace \
  dest-registry.example.com:5000/namespace
```

For more details, see [crane issue #452](https://github.com/migtools/crane/issues/452).

## Usage

This plugin is used automatically by crane when processing OpenShift resources. Optional flags can be configured:

- `--strip-default-rbac` (default: true) - Strip default RBAC resources
- `--strip-default-cabundle` (default: true) - Strip default CA bundle ConfigMaps  
- `--strip-default-pull-secrets` (default: true) - Strip default pull secrets
- `--pull-secret-replacement` - Map of pull secret replacements
- `--registry-replacement` - Map of registry path replacements
- `--pvc-rename-map` - Map of PVC name changes
- `--convert-deploymentconfigs` (default: false) - Convert `apps.openshift.io/v1` DeploymentConfigs to `apps/v1` Deployments

## DeploymentConfig conversion

DeploymentConfig conversion is disabled by default so that existing OpenShift-to-OpenShift migrations keep their current behavior. Enable it with `convert-deploymentconfigs=true` when the target should use Kubernetes Deployments.

The conversion requires Crane `v0.11.0-alpha.1` or newer. Older Crane versions honor the source whiteout but ignore the generated resource, which can remove a DeploymentConfig without creating its replacement.

On successful conversion, the plugin:

- whiteouts the source DeploymentConfig;
- returns one `apps/v1` Deployment through `NewResources`;
- applies `pvc-rename-map` to PVC references;
- removes SCC-injected security context values;
- records dropped behavior in logs and in the `crane.konveyor.io/deploymentconfig-conversion-warnings` annotation.

If conversion is unsafe, the plugin keeps the DeploymentConfig and adds these annotations:

- `crane.konveyor.io/deploymentconfig-conversion-status: skipped`
- `crane.konveyor.io/deploymentconfig-conversion-reason: <reason>`

### Field support

| DeploymentConfig field or behavior | Conversion |
|---|---|
| Name, namespace, labels, annotations | Preserved |
| Replicas, including zero | Preserved |
| Selector | Converted to `spec.selector.matchLabels` |
| Pod template | Preserved |
| `minReadySeconds`, `revisionHistoryLimit`, `paused` | Preserved |
| Rolling strategy | Converted to `RollingUpdate` |
| `maxSurge`, `maxUnavailable` | Preserved |
| Recreate strategy | Preserved |
| ConfigChange trigger | Removed; Deployment rolls out when its pod template changes |
| ImageChange trigger | Removed with a warning |
| Rolling period, polling interval, and timeout | Removed with a warning |
| Recreate timeout | Removed with a warning |
| Deployer resources, labels, annotations, active deadline | Removed with a warning |
| Custom strategy or `customParams` | Conversion skipped |
| Pre, mid, or post lifecycle hooks | Conversion skipped |
| `spec.test: true` | Conversion skipped |
| Missing pod template or invalid selector | Conversion skipped |

A DeploymentConfig without a ConfigChange trigger has manual rollout semantics. A Deployment rolls out automatically when its pod template changes, so the plugin records this difference as a warning.

ImageChange triggers are not portable Kubernetes behavior. OpenShift can provide similar behavior through the `image.openshift.io/triggers` annotation, but this plugin does not generate that annotation because it has no effect on a non-OpenShift target.

Run the Kubernetes plugin before the OpenShift plugin. Generated Deployments are not sent back through an earlier pipeline stage, so the OpenShift plugin performs the required metadata and security-context cleanup itself.

The source DeploymentConfig and an existing Deployment can have the same namespace and name because their GVKs differ. The generated Deployment would collide with that existing Deployment. Crane must reject this case as tracked in [migtools/crane#1035](https://github.com/migtools/crane/issues/1035).

## Development

For more information about developing crane plugins, see [crane-plugins](https://github.com/migtools/crane-plugins).

### Running Tests

```bash
go test ./...
```

### Building

```bash
go build -o crane-plugin-openshift .
```
