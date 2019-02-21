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
	"testing"

	ntmocks "github.com/DevFactory/go-tools/pkg/nettools/mocks"
	"github.com/DevFactory/smartnat/pkg/apis/smartnat/v1alpha1"
	"github.com/DevFactory/smartnat/pkg/controller/mapping"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	v1meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_scrubber_ScrubMapping(t *testing.T) {
	// OK uses all features
	valid1 := &v1alpha1.Mapping{
		ObjectMeta: v1meta.ObjectMeta{
			Name:      "smartnat-1",
			Namespace: "default",
		},
		Spec: v1alpha1.MappingSpec{
			Addresses: []string{
				"192.168.3.1",
				"10.10.10.10",
			},
			AllowedSources: []string{
				"10.10.100.0/24",
				"192.168.3.2/32",
				"10.0.0.1/16",
			},
			Ports: []v1alpha1.MappingPort{
				{
					Port:        8080,
					ServicePort: 8090,
					Protocol:    "UDP",
				},
				{
					Port:     8080,
					Protocol: "TCP",
				},
				{
					Port: 8090,
				},
			},
			ServiceName: "test",
			Mode:        "service",
		},
	}
	// OK empty allowedsources
	validEmptyAS := valid1.DeepCopy()
	validEmptyAS.Spec.AllowedSources = nil
	// OK serviceport 0 - same as port
	validServicePort0 := valid1.DeepCopy()
	validServicePort0.Spec.Ports[1].ServicePort = 0
	// OK addresses - none is local
	validAddressesNoLocal := valid1.DeepCopy()
	validAddressesNoLocal.Spec.Addresses[1] = "192.168.7.1"
	// wrong cidr allowedsources
	wrongCIDRAllowedSources := valid1.DeepCopy()
	wrongCIDRAllowedSources.Spec.AllowedSources = []string{"300.100.200.0"}
	// wrong service name
	wrongService := valid1.DeepCopy()
	wrongService.Spec.ServiceName = "cool-name!"
	// wrong mode
	wrongMode := valid1.DeepCopy()
	wrongMode.Spec.Mode = "direct"
	// wrong empty addresses
	wrongEmptyAddresses := valid1.DeepCopy()
	wrongEmptyAddresses.Spec.Addresses = nil
	// wrong addresses
	wrongAddresses := valid1.DeepCopy()
	wrongAddresses.Spec.Addresses[0] = "257.12.12.12"
	// wrong addresses - 2 are local
	wrongAddresses2Local := valid1.DeepCopy()
	wrongAddresses2Local.Spec.Addresses[0] = "192.168.1.1"
	// wrong empty ports
	wrongEmptyPorts := valid1.DeepCopy()
	wrongEmptyPorts.Spec.Ports = nil
	// wrong port 0
	wrongPort0 := valid1.DeepCopy()
	wrongPort0.Spec.Ports[0].Port = 0
	// wrong port 123456
	wrongPort123456 := valid1.DeepCopy()
	wrongPort123456.Spec.Ports[0].Port = 123456
	// wrong service port 123456
	wrongServicePort123456 := valid1.DeepCopy()
	wrongServicePort123456.Spec.Ports[0].ServicePort = 123456
	// wrong protocol
	wrongProto := valid1.DeepCopy()
	wrongProto.Spec.Ports[0].Protocol = "SCTP"
	// wrong reused port number
	wrongReusedPort := valid1.DeepCopy()
	wrongReusedPort.Spec.Ports[2].Port = 8080
	// wrong - reused port in other Mapping
	wrongReusedPortOther := valid1.DeepCopy()
	wrongReusedPortOther.Name = "OtherMapping"
	wrongReusedPortOther.Spec.Addresses = []string{
		"192.168.3.1",
	}
	wrongReusedPortOther.Spec.Ports = []v1alpha1.MappingPort{
		{
			Port:        8080,
			ServicePort: 8090,
			Protocol:    "udp",
		},
	}
	// wrong - uses to many ports per protocol
	wrongTooManyTcpPorts := valid1.DeepCopy()
	wrongTooManyUdpPorts := valid1.DeepCopy()
	var i int32
	for i = 0; i < mapping.MaxPortsPerProtocol+1; i++ {
		wrongTooManyTcpPorts.Spec.Ports = append(wrongTooManyTcpPorts.Spec.Ports, v1alpha1.MappingPort{
			Port:        i + 2000,
			Protocol:    "tcp",
			ServicePort: i + 2000,
		})
		wrongTooManyUdpPorts.Spec.Ports = append(wrongTooManyUdpPorts.Spec.Ports, v1alpha1.MappingPort{
			Port:        i + 2000,
			Protocol:    "udp",
			ServicePort: i + 2000,
		})
	}

	tests := []struct {
		name   string
		sn     *v1alpha1.Mapping
		others []v1alpha1.Mapping
		want   bool
	}{
		{
			name: "Validates OK",
			sn:   valid1,
			want: true,
		},
		{
			name: "Validates OK - empty AllowedSources",
			sn:   validEmptyAS,
			want: true,
		},
		{
			name: "Validates OK - uses default servicePort",
			sn:   validServicePort0,
			want: true,
		},
		{
			name: "Validates OK - uses no local External IPs",
			sn:   validAddressesNoLocal,
			want: true,
		},
		{
			name: "Fails because of service name",
			sn:   wrongService,
			want: false,
		},
		{
			name: "Fails because of wrong mode",
			sn:   wrongMode,
			want: false,
		},
		{
			name: "Fails because of empty addresses",
			sn:   wrongEmptyAddresses,
			want: false,
		},
		{
			name: "Fails because of wrong addresses",
			sn:   wrongAddresses,
			want: false,
		},
		{
			name: "Fails because of using 2 local External IPs",
			sn:   wrongAddresses2Local,
			want: false,
		},
		{
			name: "Fails because of empty ports",
			sn:   wrongEmptyPorts,
			want: false,
		},
		{
			name: "Fails because of using port 0",
			sn:   wrongPort0,
			want: false,
		},
		{
			name: "Fails because of using port 123456",
			sn:   wrongPort123456,
			want: false,
		},
		{
			name: "Fails because of using service port 123456",
			sn:   wrongServicePort123456,
			want: false,
		},
		{
			name: "Fails because of using wrong protocol",
			sn:   wrongProto,
			want: false,
		},
		{
			name: "Fails because of reusing port 8080 TCP",
			sn:   wrongReusedPort,
			want: false,
		},
		{
			name:   "Fails because reuses external endpoint of other mapping",
			sn:     valid1,
			want:   false,
			others: []v1alpha1.Mapping{*wrongReusedPortOther},
		},
		{
			name: "Fails because has invalid AllowedSources",
			sn:   wrongCIDRAllowedSources,
			want: false,
		},
		{
			name: "Fails because uses too many TCP ports",
			sn:   wrongTooManyTcpPorts,
			want: false,
		},
		{
			name: "Fails because uses too many UDP ports",
			sn:   wrongTooManyUdpPorts,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ifaceProvider, _, err := ntmocks.NewMockInterfaceProvider("eth[0-9]+", true)
			if err != nil {
				t.Logf("Cannot create ifaceProvider object: %v", err)
				t.Fail()
			}
			s := mapping.NewScrubber(ifaceProvider)
			valid, _, _, _ := s.ScrubMapping(tt.sn, tt.others)
			if valid != tt.want {
				t.Errorf("scrubber.ScrubMapping() got = %v, want %v", valid, tt.want)
			}
			if !valid {
				return
			}
			assert.Equal(t, int32(8080), tt.sn.Spec.Ports[1].ServicePort)
			assert.Equal(t, int32(8090), tt.sn.Spec.Ports[2].ServicePort)
			assert.Equal(t, "tcp", tt.sn.Spec.Ports[2].Protocol)
		})
	}
}

func Test_scrubber_ValidateEndpoints(t *testing.T) {
	tests := []struct {
		name      string
		endpoints *v1.Endpoints
		wantErr   bool
	}{
		{
			name: "valid empty Subsets",
			endpoints: &v1.Endpoints{
				Subsets: []v1.EndpointSubset{},
			},
			wantErr: false,
		},
		{
			name: "valid one Subset",
			endpoints: &v1.Endpoints{
				Subsets: []v1.EndpointSubset{
					{
						Addresses: []v1.EndpointAddress{
							{
								Hostname: "test",
								IP:       "127.0.0.1",
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid two Subsets",
			endpoints: &v1.Endpoints{
				Subsets: []v1.EndpointSubset{
					{
						Addresses: []v1.EndpointAddress{
							{
								Hostname: "test",
								IP:       "127.0.0.1",
							},
						},
					},
					{
						Addresses: []v1.EndpointAddress{
							{
								Hostname: "test",
								IP:       "127.0.0.1",
							},
						},
					},
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, _, _ := ntmocks.NewMockInterfaceProvider(".*", false)
			s := mapping.NewScrubber(provider)
			if err := s.ValidateEndpoints(nil, tt.endpoints); (err != nil) != tt.wantErr {
				t.Errorf("scrubber.ValidateEndpoints() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
