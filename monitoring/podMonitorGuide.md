# PodMonitor

When using Dragonfly operator, sometimes there may be a need to monitor the dragonfly instances. PodMonitors can be of great use here. In this doc, we will see how we can configure PodMonitor resource to monitor dragonfly instances.

## Step 1:
First make sure you have all the prometheus crds installed in your cluster. Here we are going to install prometheus operator.

```
LATEST=$(curl -s https://api.github.com/repos/prometheus-operator/prometheus-operator/releases/latest | jq -cr .tag_name)
curl -sL https://github.com/prometheus-operator/prometheus-operator/releases/download/${LATEST}/bundle.yaml | kubectl create -f -
```

## Step 2:
Now that we have installed the operator, we can create `prometheus` resources. If you have RBAC enabled, create necessary `serviceaccount`, `clusterrole` and `clusterrolebinding` resources first.

ServiceAccount -
```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: prometheus
```

Create ClusterRole -
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: prometheus
rules:
- apiGroups: [""]
  resources:
  - nodes
  - nodes/metrics
  - services
  - endpoints
  - pods
  verbs: ["get", "list", "watch"]
- apiGroups:
  - networking.k8s.io
  resources:
  - ingresses
  verbs: ["get", "list", "watch"]
- nonResourceURLs: ["/metrics"]
  verbs: ["get"]
```

ClusterRoleBinding -
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: prometheus
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: prometheus
subjects:
- kind: ServiceAccount
  name: prometheus
  namespace: default
```

Once you configured the RBAC, create a ServiceMonitor resource -
```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: dragonfly-monitor
  labels:
    group: dragonfly
    app: dragonfly-monitor
spec:
  selector:
    matchLabels:
      app: dragonfly-sample
  podMetricsEndpoints:
  - port: admin
```

Note that we must specify `app` label under the `matchLabels` (`selector`) field. It is used to target the desired dragonfly instances. The value of `app` label is the name of your dragonfly resource name (in this case, `dragonfly-sample`).

Dragonfly resources expose a port named `admin` and you can use it as the endpoint for PodMonitor.

## Step 3:
We can now create dragonfly resources and Prometheus will automatically scrap and monitor the created resources.

```
kubectl apply -f config/samples/v1alpha1_dragonfly.yaml
```

If you want to view or query scraped data in the localhost, run the below command - 
```
kubectl port-forward prometheus-prometheus-0 9090:9090
```
Now go to `localhost:9090`. You'll see the prometheus dashboard.
