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

	estrings "github.com/DevFactory/go-tools/pkg/extensions/strings"
	"github.com/DevFactory/go-tools/pkg/nettools"
	"github.com/DevFactory/smartnat/pkg/apis/smartnat/v1alpha1"
	smartnatv1alpha1 "github.com/DevFactory/smartnat/pkg/apis/smartnat/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	pfx = "SmartNat validation failed: "
	// MaxPortsPerProtocol - iptables multiport extension supports max 15 ports as arguments
	MaxPortsPerProtocol    = 15
	defaultInvalidOKStatus = "no"
)

// Scrubber provides validation, scrubbing and safe defaults for objects passed to it.
type Scrubber interface {

	// ScrubMapping does scrubbing on *v1alpha1.Mapping object, including validating ports
	// and setting their defaults if needed, validating CIDR expressions and checking if IP
	// address is local. It also checks for externalIP:port conflicts with mappings in
	// slice passed as the second argument.
	ScrubMapping(sn *v1alpha1.Mapping, others []v1alpha1.Mapping) (valid, dirty bool, validErrMessage string,
		localExternalIP *net.IP)

	// ValidateEndpoints checks if Endpoints have only 0 or 1 Subsets
	ValidateEndpoints(mapping *smartnatv1alpha1.Mapping, endpoints *v1.Endpoints) error
}

type scrubber struct {
	provider nettools.InterfaceProvider
}

// NewScrubber returns a scrubber for SmartNat objects
func NewScrubber(interfaceProvider nettools.InterfaceProvider) Scrubber {
	return &scrubber{
		provider: interfaceProvider,
	}
}

func (s *scrubber) ValidateEndpoints(mapping *smartnatv1alpha1.Mapping, endpoints *v1.Endpoints) error {
	l := len(endpoints.Subsets)
	if l == 0 || l == 1 {
		return nil
	}
	msg := fmt.Sprintf("Expected to find 0 or 1 Subsets in Endpoints, found: %d", l)
	logrusWithMapping(mapping).Warn(msg)
	return fmt.Errorf(msg)
}

func (s *scrubber) ScrubMapping(mapping *v1alpha1.Mapping, others []v1alpha1.Mapping) (valid, dirty bool,
	validErrMessage string, localExternalIP *net.IP) {
	dirty = s.fillDefaults(mapping)
	valid, validErrMessage, localExternalIP = s.validate(mapping, others)
	return
}

func (s *scrubber) validate(mapping *v1alpha1.Mapping, others []v1alpha1.Mapping) (valid bool,
	validErrMessage string, localExternalIP *net.IP) {
	spec := mapping.Spec
	// validate service name
	svcErrors := validation.IsDNS1035Label(spec.ServiceName)
	if svcErrors != nil {
		msg := fmt.Sprintf("Invalid service name format in SmartNat.Spec. %v", strings.Join(svcErrors, ", "))
		logrusWithMapping(mapping).Infof(pfx + msg)
		return false, msg, nil
	}
	// validate addresses
	isValid, errValid, localIP := s.validateAddresses(mapping)
	if !isValid {
		return isValid, errValid, localIP
	}
	// validate allowed sources
	for _, address := range spec.AllowedSources {
		if _, _, err := net.ParseCIDR(address); err != nil {
			msg := fmt.Sprintf("Invalid CIDR in SmartNat.Spec.AllowedSources: %v", address)
			logrusWithMapping(mapping).Infof(pfx + msg)
			return false, msg, localIP
		}
	}
	// validate mode
	mode := strings.ToLower(spec.Mode)
	if mode != "service" {
		msg := fmt.Sprintf("Invalid mode '%v' specified. Valid is 'service'.", spec.Mode)
		logrusWithMapping(mapping).Infof(msg)
		return false, msg, localIP
	}
	// validate ports
	portsValid, portsErrMsg := s.validatePorts(mapping)
	if !portsValid {
		return false, portsErrMsg, localIP
	}
	// we have to check here all other mappings for conflicts of external endpoints
	// TODO: the same information can be used to dynamically allocate port numbers and External IPs
	othersValid, othersErrMsg := s.validateOthers(mapping, others)
	if !othersValid {
		return false, othersErrMsg, localIP
	}

	return true, "", localIP
}

func (s *scrubber) validateOthers(mapping *v1alpha1.Mapping, others []v1alpha1.Mapping) (valid bool,
	validErrMsg string) {
	for _, other := range others {
		// skip conflict with self
		if other.Name == mapping.Name && other.Namespace == mapping.Namespace {
			continue
		}
		// there's no conflict with other if we use different externalIPs
		commonExternalIPs := estrings.GetCommon(other.Spec.Addresses, mapping.Spec.Addresses)
		if len(commonExternalIPs) == 0 {
			continue
		}
		// check if we use the same external port protocol and number
		for _, op := range other.Spec.Ports {
			for _, mp := range mapping.Spec.Ports {
				if op.Protocol == mp.Protocol && op.Port == mp.Port {
					msg := fmt.Sprintf("Found conflicting external endpoint: IP(s): %s; protocol: %s; "+
						"port: %d", strings.Join(commonExternalIPs, ","), op.Protocol, op.Port)
					logrusWithMapping(mapping).Info(pfx + msg)
					return false, msg
				}
			}
		}
	}
	return true, ""
}

func (s *scrubber) validateAddresses(mapping *v1alpha1.Mapping) (valid bool, validErrMsg string,
	localExternalIP *net.IP) {
	if len(mapping.Spec.Addresses) == 0 {
		msg := "No address in SmartNat.Spec.Addresses."
		logrusWithMapping(mapping).Info(pfx + msg)
		return false, msg, nil
	}
	localIPsFound := 0
	var externalIP net.IP
	for _, address := range mapping.Spec.Addresses {
		ip := net.ParseIP(address)
		if ip == nil {
			msg := fmt.Sprintf("Invalid address in SmartNat.Spec.Addresses: %v", address)
			logrusWithMapping(mapping).Infof(pfx + msg)
			return false, msg, nil
		}
		if s.provider.IsLocalIP(ip) {
			localIPsFound++
			externalIP = ip
		}
	}
	if localIPsFound > 1 {
		msg := "More than 1 external IP address in Mapping is local"
		logrusWithMapping(mapping).Info(pfx + msg)
		return false, msg, &externalIP
	}
	if localIPsFound == 0 {
		logrusWithMapping(mapping).Info("No external IP address in mapping is local.")
		return true, "", nil
	}
	return true, "", &externalIP
}

func (s *scrubber) validatePorts(mapping *v1alpha1.Mapping) (bool, string) {
	if len(mapping.Spec.Ports) == 0 {
		msg := "No port in SmartNat.Spec.Ports."
		logrusWithMapping(mapping).Info(pfx + msg)
		return false, msg
	}
	usedPorts := map[string]bool{}
	tcpCount, udpCount := 0, 0
	for _, port := range mapping.Spec.Ports {
		if port.Port == 0 || port.Port > 65535 {
			msg := "Wrong port value in SmartNat.Spec.Ports. Can't be <=0 or >65535."
			logrusWithMapping(mapping).Info(pfx + msg)
			return false, msg
		}
		if port.ServicePort == 0 || port.ServicePort > 65535 {
			msg := "Wrong servicePort value in SmartNat.Spec.Ports. Can't be <=0 or >65535."
			logrusWithMapping(mapping).Info(pfx + msg)
			return false, msg
		}
		proto := strings.ToLower(port.Protocol)
		if proto != "tcp" && proto != "udp" {
			msg := fmt.Sprintf("Wrong protocol value in SmartNat.Spec.Ports. Only 'Tcp' and 'Udp'"+
				" are supported. Found: %s", port.Protocol)
			logrusWithMapping(mapping).Info(pfx + msg)
			return false, msg
		}
		if proto == "tcp" {
			tcpCount++
		} else {
			udpCount++
		}
		key := fmt.Sprintf("%s-%d", port.Protocol, port.Port)
		if _, found := usedPorts[key]; found {
			msg := fmt.Sprintf("Wrong value in SmartNat.Spec.Ports. Port '%s' is used multiple times", key)
			logrusWithMapping(mapping).Info(pfx + msg)
			return false, msg
		}
		usedPorts[key] = true
	}
	if tcpCount > MaxPortsPerProtocol {
		msg := fmt.Sprintf("Too many TCP ports configured. Max is %d, found %d",
			MaxPortsPerProtocol, tcpCount)
		logrusWithMapping(mapping).Info(pfx + msg)
		return false, msg
	}
	if udpCount > MaxPortsPerProtocol {
		msg := fmt.Sprintf("Too many UDP ports configured. Max is %d, found %d",
			MaxPortsPerProtocol, udpCount)
		logrusWithMapping(mapping).Info(pfx + msg)
		return false, msg
	}
	return true, ""
}

func (s *scrubber) fillDefaults(mapping *v1alpha1.Mapping) bool {
	dirty := false
	if mapping.Status.Invalid == "" {
		mapping.Status.Invalid = defaultInvalidOKStatus
		dirty = true
	}
	if len(mapping.Spec.AllowedSources) == 0 {
		mapping.Spec.AllowedSources = []string{"0.0.0.0/0"}
		dirty = true
	}
	// fill defaults for ports
	for i := range mapping.Spec.Ports {
		if mapping.Spec.Ports[i].ServicePort == 0 {
			mapping.Spec.Ports[i].ServicePort = mapping.Spec.Ports[i].Port
			dirty = true
		}
		if mapping.Spec.Ports[i].Protocol == "" {
			mapping.Spec.Ports[i].Protocol = "tcp"
			dirty = true
		}
		if mapping.Spec.Ports[i].Protocol != strings.ToLower(mapping.Spec.Ports[i].Protocol) {
			mapping.Spec.Ports[i].Protocol = strings.ToLower(mapping.Spec.Ports[i].Protocol)
			dirty = true
		}
	}
	if mapping.Status.ConfiguredAddresses == nil {
		mapping.Status.ConfiguredAddresses = make(map[string]v1alpha1.MappingPodIPs)
		dirty = true
	}
	return dirty
}
