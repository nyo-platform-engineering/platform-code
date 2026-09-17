# Development Applications

Give each workload a folder with `app.yaml` (Argo CD Application) and
`values.yaml` (deployment chart settings). List `<app>/app.yaml` in
`kustomization.yaml`; values files are not Kubernetes resources. The `dev-apps` Application registers this
layer after the platform Application definitions at sync wave 5.

Copy `../../examples/application.yaml` into `<app>/app.yaml`, set a unique Application name and its
Git source, and keep the Application in namespace `argocd`. Its destination
namespace is `dev`. Application source code and Dockerfiles remain in
`code/apps`. Deployment settings belong in `<app>/values.yaml` here and use
the reusable chart at `code/infrastructure/charts/deployment`.

`go-demo/app.yaml` deploys the Go service through that chart, using
`go-demo/values.yaml` for its image, security settings, probes, and HTTPRoute.
Build and import `local/go-demo:dev1` into k3d before syncing it. Add further
Applications to `kustomization.yaml` as workloads are created.
