import os
import yaml
import sys

os.makedirs('plugins/OpenShift', exist_ok=True)

if os.path.exists("index.yaml"):
    file = open("index.yaml","r")
    not_present = 1
    index = yaml.safe_load(file)
    for plugin in index['plugins']:
        if plugin['name'] == 'OpenShiftPlugin':
            not_present = 0
            break
    if not_present:
        index['plugins'].append({"name": "OpenShiftPlugin", "path": "https://github.com/%s/crane-plugins/raw/main/plugins/OpenShift/index.yaml"%sys.argv[2]})
    file = open("index.yaml","w")
    yaml.dump(index, file)
    file.close()

else:
    file = open("index.yaml","a+")

    index = yaml.safe_load(file)

    index = {}
    index['kind'] = 'PluginIndex'
    index['apiVersion'] = 'crane.konveyor.io/v1alpha1'
    index['plugins'] = []

    index['plugins'].append({"name": "OpenShiftPlugin", "path": "https://github.com/%s/crane-plugins/raw/main/plugins/OpenShift/index.yaml"%sys.argv[2]})

    yaml.dump(index, file)
    file.close()

# create or append in plugin index
if os.path.exists('plugins/OpenShift/index.yaml'):

    file = open("plugins/OpenShift/index.yaml","r")

    index = yaml.safe_load(file)

    index['versions'].append({})
    index['versions'][-1] = {
        'name': 'OpenShiftPlugin',
        'shortDescription': 'OpenShiftPlugin',
        'description': 'this is OpenShiftPlugin',
        'version': sys.argv[1],
        'binaries': [
            {
                'os': 'linux',
                'arch': 'amd64',
                'uri': "https://github.com/%s/releases/download/%s/amd64-linux-openshiftplugin-%s"%(sys.argv[3], sys.argv[1],sys.argv[1]),
            },
            {
                'os': 'darwin',
                'arch': 'amd64',
                'uri': "https://github.com/%s/releases/download/%s/amd64-darwin-openshiftplugin-%s"%(sys.argv[3], sys.argv[1],sys.argv[1]),
            },
            {
                'os': 'darwin',
                'arch': 'arm64',
                'uri': "https://github.com/%s/releases/download/%s/arm64-darwin-openshiftplugin-%s"%(sys.argv[3], sys.argv[1],sys.argv[1]),
            },
        ],
        'optionalFields': [
            {  
                'flagName': "strip-default-pull-secrets",
                'help':     "Whether to strip Pod and BuildConfig default pull secrets (beginning with builder/default/deployer-dockercfg-) that aren't replaced by the map param pull-secret-replacement",
                'example':  "true",
            },
            { 
                'flagName': "pull-secret-replacement",
                'help':     "Map of pull secrets to replace in Pods and BuildConfigs while transforming in format secret1=destsecret1,secret2=destsecret2[...]",
                'example':  "default-dockercfg-h4n7g=default-dockercfg-12345,builder-dockercfg-abcde=builder-dockercfg-12345",
            },
            { 
                'flagName': "registry-replacement",
                'help':     "Map of image registry paths to swap on transform, in the format original-registry1=target-registry1,original-registry2=target-registry2...",
                'example':  "docker-registry.default.svc:5000=image-registry.openshift-image-registry.svc:5000,docker.io/foo=quay.io/bar",
            },
        ]
    }

    file = open("plugins/OpenShift/index.yaml","w")

    yaml.dump(index, file)
    file.close()
    
else:
    file = open("plugins/OpenShift/index.yaml","a+")

    index = yaml.safe_load(file)

    index = {}
    index['kind'] = 'Plugin'
    index['apiVersion'] = 'crane.konveyor.io/v1alpha1'
    index['versions'] = []

    index['versions'].append({})
    index['versions'][0] = {
        'name': 'OpenShiftPlugin',
        'shortDescription': 'OpenShiftPlugin',
        'description': 'this is OpenShiftPlugin',
        'version': sys.argv[1],
        'binaries': [
            {
                'os': 'linux',
                'arch': 'amd64',
                'uri': "https://github.com/%s/releases/download/%s/amd64-linux-openshiftplugin-%s"%(sys.argv[3], sys.argv[1],sys.argv[1]),
            },
            {
                'os': 'darwin',
                'arch': 'amd64',
                'uri': "https://github.com/%s/releases/download/%s/amd64-darwin-openshiftplugin-%s"%(sys.argv[3], sys.argv[1],sys.argv[1]),
            },
            {
                'os': 'darwin',
                'arch': 'arm64',
                'uri': "https://github.com/%s/releases/download/%s/arm64-darwin-openshiftplugin-%s"%(sys.argv[3], sys.argv[1],sys.argv[1]),
            },
        ],
        'optionalFields': [
            {  
                'flagName': "strip-default-pull-secrets",
                'help':     "Whether to strip Pod and BuildConfig default pull secrets (beginning with builder/default/deployer-dockercfg-) that aren't replaced by the map param pull-secret-replacement",
                'example':  "true",
            },
            { 
                'flagName': "pull-secret-replacement",
                'help':     "Map of pull secrets to replace in Pods and BuildConfigs while transforming in format secret1=destsecret1,secret2=destsecret2[...]",
                'example':  "default-dockercfg-h4n7g=default-dockercfg-12345,builder-dockercfg-abcde=builder-dockercfg-12345",
            },
            { 
                'flagName': "registry-replacement",
                'help':     "Map of image registry paths to swap on transform, in the format original-registry1=target-registry1,original-registry2=target-registry2...",
                'example':  "docker-registry.default.svc:5000=image-registry.openshift-image-registry.svc:5000,docker.io/foo=quay.io/bar",
            },
        ]
    }
    
    yaml.dump(index, file)
    file.close()