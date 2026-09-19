// Module: the minimal example — one Deployment + one Service for a single
// app into the existing `default` namespace, rendered into one
// combined.yaml by the `k8s` extension. No Namespace is created (the
// default one ships with every cluster). Reusable template under
// samples/bicep/ — the param files in samples/bicep/hello/ point at this
// file via `using`. Bicep config is shared from
// samples/bicep/bicepconfig.json (found by searching upward from the CLI
// working directory).
//
// Generate (from samples/bicep/hello):
//   bicep local-deploy dev.bicepparam    (output in samples/manifests/hello/dev/)
// Apply:
//   kubectl apply -f samples/manifests/hello/dev/combined.yaml

targetScope = 'local'

@description('Directory (relative to the bicep CLI working directory) for the generated YAML manifest files')
param outputDir string = 'manifests'

@description('Existing namespace to deploy into (defaults to the cluster-provided `default`)')
param namespace string = 'default'

@description('Application name — used for all resource names and the app label (must be DNS-1123-safe, unique per namespace)')
param appName string

@description('Container image, e.g. nginx:1.27-alpine')
param image string

@description('TCP port the container listens on')
param containerPort int

@description('Replicas to run')
param replicas int = 1

extension k8smanifest with {
  outputDir: outputDir
  combine: true
} as k8smanifest

var commonLabels = {
  app: appName
  'app.kubernetes.io/managed-by': 'bicep'
}

resource deployment 'apps/Deployment@v1' = {
  metadata: {
    name: appName
    namespace: namespace
    labels: commonLabels
  }
  spec: {
    replicas: replicas
    selector: {
      matchLabels: {
        app: appName
      }
    }
    template: {
      metadata: {
        labels: commonLabels
      }
      spec: {
        containers: [
          {
            name: appName
            image: image
            ports: [
              {
                containerPort: containerPort
              }
            ]
            readinessProbe: {
              httpGet: {
                path: '/'
                port: containerPort
              }
              initialDelaySeconds: 3
              periodSeconds: 5
              failureThreshold: 3
            }
            livenessProbe: {
              httpGet: {
                path: '/'
                port: containerPort
              }
              initialDelaySeconds: 10
              periodSeconds: 10
              failureThreshold: 3
            }
            resources: {
              requests: {
                cpu: '100m'
                memory: '128Mi'
              }
              limits: {
                cpu: '500m'
                memory: '256Mi'
              }
            }
          }
        ]
      }
    }
  }
}

resource service 'core/Service@v1' = {
  metadata: {
    name: appName
    namespace: namespace
    labels: commonLabels
  }
  spec: {
    selector: {
      app: appName
    }
    ports: [
      {
        name: 'http'
        port: 80
        targetPort: containerPort
      }
    ]
  }
}
