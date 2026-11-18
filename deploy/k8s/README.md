# FlowBridge Kubernetes 部署

## 构建镜像

在仓库根目录执行。先把 `REPOSITORY` 换成集群可以拉取的镜像仓库地址。

```bash
REPOSITORY=registry.example.com/team/flowbridge
TAG=$(git rev-parse --short HEAD)
docker buildx build --platform linux/amd64 --tag "$REPOSITORY:$TAG" --push .
```

将 [kustomization.yaml](kustomization.yaml) 的 `images.newName` 和 `images.newTag` 改为上面的仓库地址和标签。当前 Dockerfile 构建 amd64 二进制，Pod 会调度到 amd64 节点。私有仓库可在 Deployment 的 Pod 配置中引用命名空间里已有的 `imagePullSecrets`。

## 部署与检查

默认使用当前 kubeconfig 的上下文和命名空间。`kubectl get storageclass` 可查看集群的存储类。如果默认类不适用，在 [pvc.yaml](pvc.yaml) 的 `spec.storageClassName` 指定支持 `ReadWriteOnce`、适用于 SQLite WAL 的文件系统存储。初始容量为 `10Gi`。使用块存储提供的文件系统或节点本地盘，SQLite WAL 要求支持本地文件锁和共享内存。 [SQLite WAL 文档](https://www.sqlite.org/wal.html)

```bash
kubectl kustomize deploy/k8s
kubectl apply -k deploy/k8s
kubectl rollout status deployment/flowbridge --timeout=5m
kubectl get deployment/flowbridge service/flowbridge pvc/flowbridge-data
```

需要指定命名空间时，给部署和查询命令加上 `-n <namespace>`。集群内通过 `http://flowbridge:8080` 访问同命名空间的服务。本地可以转发端口，在另一终端验证 `/readyz`。

```bash
kubectl port-forward service/flowbridge 7070:8080
```

```bash
curl -fsS http://127.0.0.1:7070/readyz
kubectl logs deployment/flowbridge --tail=100
```

## 存储与更新

- 单副本使用 `Recreate` 更新。更新时旧 Pod 先退出，服务会有短暂中断。保持副本数为 `1`，通过工作负载更新镜像和配置。 [Kubernetes Deployment 更新策略](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#recreate-deployment)
- 整个 `/app/data` 挂载在 `flowbridge-data` PVC，任务、SQLite WAL 和待发送回调随卷保留。从 Docker Compose 迁移时，新 PVC 是空的。如需保留历史任务和待发回调，在切换流量前从旧卷做一致性备份并恢复到新卷。
- `kustomization.yaml` 可调整 P0 地址、工作流并发和回调并发。两类并发默认各为 `8`，应用从集群发起到 P0 和 `notify_url` 的请求。更新镜像标签或配置后重新执行 `kubectl apply -k deploy/k8s`。
