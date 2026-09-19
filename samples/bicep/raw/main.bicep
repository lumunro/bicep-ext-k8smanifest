// Module: the raw passthrough example — two ConfigMaps rendered through
// `k8smanifest/Raw@v1` into one combined.yaml by the `k8smanifest`
// extension. No Namespace is created (the default one ships with every
// cluster). Reusable template under samples/bicep/ — the param files in
// samples/bicep/raw/ point at this file via `using`. Bicep config is shared
// from samples/bicep/bicepconfig.json (found by searching upward from the
// CLI working directory).
//
// Raw is for documents the typed bodies cannot express: a field the pinned
// API types don't know (alpha/feature-gated fields, CRDs outside the pinned
// set), a SOPS-encrypted Secret, or a manifest produced elsewhere. The body
// is strictly `{ document: <manifest> }` and apiVersion/kind come from the
// document, never from the type string.
//
// Generate (from samples/bicep/raw):
//   bicep local-deploy dev.bicepparam    (output in samples/manifests/raw/dev/)
// Apply:
//   kubectl apply -f samples/manifests/raw/dev/combined.yaml

targetScope = 'local'

@description('Directory (relative to the bicep CLI working directory) for the generated YAML manifest files')
param outputDir string = 'manifests'

@description('Existing namespace to deploy into (defaults to the cluster-provided `default`)')
param namespace string = 'default'

extension k8smanifest with {
  outputDir: outputDir
  combine: true
} as k8smanifest

// The manifest from a file: loadYamlContent feeds a ready-made document
// through the `document` property — hand-written YAML, another tool's
// output, an encrypted secret, anything the API server accepts.
resource fileConfig 'k8smanifest/Raw@v1' = {
  document: loadYamlContent('./app-config.yaml')
}

// The same wrapper with an inline document: `document` carries the full
// manifest, apiVersion/kind included.
resource inlineConfig 'k8smanifest/Raw@v1' = {
  document: {
    apiVersion: 'v1'
    kind: 'ConfigMap'
    metadata: {
      name: 'raw-inline-config'
      namespace: namespace
      labels: {
        'app.kubernetes.io/managed-by': 'bicep'
      }
    }
    data: {
      logLevel: 'info'
      maxConnections: '50'
    }
  }
}
