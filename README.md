<p align="center">
  <a href="https://dragonflydb.io">
    <img  src="/.github/images/logo-full.svg"
      width="284" border="0" alt="Dragonfly">
  </a>
</p>

Dragonfly Operator is a Kubernetes operator used to deploy and manage [Dragonfly](https://dragonflydb.io/) instances inside your Kubernetes clusters.
Main features include:

- Automatic failover
- Scaling horizontally and vertically with custom rollout strategy
- Authentication and server TLS
- Automatic snapshots to PVCs and S3
- Monitoring with Prometheus and Grafana
- Comprehensive configuration options

You can find more information about Dragonfly in the [official documentation](https://dragonflydb.io/docs/).
There is also a dedicated [Dragonfly Operator section](https://www.dragonflydb.io/docs/managing-dragonfly/operator/installation)
that contains more details and examples on how to use the operator.

## Installation

Make sure to have your Kubernetes cluster up and running. Dragonfly Operator can be installed by running

```sh
# Install the CRD and Operator
kubectl apply -f https://raw.githubusercontent.com/dragonflydb/dragonfly-operator/main/manifests/dragonfly-operator.yaml
```

By default, the operator will be installed in the `dragonfly-operator-system` namespace.

## Usage

### Creating a Dragonfly instance

To create a sample Dragonfly instance, you can run the following command:

```sh
kubectl apply -f https://raw.githubusercontent.com/dragonflydb/dragonfly-operator/main/config/samples/v1alpha1_dragonfly.yaml
```

This will create a Dragonfly instance with 3 replicas. You can check the status of the instance by running

```sh
kubectl describe dragonflies.dragonflydb.io dragonfly-sample
```

A service of the form `<dragonfly-name>.<namespace>.svc.cluster.local` will be created, that selects the master instance. You can use this service to connect to the cluster. As pods are added/removed, the service will automatically update to point to the new master.

#### Connecting with `redis-cli`

To connect to the cluster using `redis-cli`, you can run:

```sh
kubectl run -it --rm --restart=Never redis-cli --image=redis:7.0.10 -- redis-cli -h dragonfly-sample.default
```

This will create a temporary pod that runs `redis-cli` and connects to the cluster. After pressing `shift + R`, You can then run Redis commands as
usual. For example, to set a key and get it back, you can run

```sh
If you don't see a command prompt, try pressing enter.
dragonfly-sample.default:6379> GET 1
(nil)
dragonfly-sample.default:6379> SET 1 2
OK
dragonfly-sample.default:6379> GET 1
"2"
dragonfly-sample.default:6379> exit
pod "redis-cli" deleted
```

### Scaling up/down the number of replicas

To scale up/down the number of replicas, you can edit the `spec.replicas` field in the Dragonfly instance. For example, to scale up to 5 replicas, you can run

```sh
kubectl patch dragonfly dragonfly-sample --type merge -p '{"spec":{"replicas":5}}'
```

### Vertically scaling the instance

To vertically scale the instance, you can edit the `spec.resources` field in the Dragonfly instance. For example, to increase the memory limit to 2GiB, you can run

```sh
kubectl patch dragonfly dragonfly-sample --type merge -p '{"spec":{"resources":{"requests":{"memory":"1Gi"},"limits":{"memory":"2Gi"}}}}'
```

### Configuring instance authentication

To add authentication to the dragonfly pods, you either set the `DFLY_requirepass` environment variable, or add the `--requirepass` argument.

### Deleting a Dragonfly instance

To delete a Dragonfly instance, you can run

```sh
kubectl delete dragonfly dragonfly-sample
```

This will automatically delete all the resources (i.e pods and services) associated with the instance.

### Uninstalling the operator

To uninstall the operator, you can run

```sh
kubectl delete -f https://raw.githubusercontent.com/dragonflydb/dragonfly-operator/main/manifests/dragonfly-operator.yaml
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
