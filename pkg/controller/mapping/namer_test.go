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
	"reflect"
	"testing"

	"github.com/DevFactory/smartnat/pkg/controller/mapping"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_nameProvider_Name(t *testing.T) {
	tests := []struct {
		name       string
		objectMeta metav1.ObjectMeta
		want       mapping.ShortName
	}{
		{
			name: "test generates correct short unique service name based on ObjectMeta",
			objectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "test",
			},
			want: "MDICIJE6TVWKE333NKHBEORO",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mapping.NewNamer()
			if got := p.Name(tt.objectMeta); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("nameProvider.Name() = %v, want %v", got, tt.want)
			}
		})
	}
}
