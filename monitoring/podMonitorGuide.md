# PodMonitor

When using Dragonfly operator, sometimes there may be a need to monitor the dragonfly instances. PodMonitors can be of great use here. In this doc, we will see how we can configure PodMonitor resource to monitor dragonfly instances.

## Install Prometheus Operator

First make sure you have all the prometheus crds installed in your cluster. Here we are going to install prometheus operator.

```bash
LATEST=$(curl -s https://api.github.com/repos/prometheus-operator/prometheus-operator/releases/latest | jq -cr .tag_name)
curl -sL https://github.com/prometheus-operator/prometheus-operator/releases/download/${LATEST}/bundle.yaml | kubectl create -f -
```

## Create prerequisite resources

Now that we have installed the operator, we can create `prometheus` resources. If you have RBAC enabled, create necessary `serviceaccount`, `clusterrole` and `clusterrolebinding` resources first.

```bash
kubectl apply -f monitoring/promServiceAccount.yaml
kubectl apply -f monitoring/promClusterRole.yaml
kubectl apply -f monitoring/promClusterBinding.yaml
```

This will allow prometheus to scrape data from dragonfly resources. Once we
configured the RBAC, we can now create a PodMonitor resource.

```bash
kubectl apply -f monitoring/podMonitor.yaml
```

Note that we must specify `app` label under the `matchLabels` (`selector`) field. It is used to target the desired dragonfly instances. The value of `app` label is the name of your dragonfly resource name (in this case, `dragonfly-sample`).

Dragonfly resources expose a port named `admin` and you can use it as the endpoint for PodMonitor.

## Create Dragonfly resource

We can now create dragonfly resources and Prometheus will automatically scrap and monitor the created resources.

```bash
kubectl apply -f config/samples/v1alpha1_dragonfly.yaml
```

If you want to view or query scraped data in the localhost, run the below command:

```bash
kubectl port-forward prometheus-prometheus-0 9090:9090
```

Now go to `localhost:9090`. You'll see the prometheus dashboard.
