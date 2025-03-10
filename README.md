# Kubernetes Virtual Kubelet with Perian Sky Platform

This project integrates a Virtual Kubelet with the Perian Sky Platform, enabling seamless execution of Kubernetes workloads in a serverless, GPU-powered environment. By leveraging the power of the Virtual Kubelet, Kubernetes users can now run their workloads as jobs on the Perian Sky Platform, which provides a highly scalable, cost-effective, and flexible serverless infrastructure for GPU-accelerated tasks.

### Key Features:

- **Effortless Integration**: Transform Pods running on the Virtual Kubelet into Perian Sky jobs with minimal configuration.
- **Serverless GPU Environment**: Utilize Perian Sky's affordable GPU services for running AI, ML, and other resource-intensive workloads without worrying about managing infrastructure.
- **Seamless Kubernetes Compatibility**: Continue using your existing Kubernetes infrastructure with minimal disruption, while taking advantage of the scalable GPU capabilities offered by Perian Sky.

This integration enables you to **extend the Kubernetes API** to interact with the Perian Sky platform, automatically managing the scheduling and execution of jobs in a serverless GPU environment. By utilizing this approach, you can efficiently manage resources and achieve cost-effective scaling of your GPU workloads.

## Status: Experimental

This provider is currently in the experimental stages. Contributions welcome!

## Quick Start

Follow these steps to quickly get started with running the Virtual Kubelet and leveraging the Perian Sky Platform for GPU-powered workloads.

### 1. Clone the Repository

Clone the repository that contains the Virtual Kubelet integration code:

```bash
git clone https://github.com/Perian-io/perian-virtual-kubelet.git
cd perian-virtual-kubelet
```

### 2. Build the virtual kubelet container

```bash
make build-docker
```

### 3. Create a Perian config yaml file

```yaml
PerianServerURL: "https://api.perian.cloud"
PerianOrg: "perian-org"
PerianAuthToken: "perian-bearer-auth-token"
KubeletPort: "8080"
NodeName: "perian-vk-node"
InternalIP: "127.0.0.1"
KubernetesSecretName: "my-docker-secret"
```

### 4. Create a Pod definition file to deploy Virtual Kubelet

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: perian-virtual-kubelet
  namespace: kube-system
spec:
  containers:
    - name: perian-virtual-kubelet
      image: perian-virtual-kubelet:latest
      imagePullPolicy: Never
      env:
        - name: CONFIG
          value: "/app/config/perian.yaml"
        - name: POD_IP
          valueFrom:
            fieldRef:
              fieldPath: status.podIP
      ports:
        - containerPort: 8080
      volumeMounts:
        - name: config-volume
          mountPath: "/app/config"
  volumes:
    - name: config-volume
      configMap:
        name: my-config
```

Notice: add the path to the Perian configuration file to the CONFIG environment variable.

### 5. Deploy the Pod to your cluster

```bash
kubectl apply -f virtualkubelet_pod.yaml
```

### 6. Add node name to your Pod definition to select Virtual Kubelet

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
  namespace: default
spec:
  nodeName: "node-name"
  ...
```

### 6. Deploy your Pod to the Perian Sky Platform

```bash
kubectl apply -f pod.yaml
```

### Optional: Adding docker registry credentials

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: docker-secret
  namespace: default
type: Opaque
data:
  registryURL: "base64_encoded_url"
  username: "base64_encoded_username"
  password: "base64_encoded_password"
```
