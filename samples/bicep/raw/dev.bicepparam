using './main.bicep'

// The raw passthrough example: two ConfigMaps (one from a file, one inline)
// through k8smanifest/Raw@v1 into the cluster-provided `default` namespace
// (no Namespace is created).
param namespace = 'default'
param outputDir = '../../manifests/raw/dev'
