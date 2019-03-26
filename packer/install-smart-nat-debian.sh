#!/bin/bash -ex

TEMPLATE_DIR=${TEMPLATE_DIR:-/tmp/smart-nat}

sudo apt-get update
sudo apt-get install -y ipset ipvsadm conntrack curl ntp 

sudo bash -c 'echo "net.ipv4.ip_forward=1" > /etc/sysctl.d/98-central.conf'

cd /tmp
curl -L -O https://github.com/DevFactory/smartnat/releases/download/v${SN_VER}/smartnat_${SN_VER}_linux_amd64.tar.gz
tar xzvf smartnat_${SN_VER}_linux_amd64.tar.gz
sudo mv smartnat-manager /usr/local/bin/smartnat-manager

curl -L -O https://storage.googleapis.com/kubernetes-release/release/v${KUBE_VER}/bin/linux/amd64/kube-proxy
sudo mv kube-proxy /usr/local/bin/kube-proxy
sudo mv deployment/kube-proxy/kube-proxy.sh /usr/local/bin/kube-proxy.sh

curl -L -O https://storage.googleapis.com/kubernetes-release/release/v${KUBE_VER}/bin/linux/amd64/kubectl
sudo mv kubectl /usr/local/bin/kubectl

sudo chmod a+x /usr/local/bin/kubectl /usr/local/bin/smartnat-manager /usr/local/bin/kube-proxy /usr/local/bin/kube-proxy.sh

sudo cp deployment/kube-proxy/kube-proxy.service /etc/systemd/system/
sudo cp deployment/smartnat/smartnat.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable kube-proxy smartnat

rm *.tar.gz
echo 'done!'
