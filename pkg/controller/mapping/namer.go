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
	"crypto/sha256"
	"encoding/base32"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ShortName is a at most 24 characters long name based on original metav1.ObjectMeta namespace
// and name
type ShortName = string

// Namer provides a consistent, unique and concise names for full namespace/name names
//
// Name returns a concise name for a give namespaced name
type Namer interface {
	Name(objectMeta metav1.ObjectMeta) ShortName
}

// NewNamer returns new implementation of Namer
func NewNamer() Namer {
	return &nameProvider{}
}

type nameProvider struct{}

func (p *nameProvider) Name(objectMeta metav1.ObjectMeta) ShortName {
	buf := []byte(fmt.Sprintf("%s/%s", objectMeta.Namespace, objectMeta.Name))
	hash := sha256.Sum256([]byte(buf))
	encoded := base32.StdEncoding.EncodeToString(hash[:])
	return ShortName(encoded[:24])
}
