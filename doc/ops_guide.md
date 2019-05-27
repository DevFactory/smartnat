# SmartNat Operator Guide

## Introduction

Smartnat project provides the `smartnat-manager` binary, which is the kubernetes controller that handles Mappings. Still, to be useful the binary needs to be deployed on an instance (a physical server, a computing instance created in a cloud or virtual machine). To work there, the instance must provide expected networking configuration and run necessary services. How to do that depends heavily on what type of instance you're going to use and the Linux distribution you want to deploy.

### Software requirements

The general requirements are:

* any instance running a modern Linux distribution, that has installed the following tools and modules:
  * `iptables`, including modules:
    * `multiport`
    * `ipset`
    * `comment`
  * `ipset`
  * `ipvsadm`
  * `ip`
  * `conntrack`
  * GNU AWK (`gawk`) as the default `awk` binary
* enabled and running NTP time synchronization service, like `ntpd`
* enabled IPv4 forwarding, for example using `sysctl` directly by setting: `net.ipv4.ip_forward=1`

### Network interfaces requirements

First, please have a look at diagram in [README.md](../README.md#how-does-it-work). In general, the Smartnat instance has to have networking interface of 3 kinds:

1. The default gateway interface. This is the interface that your normal (from `main` route table) default gateway is on. With that interface, the public network (probably the Internet) has to be reachable. It won't be used by Smartnat to configure Mappings for clients nor in any other way. It's left entirely to the instance administrator and can be used for normal routing required to install security updates and so on.
1. The cluster interface. Through this interface, the Smartnat instance must be able to reach any Pod in your kubernetes cluster. This doesn't have to be a separate interface, ie. it's OK if your default gateway interface described above can also reach pods in your cluster. Then, you can safely skip this interface.
1. External client interfaces. These are the interfaces, which are dedicated to receiving client's traffic. They have to be dedicated to Smartnat only. It is administrator's responsibility to make these interface come up on system boot and to assign any IP addresses that are going to be used by Smartnat to them. Also, the router that can take traffic coming from these interfaces further into the network, has to have the first or the last IP address in the local subnet of the interface.

#### Example

Below is an example configuration of EC2 instance running in AWS. This instance uses `eth0` as both the default gateway interface and as the cluster interface. `eth1` and `eth2` are in the same subnet as `eth0` and provide 2 external IPs each.

```text
# ip a sh dev eth0
2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 9001 qdisc mq state UP group default qlen 1000
    link/ether 0e:cd:44:52:e5:20 brd ff:ff:ff:ff:ff:ff
    inet 10.22.66.97/23 brd 10.22.67.255 scope global dynamic eth0
       valid_lft 2239sec preferred_lft 2239sec
# ip a sh dev eth1
3: eth1: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 9001 qdisc mq state UP group default qlen 1000
    link/ether 0e:50:57:39:49:dc brd ff:ff:ff:ff:ff:ff
    inet 10.22.66.41/23 brd 10.22.67.255 scope global dynamic eth1
       valid_lft 2423sec preferred_lft 2423sec
    inet 10.22.66.40/23 brd 10.22.67.255 scope global secondary eth1
       valid_lft forever preferred_lft forever
# ip a sh dev eth2
4: eth2: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 9001 qdisc mq state UP group default qlen 1000
    link/ether 0e:db:01:71:5e:4a brd ff:ff:ff:ff:ff:ff
    inet 10.22.66.51/23 brd 10.22.67.255 scope global dynamic eth2
       valid_lft 3225sec preferred_lft 3225sec
    inet 10.22.66.50/23 brd 10.22.67.255 scope global secondary eth2
       valid_lft forever preferred_lft forever
# ip r
default via 10.22.66.1 dev eth0
default via 10.22.66.1 dev eth1 metric 10001
default via 10.22.66.1 dev eth2 metric 10002
10.22.66.0/23 dev eth0 proto kernel scope link src 10.22.66.97
10.22.66.0/23 dev eth2 proto kernel scope link src 10.22.66.51
10.22.66.0/23 dev eth1 proto kernel scope link src 10.22.66.41
169.254.169.254 dev eth0
```

### Deployment scenarios

In general, there are 2 main deployment scenarios possible:

1. Outside the cluster. You create a Smartnat instance that's not a cluster's node. To make it work, you have to start `kube-proxy` and `smartnat-manager` binaries there.
2. In the cluster. The Smartnat instance is created as a node in the cluster. In this case, `kube-proxy` will be probably already started for you. You have to run Smartnat from docker image and deploy as DaemonSet.

Right now, only the solution 1. above is described here, as we believe it's a better fit. As for solution 2., you probably shouldn't run any other pods than Smartnat on that node anyway. This means, that you will have a cluster node, that actually can't schedule your workloads - so there's not much sense in creating it that way. Also, `kubelet` and `docker` daemons will require some resources, so without them you can probably use smaller instance. And we reduce attack surface of an instance, that will be facing client's traffic.

## Creating your SmartNat instance

### 1. Preparing the networking configuration

Boot your instance and setup networking as [described above](#network-interfaces-requirements). This part depends heavily on your network architecture and used distribution. Just make sure everything works as described above, that traffic forwarding is enabled and there's no embedded firewall that can block traffic between external and cluster interfaces.

### 2. Installing and running necessary services

#### 2.1. Scripted installation

We provide [install-smart-nat.sh](packer/install-smart-nat.sh) script that can be used to install all the required binaries and service files into your systemd based distribution. You can also use this script with [packer](https://packer.io/) to easily build your virtual machine image. If you use AWS, you can check our [packer template](packer/packer.json) to build the Smartnat with packer on AWS on top the most recent Amazon Linux 2 distribution.

Alternatively, you can find detailed step-by-step instructions below.

#### 2.2. Step-by-step manual installation

##### 2.2.1. Kube-proxy

Your node has to run `kube-proxy` service that is version compatible with your cluster - you can download it here for different releases: [https://storage.googleapis.com/kubernetes-release/release/v1.X.Y/bin/linux/amd64/kube-proxy](https://storage.googleapis.com/kubernetes-release/release/v1.X.Y/bin/linux/amd64/kube-proxy). The important part here is that it needs to correctly connect, authenticate and authorize with your API server (if only you have RBAC enabled). For this, you have to create a valid kubeconfig file and place it under `/etc/kubeconfig` path. Make sure `kube-proxy` authenticates as `system:kube-proxy` user. Your `kube-proxy` must run in ["IPVS" mode](https://kubernetes.io/docs/concepts/services-networking/service/#proxy-mode-ipvs). Now, start `kube-proxy` with this command

```text
kube-proxy \
        --masquerade-all \
        --kubeconfig=/etc/kubeconfig \
        --proxy-mode=ipvs \
        --v=2
```

If your distro uses `systemd`, we provide ready config files. Get [kube-proxy.sh](../deployment/kube-proxy/kube-proxy.sh) and place it under `/usr/local/bin/kube-proxy.sh`. Next, get [kube-proxy.service](../deployment/kube-proxy/kube-proxy.service) and place it under `/etc/systemd/system` (all deployment files are also included in [binary releases](https://github.com/DevFactory/smartnat/releases)). Next, reload systemd and start the service:

```text
sudo systemctl daemon-reload
sudo systemctl enable kube-proxy
sudo systemctl restart kube-proxy
```

Check your `kube-proxy` status with `systemctl status kube-proxy` and logs with `journalctl -u kube-proxy`.

##### 2.2.2. Smartnat binary

When `kube-proxy` is up and running, it's time to deploy `smartnat-manager` binary in a very similar manner. Download the binary from the [release page](https://github.com/DevFactory/smartnat/releases) and place in `/usr/local/bin/smartnat-manager`. Get [smartnat.service](../deployment/smartnat/smartnat.service) systemd unit file and copy it into `/etc/systemd/system`. Next, configure the `Environment` entries in the file to configure `smartnat-manager`. Options are explained below.

#### 2.3. Smartnat configuration

The manager can be configured using the following environment variables:

* "SNCTRLR_DEBUG_ENABLED" - set to `true` to enable debug messages in log.
* "SNCTRLR_DEFAULT_GW_INTERFACE" - the name of default gateway interface, as explained [above](#network-interfaces-requirements). Example: `eth0`.
* "SNCTRLR_INTERFACES_PATTERN" - configures a regular expression for network interface names which are external (client facing). Only matching interfaces and their addresses will be serviced by smartnat. Example: `eth[1-9][0-9]*` means _everything that starts with `eth`, but not `eth0`_.
* "SNCTRLR_GW_ADDRESS_OFFSET" - configures the offset of router's IP address for all the external network interfaces matching SNCTRLR_INTERFACES_PATTERN. A positive offset is counted forward from the subnet's address of the 1st IP found on the interface (so, if the IP is "10.10.10.10/24" and offset is 1, the assumed router/gateway on this interface is "10.10.10.1"). A negative offset is counted backwards from the broadcast address (so, if the IP is "10.10.10.10/24" and the offset is -1, the assumed gateway IP on this interface is "10.10.10.254"). Please make sure that on all external interfaces the offset correctly points to the gateway routing the traffic outgoing from that interface.
* "SNCTRLR_MONITOR_PORT" - configures TCP port for exposing HTTP health check
* "SNCTRLR_MONITOR_IP" - configures IP address to bind to for exposing HTTP health check; if no value is set, `127.0.0.1` is used; to bind to all IP addresses, use empty string (``)

Advanced options:

* "SNCTRLR_SETUP_MASQUERADE" - a `true/false` variable that enables masquerading of traffic coming to a Smartnat instance and then leaving to cluster pods. When `true`, the traffic seems to be coming to the pods from the Smartnat instance itself. You generally want this to be set to `true`, unless your smartnat instance is acting as a router for traffic outgoing from the all the pods and nodes.
* "SNCTRLR_SETUP_SNAT" - a `true/false` bool variable that configures SNAT rules for a traffic originating on pods and reaching smartnat instance. You want this to be `true` only if the traffic that starts on pods should leave the smartnat instance with the same network interface and IP address as the one configured for the traffic incoming to smartnat for the Mapping. Making the traffic from pods to reach the smartnat is external problem and needs to be handled outside of smartnat service. Leave at `false` if you're not sure you need this.
* "SNCTRLR_IPTABLES_TIMEOUT_SEC" - timeout in seconds for the `iptables` system command that is frequently called by smartnat. Default should be OK, might need to be increased on heavily loaded smartnat instances.
* "SNCTRLR_AUTOREFRESH_PERIOD_SEC" - smartnat automatically reloads information about available network interface and assigned IP addresses. This setting controls how frequently (in seconds) should this information be loaded from the operating system. The default is 60 s.
* "SNCTRLR_MAX_FINALIZE_WAIT_MINUTES" - sets the absolute finalizer timeout in minutes. The finalizer timeout is a safety mechanism that needs to be triggered if a smartnat instance has configured a Mapping and then the instance had failed and never came back. In that case, when the Mapping is removed, kubernetes doesn't really delete the Mapping until all smartnat nodes that had been configured for it confirm that the configuration was removed. A failed instance will never do that, so we use this timeout to forcibly remove the Mapping after SNCTRLR_AUTOREFRESH_PERIOD_SEC, even if it is indicated as being still configured on some smartnat nodes.
* "SNCTRLR_MAX_PORTS_PER_MAPPING" - limits the number of Ports a single Mapping can configure. The number of exposed ports has influence on traffic forwarding rate and thus might be needed to tune by the administrator. The default is 50.

### 3. Cluster configuration

The last step is to apply the necessary configuration to the cluster itself. This includes definitions of the CRD and RBAC objects. Run the following commands:

```text
kubectl apply -f config/rbac/rbac_role.yaml
kubectl apply -f config/rbac/rbac_role_binding.yaml
kubectl apply -f config/crds/smartnat_v1alpha1_mapping.yaml
```

### 4. Done

Everything is place now. On the smartnat instance reload and restart the smartnat service with:

```text
sudo systemctl daemon-reload
sudo systemctl enable smartnat
sudo systemctl restart smartnat
```
