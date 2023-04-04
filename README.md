<p align="center">
  <a href="https://dragonflydb.io">
    <img  src="/.github/images/logo-full.svg"
      width="284" border="0" alt="Dragonfly">
  </a>
</p>

Dragonfly Operator is a Kubernetes operator used to deploy and manage [Dragonfly](https://dragonflydb.io/) instances inside your Kubernetes clusters.
Main features include:

- Automatic failover
- Scaling up/down the number of instances

You can find more information about Dragonfly in the [official documentation](https://dragonflydb.io/docs/).

## Getting Started

You’ll need a Kubernetes cluster to run against. You can use [KIND](https://sigs.k8s.io/kind) to get a local cluster for testing, or run against a remote cluster.
**Note:** Your controller will automatically use the current context in your kubeconfig file (i.e. whatever cluster `kubectl cluster-info` shows).

### Running on the cluster

1. Build your image with `IMG` tag:

```sh
export IMG=<some-registry>/dragonfly-operator:tag
make docker-build
```

2. Make the image available to the cluster:

> **Note**
>
> If you are using `kind`, You can load the image instead of pushing to a registry by running
>
> ```sh
> make docker-kind-load IMG=<some-registry>/dragonfly-operator:tag
> ```

```sh
make docker-push
```

2. Deploy the controller to the cluster with the image specified by `IMG`:

```sh
make deploy
```

3. Verify that the controller is running, and CRD's are installed:

```sh
➜ watch kubectl -n dragonfly-operator-system get pods                                                        
NAME                                                     READY   STATUS        RESTARTS   AGE
dragonfly-operator-controller-manager-7b88f9d84b-qnj4c   2/2     Running       0          13m
➜ kubectl get crds                             
NAME                         CREATED AT
dragonflies.dragonflydb.io   2023-04-03T13:29:18Z
```

3. Install a sample instance of Custom Resource:

```sh
kubectl apply -f config/samples/v1alpha1_dragonfly.yaml

```

4. Check the status of the instance:

```sh
kubectl describe dragonfly dragonfly-sample
```

### Uninstall CRDs

To delete the CRDs from the cluster:

```sh
make uninstall
```

### Undeploy controller

UnDeploy the controller from the cluster:

```sh
make undeploy
```

## License

Copyright 2023 DragonflyDB authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
