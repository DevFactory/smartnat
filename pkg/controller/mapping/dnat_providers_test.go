/* Copyright 2019 DevFactory FZ LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. */

package mapping_test

import (
	"fmt"
	"net"
	"testing"

	"github.com/DevFactory/go-tools/pkg/nettools"
	nt "github.com/DevFactory/go-tools/pkg/nettools"
	"github.com/DevFactory/smartnat/pkg/apis/smartnat/v1alpha1"
	"github.com/DevFactory/smartnat/pkg/controller/mapping"
	v1 "k8s.io/api/core/v1"
	v1meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stretchr/testify/assert"

	ntmocks "github.com/DevFactory/go-tools/pkg/nettools/mocks"
)

const (
	natTableName         = "nat"
	mangleTableName      = "mangle"
	preroutingChain      = "SNM-PREROUTING"
	snatPostroutingChain = "SNM-POSTROUTING-SNAT"
	masqPostroutingChain = "SNM-POSTROUTING-MASQ"
	markMasqComment      = "mark for masquerade in " + masqPostroutingChain
	iptablesMasqMark     = "0x00100000"
	protoTcp             = "tcp"
	protoUdp             = "udp"
)

type testCase struct {
	extIP              string
	externalIP         net.IP
	dport, servicePort int32
	allowed            string
	serviceIP          string
	meta               v1meta.ObjectMeta
	mapping            *v1alpha1.Mapping
	svc                *v1.Service
	customChain        string
}

func getTestCase() testCase {
	extIP := "10.10.10.10"
	meta := v1meta.ObjectMeta{
		Name:      "test",
		Namespace: "test",
	}
	allowed := "0.0.0.0/0"
	serviceIP := "172.16.1.1"
	return testCase{
		extIP:       extIP,
		externalIP:  net.ParseIP(extIP),
		dport:       80,
		servicePort: 8080,
		allowed:     allowed,
		serviceIP:   serviceIP,
		meta:        meta,
		mapping: &v1alpha1.Mapping{
			ObjectMeta: meta,
			Spec: v1alpha1.MappingSpec{
				Addresses:      []string{extIP, "10.20.30.1"},
				AllowedSources: []string{allowed},
				ServiceName:    "test",
			},
		},
		svc: &v1.Service{
			ObjectMeta: meta,
			Spec: v1.ServiceSpec{
				ClusterIP: serviceIP,
			},
		},
		customChain: "MAP-MDICIJE6TVWKE333NKHBEORO",
	}
}

func Test_throughServiceDNATProvider_SetupDNAT(t *testing.T) {
	c := getTestCase()

	iptablesMarkRule := nettools.IPTablesRuleArgs{
		Table:     natTableName,
		ChainName: c.customChain,
		Selector:  nil,
		Action:    []string{"MARK", "--or-mark", iptablesMasqMark},
		Comment:   markMasqComment,
	}
	iptablesBogusExistingRule := getIptablesDNATRule(c.serviceIP, protoTcp, c.customChain, 1111, c.servicePort, c.meta)
	tests := []struct {
		name  string
		ports []v1alpha1.MappingPort
		init  func(ipt *ntmocks.IPTablesHelper)
	}{
		{
			name: "tcp only, no existing rules",
			ports: []v1alpha1.MappingPort{
				v1alpha1.MappingPort{
					Port:        c.dport,
					Protocol:    protoTcp,
					ServicePort: c.servicePort,
				},
			},
			init: func(ipt *ntmocks.IPTablesHelper) {
				ipt.
					On("EnsureExistsOnlyAppend",
						getIptablesProtoJumpRule(c.extIP, protoTcp, c.allowed, c.customChain, c.dport, c.meta)).Return(nil).
					On("DeleteByComment", natTableName, preroutingChain, fmt.Sprintf("for mapping %s/%s [%s]",
						c.mapping.Namespace, c.mapping.Name, protoUdp)).Return(nil).
					On("EnsureExistsInsert", iptablesMarkRule).Return(nil).
					On("LoadRules", natTableName, c.customChain).Return([]*nt.IPTablesRuleArgs{}, nil).
					On("EnsureExistsAppend",
						getIptablesDNATRule(c.serviceIP, protoTcp, c.customChain, c.dport, c.servicePort, c.meta)).Return(nil)
			},
		},
		{
			name: "tcp and udp, some existing rules",
			ports: []v1alpha1.MappingPort{
				v1alpha1.MappingPort{
					Port:        c.dport,
					Protocol:    protoTcp,
					ServicePort: c.servicePort,
				},
				v1alpha1.MappingPort{
					Port:        c.dport,
					Protocol:    protoUdp,
					ServicePort: c.servicePort,
				},
			},
			init: func(ipt *ntmocks.IPTablesHelper) {
				ipt.
					On("EnsureExistsOnlyAppend",
						getIptablesProtoJumpRule(c.extIP, protoTcp, c.allowed, c.customChain, c.dport, c.meta)).Return(nil).
					On("EnsureExistsOnlyAppend",
						getIptablesProtoJumpRule(c.extIP, protoUdp, c.allowed, c.customChain, c.dport, c.meta)).Return(nil).
					On("EnsureExistsInsert", iptablesMarkRule).Return(nil).
					On("LoadRules", natTableName, c.customChain).Return(
					[]*nt.IPTablesRuleArgs{&iptablesBogusExistingRule}, nil).
					On("Delete", iptablesBogusExistingRule).Return(nil).
					On("EnsureExistsAppend",
						getIptablesDNATRule(c.serviceIP, protoTcp, c.customChain, c.dport, c.servicePort, c.meta)).Return(nil).
					On("EnsureExistsAppend",
						getIptablesDNATRule(c.serviceIP, protoUdp, c.customChain, c.dport, c.servicePort, c.meta)).Return(nil)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c.mapping.Spec.Ports = tt.ports
			iptables := &ntmocks.IPTablesHelper{}
			iptables.On("EnsureChainExists", "nat", c.customChain).Return(nil)
			tt.init(iptables)
			dnat := mapping.NewThroughServiceDNATProvider(iptables, mapping.NewNamer())
			// call SetupDNAT
			err := dnat.SetupDNAT(c.externalIP, c.mapping, c.svc, nil, true)
			assert.Nil(t, err)
			iptables.AssertExpectations(t)
		})
	}
}

func Test_throughServiceDNATProvider_DeleteDNAT(t *testing.T) {
	c := getTestCase()

	tests := []struct {
		name  string
		ports []v1alpha1.MappingPort
	}{
		{
			name: "tcp only",
			ports: []v1alpha1.MappingPort{
				v1alpha1.MappingPort{
					Port:        c.dport,
					Protocol:    protoTcp,
					ServicePort: c.servicePort,
				},
			},
		},
		{
			name: "tcp and udp",
			ports: []v1alpha1.MappingPort{
				v1alpha1.MappingPort{
					Port:        c.dport,
					Protocol:    protoTcp,
					ServicePort: c.servicePort,
				},
				v1alpha1.MappingPort{
					Port:        c.dport,
					Protocol:    protoUdp,
					ServicePort: c.servicePort,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c.mapping.Spec.Ports = tt.ports
			iptables := &ntmocks.IPTablesHelper{}
			iptables.
				On("FlushChain", "nat", c.customChain).Return(nil).
				On("DeleteByComment", natTableName, preroutingChain, fmt.Sprintf("for mapping %s/%s [%s]",
					c.mapping.Namespace, c.mapping.Name, protoTcp)).Return(nil).
				On("DeleteByComment", natTableName, preroutingChain, fmt.Sprintf("for mapping %s/%s [%s]",
					c.mapping.Namespace, c.mapping.Name, protoUdp)).Return(nil).
				On("DeleteChain", natTableName, c.customChain).Return(nil)
			dnat := mapping.NewThroughServiceDNATProvider(iptables, mapping.NewNamer())
			// call DeleteDNAT
			err := dnat.DeleteDNAT(c.externalIP, c.mapping)
			assert.Nil(t, err)
			iptables.AssertExpectations(t)
		})
	}
}

func getIptablesProtoJumpRule(extIP, proto, allowed, customChain string, dport int32,
	meta v1meta.ObjectMeta) nettools.IPTablesRuleArgs {
	return nettools.IPTablesRuleArgs{
		Table:     natTableName,
		ChainName: preroutingChain,
		Selector: []string{"-d", fmt.Sprintf("%s/32", extIP), "-p", proto,
			"-m", "multiport", "--dports", fmt.Sprintf("%d", dport), "-s", allowed},
		Action:  []string{customChain},
		Comment: fmt.Sprintf("for mapping %s/%s [%s]", meta.Namespace, meta.Name, proto),
	}
}

func getIptablesDNATRule(serviceIP, proto, customChain string, dport, servicePort int32,
	meta v1meta.ObjectMeta) nettools.IPTablesRuleArgs {
	return nettools.IPTablesRuleArgs{
		Table:     natTableName,
		ChainName: customChain,
		Selector:  []string{"-p", proto, "--dport", fmt.Sprintf("%d", dport)},
		Action:    []string{"DNAT", "--to-destination", fmt.Sprintf("%s:%d", serviceIP, servicePort)},
		Comment: fmt.Sprintf("for mapping %s/%s [%s:%d:%d]", meta.Namespace, meta.Name,
			proto, dport, servicePort),
	}
}
