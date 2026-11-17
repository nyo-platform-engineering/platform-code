# Argo CD resource sizing

The application controller and repo-server were OOMKilled during initial sync
with 256 MiB limits. Their idle usage does not capture cluster discovery,
manifest generation, or the memory used by Helm/Git child processes.

| Component | Memory request | Memory limit |
| --- | --- | --- |
| Application controller | 512 MiB | 1 GiB |
| Repo-server | 256 MiB | 1 GiB |

The controller uses five status workers and two operation workers. Repo-server
allows two concurrent manifest generations. These settings trade some sync speed
for lower startup memory peaks. Other Argo CD components and AAT workload resource
classes retain their existing allocations.

These are initial lab budgets, not measured peak requirements. After a full sync,
check `kubectl --kubeconfig .state/kubeconfig top pods -n argocd` and container
restart reasons before reducing them. The chart and settings are owned by the
Argo CD Application in Git; bootstrap seeds only a missing installation.
