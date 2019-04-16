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

package mapping

import (
	"fmt"
	"net"
	"strings"

	"github.com/DevFactory/go-tools/pkg/extensions/collections"
	"github.com/DevFactory/go-tools/pkg/nettools"
	"github.com/DevFactory/smartnat/pkg/apis/smartnat/v1alpha1"
	v1 "k8s.io/api/core/v1"
)

const (
	chainNamePrefix = "MAP"
	ipSetNamePrefix = "DNAT"
	markMasqComment = "mark for masquerade in " + masqPostroutingChain
)

// DNATProvider provides and interface to configure necessary DNAT rules from an External IP. The target
// of the translation a exact meanings to perform it are implementation specific.
//
// SetupDNAT configures and synchronizes DNAT rules for the specified externalIP and mapping, service,
// endpoints trio
//
// DeleteDNAT deletes all the configuration introduced by SetupDNAT for the specified externalIP and
// mapping.
type DNATProvider interface {
	SetupDNAT(externalIP net.IP, mapping *v1alpha1.Mapping, svc *v1.Service, eps *v1.Endpoints,
		setupMasquerade bool) error
	DeleteDNAT(externalIP net.IP, mapping *v1alpha1.Mapping) error
}

// ThroughServiceDNAT implements DNATProvider by doing translation from External IP to ClusterIP of
// the service. The translation is done using iptables and will only work if kube-proxy
// is not running in 'iptables' mode. 'IPVS' kube-proxy is highly recommended and the only
// supported configuration.
type ThroughServiceDNAT struct {
	iptables nettools.IPTablesHelper
	ipset    nettools.IPSetHelper
	namer    Namer
}

// NewThroughServiceDNATProvider returns new instance of the ThroughServiceDNAT
func NewThroughServiceDNATProvider(iptables nettools.IPTablesHelper, ipset nettools.IPSetHelper,
	namer Namer) DNATProvider {
	return &ThroughServiceDNAT{
		iptables: iptables,
		ipset:    ipset,
		namer:    namer,
	}
}

// SetupDNAT implements DNATProvider.SetupDNAT by doing translation with iptables from
// External IP to ClusterIP of the Service.
func (p *ThroughServiceDNAT) SetupDNAT(externalIP net.IP, mapping *v1alpha1.Mapping, svc *v1.Service,
	_ *v1.Endpoints, setupMasquerade bool) error {
	// create per service chain X
	// insert in PREROUTING jump to X if traffic goes to externalIP and one of the external ports
	// in chain X, for each port P mapped to port R on service with IP S:
	//    -p PROTO --dport P -m comment --comment CMT -j DNAT --to-destination S:R
	chainName := p.getChainName(mapping)
	logrusWithMapping(mapping).Debugf("Starting to setup through-service DNAT with "+
		"chain name %s", chainName)

	logrusWithMapping(mapping).Debugf("Checking/creating chain %s", chainName)
	if err := p.iptables.EnsureChainExists(natTableName, chainName); err != nil {
		logrusWithMapping(mapping).Errorf("Error when creating chain %s: %v", chainName, err)
		return err
	}

	// create ipset that contains all "allowed_source_ip:proto,port" pairs
	// and add iptables rules that checks against that set and jumps to
	// per-service chain with per-port translation rules
	p.synchronizeJumpAndIPSet(mapping, externalIP, preroutingChain, chainName)

	// setup Masquerade marks if enabled
	if setupMasquerade {
		logrusWithMapping(mapping).Debugf("Adding mask rule for masquerade in chain %s", chainName)
		rule := nettools.IPTablesRuleArgs{
			Table:     natTableName,
			ChainName: chainName,
			Selector:  nil,
			Action:    []string{"MARK", "--or-mark", iptablesMasqMark},
			Comment:   markMasqComment,
		}
		if err := p.iptables.EnsureExistsInsert(rule); err != nil {
			logrusWithMapping(mapping).Errorf("Error inserting iptables rule in chain %s "+
				" with comment %s: %v", chainName, rule.Comment, err)
			return err
		}
	}

	return p.synchronizePerPortRules(mapping, svc, chainName)
}

// DeleteDNAT implements DNATProvider.DeleteDNAT by removing translation in iptables from
// External IP to ClusterIP of the Service.
func (p *ThroughServiceDNAT) DeleteDNAT(externalIP net.IP, mapping *v1alpha1.Mapping) error {
	// get per service chain name X
	// - flush it
	// - delete jump to it from PREROUTING
	// - delete the chain X
	chainName := p.getChainName(mapping)
	logrusWithMapping(mapping).Debugf("Starting to delete per-service DNAT with chain name %s", chainName)

	logrusWithMapping(mapping).Debugf("Flushing chain %s", chainName)
	if err := p.iptables.FlushChain(natTableName, chainName); err != nil {
		logrusWithMapping(mapping).Errorf("Error when flushing chain %s: %v", chainName, err)
		return err
	}

	// remove ipset
	setName := p.getIPSetName(mapping)
	if err := p.ipset.DeleteSet(setName); err != nil {
		logrusWithMapping(mapping).Errorf("Error removing ipset %s: %v", setName, err)
		return err
	}

	// remove jump rule
	comment := p.getJumpComment(mapping)
	logrusWithMapping(mapping).Debugf("Deleting jump in chain %s with comment: %s",
		chainName, comment)
	if err := p.iptables.DeleteByComment(natTableName, preroutingChain, comment); err != nil {
		logrusWithMapping(mapping).Errorf("Error deleting iptables jump from chain %s to %s: %v",
			preroutingChain, chainName, err)
		return err
	}

	// delete custom chain
	logrusWithMapping(mapping).Debugf("Deleting chain %s", chainName)
	if err := p.iptables.DeleteChain(natTableName, chainName); err != nil {
		logrusWithMapping(mapping).Errorf("Error when deleting chain %s: %v", chainName, err)
		return err
	}
	return nil
}

func (p *ThroughServiceDNAT) synchronizeJumpAndIPSet(mapping *v1alpha1.Mapping, externalIP net.IP, fromChain,
	toChain string) error {
	// ensure ipset for this services exists
	setName := p.getIPSetName(mapping)
	logrusWithMapping(mapping).Debugf("Ensuring ipset %s exists", setName)
	if err := p.ipset.EnsureSetExists(setName, "hash:net,port"); err != nil {
		logrusWithMapping(mapping).Errorf("Error creating ipset: %v", err)
		return err
	}

	// synchronize ipset content
	logrusWithMapping(mapping).Debugf("Synchronizing entries in ipset %s", setName)
	tcpPorts, udpPorts := p.getPerProtocolPorts(mapping)
	netPorts := []nettools.NetPort{}
	for _, allowedSrc := range mapping.Spec.AllowedSources {
		_, srcNet, err := net.ParseCIDR(allowedSrc)
		if err != nil {
			logrusWithMapping(mapping).Errorf("Error parsing allowed source %s: %v", allowedSrc, err)
			return err
		}
		endpoints := []struct {
			ports []v1alpha1.MappingPort
			proto nettools.Protocol
		}{
			{tcpPorts, nettools.TCP},
			{udpPorts, nettools.UDP},
		}
		for _, ep := range endpoints {
			for _, port := range ep.ports {
				netPort := nettools.NetPort{
					Net:      *srcNet,
					Port:     uint16(port.Port),
					Protocol: ep.proto,
				}
				netPorts = append(netPorts, netPort)
			}
		}
	}
	if err := p.ipset.EnsureSetHasOnlyNetPort(setName, netPorts); err != nil {
		logrusWithMapping(mapping).Errorf("Error synchronizing entries in ipset %s: %v", setName, err)
		return err
	}

	// ensure jump to toChain exists with match against the ipset
	logrusWithMapping(mapping).Debugf("Adding jump from chain %s to chain %s using ipset %s",
		fromChain, toChain, setName)
	return p.iptables.EnsureExistsOnlyAppend(nettools.IPTablesRuleArgs{
		Table:     natTableName,
		ChainName: fromChain,
		Selector:  []string{"-d", fmt.Sprintf("%s/32", externalIP.String()), "-m", "set", "--match-set", setName, "src,dst"},
		Action:    []string{toChain},
		Comment:   p.getJumpComment(mapping),
	})
}

// synchronizePerPortRules needs to get current rules in the specified chain and the new set
// of MappedPorts to configure. Than, it has to leave without changes what is unchanged and
// remove and add objects as needed
func (p *ThroughServiceDNAT) synchronizePerPortRules(mapping *v1alpha1.Mapping, svc *v1.Service,
	chainName string) error {
	logrusWithMapping(mapping).Debugf("Starting to synchronize per port rules in chain %s", chainName)
	// load current rules from the chain
	current, err := p.iptables.LoadRules(natTableName, chainName)
	if err != nil {
		logrusWithMapping(mapping).Errorf("Error getting iptables rules from the operating system for"+
			" chain %s. Error: %v", chainName, err)
		return err
	}
	// exclude the mark for masquerade rule, so it is always kept
	current = p.excludeMarkMasqRule(current)
	new := p.rulesFromMappedPorts(mapping, svc.Spec.ClusterIP, chainName)

	// now find set difference "current\new" and "new\current"
	currentAsInterface := make([]interface{}, len(current))
	for i, rule := range current {
		currentAsInterface[i] = rule
	}
	newAsInterface := make([]interface{}, len(new))
	for i, rule := range new {
		newAsInterface[i] = rule
	}
	toAdd, toRemove := collections.GetSlicesDifferences(newAsInterface, currentAsInterface,
		func(r1, r2 interface{}) bool {
			return (r1.(*nettools.IPTablesRuleArgs)).Comment == (r2.(*nettools.IPTablesRuleArgs)).Comment
		})

	// delete unnecessary rules
	for _, ruleAsInterface := range toRemove {
		rule := ruleAsInterface.(*nettools.IPTablesRuleArgs)
		logrusWithMapping(mapping).Debugf("Deleting rule in chain %s. Rule comment: %s", chainName,
			rule.Comment)
		if err := p.iptables.Delete(*rule); err != nil {
			logrusWithMapping(mapping).Errorf("Error deleting iptables rule in chain %s "+
				" with comment %s: %v", chainName, rule.Comment, err)
			return err
		}
	}

	// add required new rules
	for _, ruleAsInterface := range toAdd {
		rule := ruleAsInterface.(*nettools.IPTablesRuleArgs)
		logrusWithMapping(mapping).Debugf("Adding rule in chain %s. Rule comment: %s", chainName,
			rule.Comment)
		if err := p.iptables.EnsureExistsAppend(*rule); err != nil {
			logrusWithMapping(mapping).Errorf("Error inserting iptables rule in chain %s "+
				" with comment %s: %v", chainName, rule.Comment, err)
			return err
		}
	}

	return nil
}

func (*ThroughServiceDNAT) excludeMarkMasqRule(rules []*nettools.IPTablesRuleArgs) []*nettools.IPTablesRuleArgs {
	res := make([]*nettools.IPTablesRuleArgs, 0, len(rules))
	for _, rule := range rules {
		if rule.Comment == markMasqComment {
			continue
		}
		res = append(res, rule)
	}
	return res
}

func (p *ThroughServiceDNAT) rulesFromMappedPorts(mapping *v1alpha1.Mapping, svcClusterIP,
	chainName string) []*nettools.IPTablesRuleArgs {
	new := make([]*nettools.IPTablesRuleArgs, 0, len(mapping.Spec.Ports))
	for _, port := range mapping.Spec.Ports {
		endpoint := fmt.Sprintf("%s:%d", svcClusterIP, port.ServicePort)
		new = append(new, &nettools.IPTablesRuleArgs{
			Table:     natTableName,
			ChainName: chainName,
			Selector:  []string{"-p", port.Protocol, "--dport", fmt.Sprintf("%d", port.Port)},
			Action:    []string{"DNAT", "--to-destination", endpoint},
			Comment: fmt.Sprintf("for mapping %s/%s [%s:%d:%d]", mapping.Namespace, mapping.Name,
				port.Protocol, port.Port, port.ServicePort),
		})
	}
	return new
}

func (p *ThroughServiceDNAT) getJumpComment(mapping *v1alpha1.Mapping) string {
	return fmt.Sprintf("for mapping %s/%s", mapping.Namespace, mapping.Name)
}

func (p *ThroughServiceDNAT) getPerProtocolPorts(mapping *v1alpha1.Mapping) (
	tcp, udp []v1alpha1.MappingPort) {
	tcpPorts := make([]v1alpha1.MappingPort, 0, len(mapping.Spec.Ports))
	udpPorts := make([]v1alpha1.MappingPort, 0, len(mapping.Spec.Ports))
	for _, port := range mapping.Spec.Ports {
		if strings.ToLower(port.Protocol) == "tcp" {
			tcpPorts = append(tcpPorts, port)
		}
		if strings.ToLower(port.Protocol) == "udp" {
			udpPorts = append(udpPorts, port)
		}
	}
	return tcpPorts, udpPorts
}

func (p *ThroughServiceDNAT) getChainName(mapping *v1alpha1.Mapping) string {
	return fmt.Sprintf("%s-%s", chainNamePrefix, p.namer.Name(mapping.ObjectMeta))
}

func (p *ThroughServiceDNAT) getIPSetName(mapping *v1alpha1.Mapping) string {
	return fmt.Sprintf("%s-%s", ipSetNamePrefix, p.namer.Name(mapping.ObjectMeta))
}
