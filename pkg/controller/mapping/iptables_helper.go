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
	"os/exec"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/DevFactory/go-tools/pkg/nettools"
	"github.com/DevFactory/smartnat/pkg/apis/smartnat/v1alpha1"
	"k8s.io/api/core/v1"
)

const (
	natTableName             = "nat"
	mangleTableName          = "mangle"
	preroutingChain          = "SNM-PREROUTING"
	snatPostroutingChain     = "SNM-POSTROUTING-SNAT"
	masqPostroutingChain     = "SNM-POSTROUTING-MASQ"
	iptablesRetries          = 3
	customChainServicePrefix = "SVC"
	iptablesMasqMark         = "0x00100000"
	iptKeywordMasquerade     = "MASQUERADE"
)

/*
IPTablesHelper provides high level operations tuned for smart-nat-controller on system's -

SetupDNAT sets up a DNAT rule for a traffic coming to externalIP to Service/Endpoints passed as the arguments
for ports and sources listed in Maping.Spec.

SetupSNAT sets up a SNAT rule for a traffic coming from any pod listed in Endpoints and going out through the
interface with externalIP.

SetupMark sets up an iptables mark rule for the set associated with this mapping and external IP

DeleteDNAT deletes all DNAT entries created by SetupDNAT for externalIP and mapping passed as arguments.

DeleteSNAT deletes all SNAT entries created by SetupSNAT for externalIP and mapping passed as arguments.

DeleteMark deletes Mark entry created by SetupMark for externalIP and mapping passed as arguments.
*/
type IPTablesHelper interface {
	SetupDNAT(externalIP net.IP, mapping *v1alpha1.Mapping, svc *v1.Service, eps *v1.Endpoints,
		setupMasquerade bool) error
	SetupSNAT(externalIP net.IP, mapping *v1alpha1.Mapping, svc *v1.Service, eps *v1.Endpoints) error
	SetupMark(externalIP net.IP, mapping *v1alpha1.Mapping) error
	DeleteDNAT(externalIP net.IP, mapping *v1alpha1.Mapping) error
	DeleteSNAT(externalIP net.IP, mapping *v1alpha1.Mapping) error
	DeleteMark(externalIP net.IP, mapping *v1alpha1.Mapping) error
}

// IPTablesHelp implements IPTablesHelper interface using Linux iptables
type IPTablesHelp struct {
	dnat      DNATProvider
	iptables  nettools.IPTablesHelper
	ifaces    nettools.InterfaceProvider
	namer     Namer
	setupMasq bool
	setupSNAT bool
}

// NewIPTablesHelper returns new NewIPTablesHelper implemented by NewIPTablesHelp
func NewIPTablesHelper(dnatProvider DNATProvider, iptables nettools.IPTablesHelper, namer Namer,
	ifaceProvider nettools.InterfaceProvider, setupMasquerade, setupSNAT bool) (
	IPTablesHelper, error) {
	helper := &IPTablesHelp{
		dnat:      dnatProvider,
		iptables:  iptables,
		namer:     namer,
		ifaces:    ifaceProvider,
		setupMasq: setupMasquerade,
		setupSNAT: setupSNAT,
	}
	err := helper.initializeChains()
	return helper, err
}

// SetupDNAT implements IPTablesHelper.SetupDNAT using linux iptables.
func (h *IPTablesHelp) SetupDNAT(externalIP net.IP, mapping *v1alpha1.Mapping, svc *v1.Service,
	eps *v1.Endpoints, setupMasquerade bool) error {
	return h.dnat.SetupDNAT(externalIP, mapping, svc, eps, setupMasquerade)
}

// DeleteDNAT implements IPTablesHelper.DeleteDNAT using linux iptables.
func (h *IPTablesHelp) DeleteDNAT(externalIP net.IP, mapping *v1alpha1.Mapping) error {
	return okIfDoesNotExistInIptables(h.dnat.DeleteDNAT(externalIP, mapping))
}

// SetupSNAT implements IPTablesHelper.SetupSNAT using linux iptables.
func (h *IPTablesHelp) SetupSNAT(externalIP net.IP, mapping *v1alpha1.Mapping, svc *v1.Service,
	eps *v1.Endpoints) error {
	return h.snatAction(externalIP, mapping, h.iptables.EnsureExistsOnlyAppend, "Checking/creating")
}

// DeleteSNAT implements IPTablesHelper.DeleteSNAT using linux iptables.
func (h *IPTablesHelp) DeleteSNAT(externalIP net.IP, mapping *v1alpha1.Mapping) error {
	return okIfDoesNotExistInIptables(h.snatAction(externalIP, mapping, h.iptables.Delete, "Deleting"))
}

// SetupMark implements IPTablesHelper.SetupMark using linux iptables.
func (h *IPTablesHelp) SetupMark(externalIP net.IP, mapping *v1alpha1.Mapping) error {
	return h.markAction(externalIP, mapping, h.iptables.EnsureExistsOnlyAppend, "Checking/creating")
}

// DeleteMark implements IPTablesHelper.DeleteMark using linux iptables.
func (h *IPTablesHelp) DeleteMark(externalIP net.IP, mapping *v1alpha1.Mapping) error {
	return okIfDoesNotExistInIptables(h.markAction(externalIP, mapping, h.iptables.Delete, "Deleting"))
}

func (h *IPTablesHelp) markAction(externalIP net.IP, mapping *v1alpha1.Mapping,
	actionFunc func(nettools.IPTablesRuleArgs) error, actionName string) error {
	// 1) get fwmark for interface that the traffic should egress on - iface of local ExternalIP
	// 2) make sure that in mangle SNM-PREROUTING there's only 1 entry for a given ipset that
	//    the necessary fwmark
	iface, err := h.ifaces.GetInterfaceForLocalIP(externalIP)
	if err != nil {
		logrusWithMapping(mapping).Warnf("Can't get network interface for IP %s: %v", externalIP.String(), err)
		return err
	}
	mark := getFwMarkForInterface(iface)
	ipsetName := h.namer.Name(mapping.ObjectMeta)
	logrusWithMapping(mapping).Debugf("%s Mark rule for ipset %s and External IP %s", actionName, ipsetName,
		externalIP.String())
	selector, action := h.getMarkSelectorAndAction(ipsetName, mark)
	return actionFunc(nettools.IPTablesRuleArgs{
		Table:     mangleTableName,
		ChainName: preroutingChain,
		Selector:  selector,
		Action:    action,
		Comment:   h.getMarkRuleComment(mapping, ipsetName, mark),
	})
}

func (h *IPTablesHelp) getMarkSelectorAndAction(setName string, mark int) ([]string, []string) {
	return []string{"-m", "set", "--match-set", setName, "src"},
		[]string{"MARK", "--set-xmark", fmt.Sprintf("0x%x/0x%x", mark, mark)}
}

func (h *IPTablesHelp) getMarkRuleComment(mapping *v1alpha1.Mapping, setName string, mark int) string {
	return fmt.Sprintf("for Maping %s/%s [%s] mark 0x%x", mapping.Namespace, mapping.Name, setName, mark)
}

func (h *IPTablesHelp) snatAction(externalIP net.IP, mapping *v1alpha1.Mapping,
	actionFunc func(nettools.IPTablesRuleArgs) error, actionName string) error {
	setName := h.namer.Name(mapping.ObjectMeta)
	logrusWithMapping(mapping).Debugf("%s SNAT rule for mapping %s/%s [%s] and External "+
		"IP %s", actionName, mapping.Namespace, mapping.Name, setName, externalIP.String())
	selector, action := h.getSNATSelectorAndAction(setName, externalIP.String())
	return actionFunc(nettools.IPTablesRuleArgs{
		Table:     natTableName,
		ChainName: snatPostroutingChain,
		Selector:  selector,
		Action:    action,
		Comment:   h.getSNATRuleComment(mapping, setName),
	})
}

func (h *IPTablesHelp) getSNATSelectorAndAction(setName, externalIP string) ([]string, []string) {
	return []string{"-m", "set", "--set", setName, "src"},
		[]string{"SNAT", "--to-source", externalIP}
}

func (h *IPTablesHelp) getSNATRuleComment(mapping *v1alpha1.Mapping, setName string) string {
	return fmt.Sprintf("for Maping %s/%s [%s]", mapping.Namespace, mapping.Name, setName)
}

func (h *IPTablesHelp) initializeChains() error {
	if err := h.setupIptablesForChains(mangleTableName, preroutingChain, "PREROUTING"); err != nil {
		return err
	}
	if err := h.setupIptablesForChains(natTableName, preroutingChain, "PREROUTING"); err != nil {
		return err
	}
	if h.setupSNAT {
		if err := h.setupIptablesForChains(natTableName, snatPostroutingChain, "POSTROUTING"); err != nil {
			return err
		}
	}
	if h.setupMasq {
		if err := h.setupIptablesForChains(natTableName, masqPostroutingChain, "POSTROUTING"); err != nil {
			return err
		}
		return h.initializeDefaultMasquerade()
	}
	return nil
}

func (h *IPTablesHelp) setupIptablesForChains(table, customChainName, baseChainName string) error {
	if err := h.iptables.EnsureChainExists(table, customChainName); err != nil {
		logrus.Errorf("Error creating required custom iptables chain %s in table %s. Error: %v", customChainName, table, err)
		return err
	}

	if err := h.iptables.EnsureJumpToChainExists(table, customChainName, baseChainName); err != nil {
		logrus.Errorf("Error creating required jump in table %s from iptables chain %s to chain %s. Error: %v",
			table, baseChainName, customChainName, err)
		return err
	}

	logrus.Infof("Iptables chain setup successful in table %s for custom chain %s and basic chain %s", table,
		customChainName, baseChainName)
	return nil
}

func (h *IPTablesHelp) initializeDefaultMasquerade() error {
	// if sn-ctrlr is to be used on the default gateway instance, it needs the following rule to be present
	// for all the pods, that don't use External IPs, but still want to reach external networks
	// iptables -t nat -I SN-POSTROUTING-MASQ -s 10.233.0.0/16 -o eth0 -j MASQUERADE
	// where: 10.233.0.0/16 is the cluster pod subnet and eth0 is the interface towards default GW
	// this is currently not implemented. Needs to be optional feature.

	// example: iptables -t nat -I SN-POSTROUTING-MASQ -m mark --mark 0x00100000/0x00100000 -j MASQUERADE
	logrus.Debug("Setting up MASQUERADE rule for traffic marked in PREROUTING chain as going to services")
	return h.iptables.EnsureExistsInsert(nettools.IPTablesRuleArgs{
		Table:     natTableName,
		ChainName: masqPostroutingChain,
		Selector:  []string{"-m", "mark", "--mark", fmt.Sprintf("%s/%s", iptablesMasqMark, iptablesMasqMark)},
		Action:    []string{iptKeywordMasquerade},
		Comment:   "masquerade traffic marked in PREROUTING rules as destined for services",
	})
}

func okIfDoesNotExistInIptables(err error) error {
	// It's ok if the rule already doesn't exist.
	if exitErr, ok := err.(*exec.ExitError); ok {
		errStr := string(exitErr.Stderr)
		if errStr == "iptables: No chain/target/match by that name.\n" ||
			strings.Contains(errStr, " doesn't exist.\n") {
			logrus.Debug("Nothing to do, as the requested iptables rule already doesn't exist")
			return nil
		}
	}
	return err
}
