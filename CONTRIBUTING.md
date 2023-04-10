# Contributing

## Running on the cluster

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

## Uninstall CRDs

To delete the CRDs from the cluster:

```sh
make uninstall
```

## Undeploy controller

UnDeploy the controller from the cluster:

```sh
make undeploy
```
