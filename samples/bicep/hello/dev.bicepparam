using './main.bicep'

// The minimal example app: mendhak/http-https-echo (echoes request data as
// JSON) into the cluster-provided `default` namespace (no Namespace is
// created).
param namespace = 'default'
param appName = 'hello'
param image = 'mendhak/http-https-echo:41'
param containerPort = 8080
param replicas = 1
param outputDir = '../../manifests/hello/dev'
