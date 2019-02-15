#!/bin/bash -e

iptables -t nat -C POSTROUTING -j KUBE-POSTROUTING 2>/dev/null || (iptables -t nat -N KUBE-POSTROUTING && iptables -t nat -I POSTROUTING -j KUBE-POSTROUTING)
exec kube-proxy \
        --masquerade-all \
        --kubeconfig=/etc/kubeconfig \
        --proxy-mode=ipvs \
        --v=2
