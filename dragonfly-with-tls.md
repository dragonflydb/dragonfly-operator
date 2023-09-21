# Dragonfly With Server TLS

## Generate a Cert with Cert Manager

You can also skip this step and use the self-signed cert that is generated manually. The Operator expects
a relevant secret to be present with `tls.crt`, `tls.key` keys.

### Install Cert Manager

```sh
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.13.0/cert-manager.yaml
```

Create a TLS secret to be used

```sh
kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: dragonfly-sample
spec:
  # Secret names are always required.
  secretName: dragonfly-sample
  duration: 2160h # 90d
  renewBefore: 360h # 15d
  subject:
    organizations:
      - dragonfly-sample
  # The use of the common name field has been deprecated since 2000 and is
  # discouraged from being used.
  commonName: example.com
  privateKey:
    algorithm: RSA
    encoding: PKCS1
    size: 2048
  # At least one of a DNS Name, URI, or IP address is required.
  dnsNames:
    - dragonfly-sample.com
    - www.dragonfly-sample.com
  # Issuer references are always required.
  issuerRef:
    name: ca-issuer
    kind: Issuer
    group: cert-manager.io
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: ca-issuer
spec:
  selfSigned: {}
EOF
```

## Dragonfly Instance With TLS

Create a Dragonfly instance with one auth mechanism as its needed when TLS is set:

```sh
kubectl apply -f - <<EOF
apiVersion: dragonflydb.io/v1alpha1
kind: Dragonfly
metadata:
  name: dragonfly-sample
spec:
    env:
    - name: DFLY_PASSWORD
      value: "dragonfly"
    replicas: 3
    tlsSecretRef: dragonfly-sample
EOF
```

## Verify that the Dragonfly instance is running with the TLS cert

Create a redis container with the ca.crt

```sh
kubectl run -it --rm redis-cli --image=redis:7.0.10 --restart=Never --overrides='
{
    "spec": {
        "containers": [
            {
                "name": "redis-cli",
                "image": "redis:7.0.10",
                "tty": true,
                "stdin": true,
                "volumeMounts": [
                    {
                        "name": "ca-certs",
                        "mountPath": "/etc/ssl",
                        "readOnly": true
                    }
                ]
            }
        ],
        "volumes": [
            {
                "name": "ca-certs",
                "secret": {
                    "secretName": "dragonfly-sample",
                    "items": [
                        {
                            "key": "ca.crt",
                            "path": "ca.crt"
                        }
                    ]
                }
            }
        ]
    }
}' -- redis-cli -h dragonfly-sample.default -a dragonfly --tls --cacert /etc/ssl/ca.crt
```
