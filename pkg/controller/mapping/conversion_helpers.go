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
	"net"

	"k8s.io/api/core/v1"
)

func fromFirstSubsetToNetSlice(subsets []v1.EndpointSubset) []net.IP {
	if subsets == nil || len(subsets) == 0 {
		return []net.IP{}
	}
	addresses := subsets[0].Addresses
	result := make([]net.IP, 0, len(addresses))
	for _, a := range addresses {
		result = append(result, net.ParseIP(a.IP))
	}
	return result
}
