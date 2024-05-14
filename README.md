# Multicluster Cert Manager Addon

This is a proof of concept. Do not use.

## Prereqs

- RHACM Hub Cluster
- At least 1 managed cluster imported/deployed with the RHACM Cluster

## Deploying the addon

1. Deploy the Cert-Manager Certificate and CertificateRequest CRDs in the managed cluster

    ~~~sh
    export KUBECONFIG=/path/to/managed-cluster/kubeconfig
    oc create -f https://raw.githubusercontent.com/openshift/cert-manager-operator/cert-manager-1.13/bundle/manifests/cert-manager.io_certificates.yaml
    oc create -f https://raw.githubusercontent.com/openshift/cert-manager-operator/cert-manager-1.13/bundle/manifests/cert-manager.io_certificaterequests.yaml
    ~~~

2. Deploy cert-manager operator in the Hub cluster.

3. Configure a ClusterIssuer in the hub cluster.

    ~~~sh
    export KUBECONFIG=/path/to/hub/kubeconfig
    cat <<EOF | oc apply -f -
    apiVersion: cert-manager.io/v1
    kind: Issuer
    metadata:
      name: selfsigned-issuer
      namespace: cert-manager
    spec:
      selfSigned: {}
    EOF
    cat <<EOF | oc apply -f -
    apiVersion: cert-manager.io/v1
    kind: Certificate
    metadata:
      name: selfsigned-ca
      namespace: cert-manager
    spec:
      isCA: true
      commonName: selfsigned-ca
      secretName: selfsigned-ca-root-secret
      privateKey:
        algorithm: ECDSA
        size: 256
      issuerRef:
        name: selfsigned-issuer
        kind: Issuer
        group: cert-manager.io
    EOF
    cat <<EOF | oc apply -f -
    apiVersion: cert-manager.io/v1
    kind: ClusterIssuer
    metadata:
      name: ca-issuer
    spec:
      ca:
        secretName: selfsigned-ca-root-secret
    EOF
    ~~~

4. Run the addon in the hub cluster (At this point, I'm running the addon in my laptop using the Hub's kubeconfig):

    ~~~sh
    export KUBECONFIG=/path/to/hub/kubeconfig
    go run main.go
    ~~~

5. Create a certificate in the managed cluster:

    ~~~sh
    export KUBECONFIG=/path/to/managed-cluster/kubeconfig
    cat <<EOF | oc apply -f -
    apiVersion: cert-manager.io/v1
    kind: Certificate
    metadata:
      name: apiserver-tls
    spec:
      secretName: apiserver-tls-cert
      duration: 12h
      renewBefore: 59m
      subject:
        organizations:
          - openshift
      commonName: api.mycluster.example.com
      isCA: false
      privateKey:
        algorithm: RSA
        encoding: PKCS1
        size: 2048
      usages:
        - server auth
      dnsNames:
        - api.mycluster.example.com
      uris:
        - spiffe://cluster.local/ns/openshift-config/sa/api
      ipAddresses:
        - 192.168.1.10
      issuerRef:
        name: ca-issuer
        kind: ClusterIssuer
        group: cert-manager.io
    EOF
    ~~~