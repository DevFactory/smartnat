# User's guide

## Introduction

To use SmartNat as a user, you have to create a Mapping object definition and send it to kubernetes API endpoint, like any other object like Service or Deployment.

To create a Mapping object, you have to prepare the following information:

* What is the service name you want to expose? Mapping exposes a Service by name existing in the same namespace.
* Which ports of the Service you want to expose? You can expose only a subset of what the Service provides.
* Which external IP and port you want to use? Currently, these values are provided by the cluster operator/admin responsible for running SmartNat.
* Do you want to limit access to your service to some client IP addresses?

## Configuration schema

To create the Mapping, use YAML schema like below

```yaml
apiVersion: smartnat.aureacentral.com/v1alpha1 # this is constant
kind: Mapping
metadata:
  name: mapping-sample             # your Mapping name
spec:
  addresses:                       # list; at least 1 address is required
  - "10.237.94.10"
  - "172.17.0.1"
  allowedSources:                  # list; if no CIDR prefixes are given, the default "0.0.0.0/0" is used
  - "10.0.0.0/16"
  - "192.168.1.0/24"
  serviceName: "echo-test-service" # the name of the Service to target; must be in the same namespace
  mode: "service"                  # mandatory; "service" is currently the only supported mode
  ports:                           # list; at least 1 address is required;
  - port: 8080                     # maps from external port 8080 to 8080 TCP on Service side (same "servicePort:" and 'protocol: "TCP"' are defaults)
  - port: 8081                     # maps from external port 8080 to 8081 TCP on Service side ( is default)
    servicePort: 8080
  - port: 8081                     # maps from external UDP port 8081 to 8080 UDP on Service side
    servicePort: 8080
    protocol: "udp"
```

In the example above, please note the following:

* line 4 defines the Mapping name; it must be unique for a namespace,
* actual specification starts in line 5 and includes:
  * line 6: the list of external IPs (at least 1) that are going to be mapped to a Service; the same set of IP and Ports can't be used by any other Mapping in the whole cluster,
  * line 9: the list of CIDR prefixes that the traffic on IPs in line 6 will be accepted from; all the other connections will be dropped by the SmartNat instance and never let into your cluster,
  * line 12: the name of a Service (in the same namespace) that this Mapping is targeting,
  * line 13: internal configuration mode; "service" is mandatory and the only mode supported now, but new modes might be added in the future; in this mode traffic from SmartNat instance is forwarded to Service IP and the Service is responsible for forwarding the traffic to one of the pods,
  * line 14: the list of port mappings; at least 1 is required; "protocol" can be skipped and defaults to "TCP"; "servicePort" can be skipped as well and defaults to the value of "port".

## Applying Mapping

A configuration file like the one above can be applied to a k8s cluster using the usual command:

```bash
kubectl [apply|create] -f mapping.yml
```

## Mapping status

After the configuration is applied, you can get the created Mapping to get additional information about the status of requested configuration. It is shown in the Status field of the Mapping object:

```yaml
$ kubectl -n default get mapping mapping-sample -o yaml
apiVersion: smartnat.aureacentral.com/v1alpha1
kind: Mapping
  (...skipped...)
  name: mapping-sample
  namespace: default
spec:
  addresses:
  - 172.17.0.1
  allowedSources:
  - 0.0.0.0/0
  mode: service
  ports:
  - port: 8080
    protocol: tcp
    servicePort: 8080
  - port: 8081
    protocol: tcp
    servicePort: 8080
  serviceName: echo-test-service
status:
  configuredAddresses:
    172.17.0.1:
      podIPs:
      - 10.237.73.24
  invalid: "no"
  serviceVIP: 172.20.111.123
```

The Status info in the example above starts in line 21. The field "invalid" is set to "no" if your Mapping passed validation and can be configured correctly. If it contains anything different, it's a validation error message that explains what went wrong. In that case, no configuration for the Mapping is actually performed.

The field "serviceVIP" shows what is the current IP addresses assigned by the kubernetes cluster to a Service that the Mapping is targeting.

The last status field is "configuredAddresses". For each requested "spec.addresses" it includes information about configuration status of the IP. If the IP is missing, it means it was not yet configured. If the IP is present, the embedded "podIPs" list includes all IP addresses of currently running Pods, that are covered by the Mapping for this IP.