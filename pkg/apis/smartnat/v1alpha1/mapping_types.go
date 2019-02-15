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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MappingPort describes mapping for a single port
type MappingPort struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`
	// +kubebuilder:validation:Enum=tcp,udp
	Protocol string `json:"protocol,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	ServicePort int32 `json:"servicePort,omitempty"`
}

// MappingSpec defines the desired state of Mapping
type MappingSpec struct {
	// +kubebuilder:validation:MinItems=1
	Addresses []string `json:"addresses"`
	// +kubebuilder:validation:MinItems=0
	AllowedSources []string `json:"allowedSources"`
	// +kubebuilder:validation:MaxLength=62
	// +kubebuilder:validation:MinLength=1
	ServiceName string `json:"serviceName"`
	// +kubebuilder:validation:Enum=service
	Mode string `json:"mode"`
	// +kubebuilder:validation:MinItems=1
	Ports []MappingPort `json:"ports"`
}

// MappingPodIPs lists IPs of pods
type MappingPodIPs struct {
	PodIPs []string `json:"podIPs,omitempty"`
}

// MappingStatus defines the observed state of Mapping
type MappingStatus struct {
	Invalid             string                   `json:"invalid,omitempty"`
	ServiceVIP          string                   `json:"serviceVIP,omitempty"`
	ConfiguredAddresses map[string]MappingPodIPs `json:"configuredAddresses,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Mapping is the Schema for the mappings API
// +k8s:openapi-gen=true
// TODO: status subresources are only alpha and disabed by default on 1.10. add "+" here when
// redy to enable. Sync with Maping update code in mapping_controller.go
// kubebuilder:subresource:status
type Mapping struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MappingSpec   `json:"spec,omitempty"`
	Status MappingStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MappingList contains a list of Mapping
type MappingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Mapping `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Mapping{}, &MappingList{})
}
