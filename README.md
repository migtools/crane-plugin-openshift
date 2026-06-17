# crane-plugin-openshift

OpenShift plugin for [crane](https://github.com/konveyor/crane) - handles OpenShift-specific resource transformations during cluster migrations.

## Features

This plugin provides transformations for OpenShift-specific resources including:

- **BuildConfigs**: Updates pull secrets and registry references
- **DeploymentConfigs**: Handles PVC renames and pod template transformations
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

```
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

## Development

### Running Tests

```bash
go test ./...
```

### Building

```bash
go build -o crane-plugin-openshift .
```
