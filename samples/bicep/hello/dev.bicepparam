using './main.bicep'

// The minimal example app: a generic HTTP server (nginx) into the
// cluster-provided `default` namespace (no Namespace is created).
param namespace = 'default'
param appName = 'hello'
param image = 'nginx:alpine'
param containerPort = 80
param replicas = 1
param outputDir = '../../manifests/hello/dev'
