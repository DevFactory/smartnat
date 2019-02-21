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

	enet "github.com/DevFactory/go-tools/pkg/extensions/net"
	estrings "github.com/DevFactory/go-tools/pkg/extensions/strings"
	"github.com/DevFactory/go-tools/pkg/nettools"
	"github.com/DevFactory/smartnat/pkg/apis/smartnat/v1alpha1"
	"k8s.io/api/core/v1"
)

// Syncer runs everything required to reconcile a single Mapping
type Syncer interface {

	// DeleteMapping deletes all the configuration related to the
	// Mapping passed as an argument
	DeleteMapping(sn *v1alpha1.Mapping, externalIP net.IP) (dirty bool, err error)

	// SyncMapping reconciles data input and output paths, setting up everything
	// that is needed for the traffic arriving on External IP to reach
	// the given Service and then get back to a client
	SyncMapping(sn *v1alpha1.Mapping, svc *v1.Service, eps *v1.Endpoints) (dirty bool, err error)
}

// LinuxSyncer handles state synchronization between API objects and operating system configuration
type LinuxSyncer struct {
	interfaceProvider nettools.InterfaceProvider
	ipRouteHelper     IPRouteSmartNatHelper
	conntrack         nettools.ConntrackHelper
	iptables          IPTablesHelper
	namer             Namer
	ipset             nettools.IPSetHelper
	setupSNAT         bool
	setupMasq         bool
}

// NewSyncer creates a new Linux based Syncer
func NewSyncer(namer Namer, interfaceProvider nettools.InterfaceProvider,
	ipRouteHelper IPRouteSmartNatHelper, conntrackHelper nettools.ConntrackHelper,
	iptHelper IPTablesHelper, ipsetHelper nettools.IPSetHelper, setupSNAT,
	setupMasq bool) Syncer {
	syncer := &LinuxSyncer{
		interfaceProvider: interfaceProvider,
		ipRouteHelper:     ipRouteHelper,
		conntrack:         conntrackHelper,
		iptables:          iptHelper,
		namer:             namer,
		ipset:             ipsetHelper,
		setupSNAT:         setupSNAT,
		setupMasq:         setupMasq,
	}
	return syncer
}

// DeleteMapping deletes all operating system configuration related to given mapping.
// When configuration is delted, it updates Status field of the mapping.
func (s *LinuxSyncer) DeleteMapping(mapping *v1alpha1.Mapping, externalIP net.IP) (dirty bool, err error) {
	logrusWithMapping(mapping).Debug("Starting to delete system configuration")
	setName := s.namer.Name(mapping.ObjectMeta)

	// let's find previously configured pods and present pods - anything we might have to
	// purge config for
	inSystemPodsIPs, err := s.ipset.GetIPs(setName)
	if err != nil {
		return false, err
	}
	configuredIPs := enet.IPSliceFromStrings(mapping.Status.ConfiguredAddresses[externalIP.String()].PodIPs)
	toRemovePods := enet.Merge(inSystemPodsIPs, configuredIPs)

	// delete ingress traffic: dnat from external IP
	logrusWithMapping(mapping).Debug("Starting to delete DNAT rules")
	if err = s.iptables.DeleteDNAT(externalIP, mapping); err != nil {
		logrusWithMapping(mapping).Errorf("Error deleting DNAT: %v", err)
		return false, err
	}

	if s.setupSNAT {
		// delete egress traffic: snat to external IP
		logrusWithMapping(mapping).Debug("Starting to delete SNAT rules")
		if err = s.iptables.DeleteSNAT(externalIP, mapping); err != nil {
			logrusWithMapping(mapping).Errorf("Error deleting SNAT: %v", err)
			return false, err
		}
	}

	// configure routing rules - iptables marking to select the correct one
	logrusWithMapping(mapping).Debug("Starting to configure iptables for ip routing rules")
	if err = s.iptables.DeleteMark(externalIP, mapping); err != nil {
		logrusWithMapping(mapping).Errorf("Error deleting Mark rule: %v", err)
		return false, err
	}

	// delete conntrack sessions - all sessions per ip
	logrusWithMapping(mapping).Debug("Starting to remove conntrack sessions")
	for _, ip := range toRemovePods {
		if _, err := s.conntrack.RemoveEntriesDNATingToIP(ip); err != nil {
			return false, err
		}
	}

	// delete ipset holding pods' IPs
	logrusWithMapping(mapping).Debugf("Starting to delete ipset %s", setName)
	if err = s.ipset.DeleteSet(setName); err != nil {
		alreadyOK := false
		if ee, ok := err.(*exec.ExitError); ok {
			errStr := string(ee.Stderr)
			if strings.Contains(errStr, "The set with the given name does not exist\n") {
				logrusWithMapping(mapping).Debugf("The set already doesn't exist, nothing to do.")
				alreadyOK = true
			}
		}
		if !alreadyOK {
			logrusWithMapping(mapping).Errorf("Error deleting SNAT: %v", err)
			return false, err
		}
	}

	// update status
	logrusWithMapping(mapping).Debug("Starting to update status of Mapping in deleteMapping")
	changed := s.updateMappingStatusForDelete(externalIP, mapping)

	logrusWithMapping(mapping).Debugf("Configuration deletion done, Mapping changed: %v", changed)
	return changed, nil
}

func (s *LinuxSyncer) updateMappingStatusForDelete(externalIP net.IP, mapping *v1alpha1.Mapping) bool {
	if _, ok := mapping.Status.ConfiguredAddresses[externalIP.String()]; !ok {
		return false
	}
	delete(mapping.Status.ConfiguredAddresses, externalIP.String())
	return true
}

// SyncMapping syncs Mapping into a configuration in the operating system. Updates Mapping.Status.
func (s *LinuxSyncer) SyncMapping(mapping *v1alpha1.Mapping, svc *v1.Service, eps *v1.Endpoints) (
	dirty bool, err error) {
	logrusWithMapping(mapping).Info("Starting to synchronize configuration")
	// find the local external IP
	localExternalIP := s.filterLocalExternalIP(mapping)
	if localExternalIP == nil {
		msg := "Did not find any local External IPs listed in Mapping.Spec.Addresses"
		logrusWithMapping(mapping).Error(msg)
		return false, fmt.Errorf(msg)
	}

	// find pod IPs, which were last configured in the Operating System
	setName := s.namer.Name(mapping.ObjectMeta)
	inSystemPodsIPs, err := s.ipset.GetIPs(setName)
	if err != nil {
		return false, err
	}
	// find pods that were (most probably) deleted since we've last got Endpoints update
	removedPodsIPs := s.getMissingPods(inSystemPodsIPs, eps)

	// common: synchronize ipset content
	logrusWithMapping(mapping).Debug("Starting to configure ipset")
	logrusWithMapping(mapping).Debugf("Ensuring set %s exists", setName)
	if err = s.ipset.EnsureSetExists(setName, "hash:ip"); err != nil {
		logrusWithMapping(mapping).Errorf("Error creating ipset: %v", err)
		return false, err
	}
	logrusWithMapping(mapping).Debugf("Syncing IPs in ipset %s", setName)
	ips := fromFirstSubsetToNetSlice(eps.Subsets)
	if err = s.ipset.EnsureSetHasOnly(setName, ips); err != nil {
		logrusWithMapping(mapping).Errorf("Error syncing IPs in ipset %s: %v", setName, err)
		return false, err
	}

	// setup egress traffic: fwmark for route selection
	logrusWithMapping(mapping).Debug("Starting to configure Mark rules")
	if err = s.iptables.SetupMark(localExternalIP, mapping); err != nil {
		logrusWithMapping(mapping).Errorf("Error setting up Mark: %v", err)
		return false, err
	}

	// setup ingress traffic: dnat from external IP, checking ports translation and src IPs
	logrusWithMapping(mapping).Debug("Starting to configure DNAT rules")
	if err = s.iptables.SetupDNAT(localExternalIP, mapping, svc, eps, s.setupMasq); err != nil {
		logrusWithMapping(mapping).Errorf("Error setting up DNAT: %v", err)
		return false, err
	}

	if s.setupSNAT {
		// setup egress traffic: snat to external IP
		logrusWithMapping(mapping).Debug("Starting to configure SNAT rules")
		if err = s.iptables.SetupSNAT(localExternalIP, mapping, svc, eps); err != nil {
			logrusWithMapping(mapping).Errorf("Error setting up SNAT: %v", err)
			return false, err
		}
	}

	// common - final tasks: prune conntrack
	for _, ip := range removedPodsIPs {
		s.conntrack.RemoveEntriesDNATingToIP(ip)
	}

	// update Mapping.Status
	var changed bool
	if changed, err = s.updateMappingStatus(localExternalIP, mapping, svc, eps); err != nil {
		logrusWithMapping(mapping).Errorf("Error updating Mapping's Status: %v", err)
		return changed, err
	}

	logrusWithMapping(mapping).Info("Configuration synchronization completed successfully")
	return changed, nil
}

func (s *LinuxSyncer) updateMappingStatus(externalIP net.IP, mapping *v1alpha1.Mapping,
	svc *v1.Service, eps *v1.Endpoints) (bool, error) {
	changed := false

	// set service's VIP
	if mapping.Status.ServiceVIP != svc.Spec.ClusterIP {
		mapping.Status.ServiceVIP = svc.Spec.ClusterIP
		changed = true
	}

	// add configured address to the list
	if _, has := mapping.Status.ConfiguredAddresses[externalIP.String()]; !has {
		mapping.Status.ConfiguredAddresses[externalIP.String()] = v1alpha1.MappingPodIPs{}
		changed = true
	}

	// update lists of pods; remember, we support only 1 Subset in Endpoints
	// check if the list of pods changed at all and update only if changed
	currentList := []string{}
	if len(eps.Subsets) > 0 {
		for _, addr := range eps.Subsets[0].Addresses {
			currentList = append(currentList, addr.IP)
		}
	}
	oldList := mapping.Status.ConfiguredAddresses[externalIP.String()].PodIPs
	if !estrings.CompareSlices(oldList, currentList) {
		new := v1alpha1.MappingPodIPs{PodIPs: currentList}
		mapping.Status.ConfiguredAddresses[externalIP.String()] = new
		changed = true
	}

	return changed, nil
}

func (s *LinuxSyncer) getMissingPods(inSystemPodsIPs []net.IP, eps *v1.Endpoints) []net.IP {
	result := make([]net.IP, len(inSystemPodsIPs))
	copy(result, inSystemPodsIPs)
	if len(eps.Subsets) > 0 {
		for _, addr := range eps.Subsets[0].Addresses {
			currentIP := net.ParseIP(addr.IP)
			result = enet.TryRemove(result, currentIP)
		}
	}
	return result
}

func (s *LinuxSyncer) filterLocalExternalIP(mapping *v1alpha1.Mapping) net.IP {
	result := []net.IP{}
	for _, addr := range mapping.Spec.Addresses {
		ip := net.ParseIP(addr)
		if s.interfaceProvider.IsLocalIP(ip) {
			result = append(result, ip)
		}
	}

	if len(result) == 0 {
		return nil
	}
	if len(result) > 1 {
		logrusWithMapping(mapping).Warnf("Found multiple local IPs, using %s only", result[0].String())
	}
	return result[0]
}
