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
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/intstr"

	nt "github.com/DevFactory/go-tools/pkg/nettools"
	ntmocks "github.com/DevFactory/go-tools/pkg/nettools/mocks"
	"github.com/stretchr/testify/mock"

	"github.com/DevFactory/smartnat/pkg/apis/smartnat/v1alpha1"
	"github.com/DevFactory/smartnat/pkg/controller/mapping/mocks"
	"github.com/DevFactory/smartnat/pkg/controller/mapping/testhelpers"

	"github.com/onsi/gomega"
	"golang.org/x/net/context"
	v1core "k8s.io/api/core/v1"
	v1meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	c                client.Client
	mappingName            = "smartnat-test"
	mappingNamespace       = "default"
	serviceName            = "test"
	nodeName               = "node1"
	servicePort      int32 = 8080
	externalIP             = "10.10.10.10"
	mappingKey             = types.NamespacedName{Name: mappingName, Namespace: mappingNamespace}
	expectedRequest        = reconcile.Request{NamespacedName: mappingKey}
	testMapping            = &v1alpha1.Mapping{
		ObjectMeta: v1meta.ObjectMeta{
			Name:      mappingName,
			Namespace: mappingNamespace,
		},
		Spec: v1alpha1.MappingSpec{
			Addresses: []string{
				externalIP,
			},
			AllowedSources: []string{
				"10.10.100.0/24",
			},
			Ports: []v1alpha1.MappingPort{
				{
					Port:        9090,
					ServicePort: 8090,
					Protocol:    "udp",
				},
				{
					Port:     servicePort,
					Protocol: "tcp",
				},
			},
			ServiceName: serviceName,
			Mode:        "service",
		},
	}
	service = &v1core.Service{
		ObjectMeta: v1meta.ObjectMeta{
			Name:      serviceName,
			Namespace: mappingNamespace,
		},
		Spec: v1core.ServiceSpec{
			Type:            v1core.ServiceTypeClusterIP,
			SessionAffinity: v1core.ServiceAffinityNone,
			Ports: []v1core.ServicePort{
				v1core.ServicePort{
					Port:       servicePort,
					Protocol:   v1core.ProtocolTCP,
					TargetPort: intstr.FromInt(10000),
				},
			},
		},
	}
	endpoints = &v1core.Endpoints{
		ObjectMeta: v1meta.ObjectMeta{
			Name:      serviceName,
			Namespace: mappingNamespace,
		},
		Subsets: []v1core.EndpointSubset{
			v1core.EndpointSubset{
				Addresses: []v1core.EndpointAddress{
					v1core.EndpointAddress{
						IP:       "172.16.10.10",
						NodeName: &nodeName,
					},
				},
				Ports: []v1core.EndpointPort{
					v1core.EndpointPort{
						Port:     10000,
						Protocol: v1core.ProtocolTCP,
					},
				},
			},
		},
	}
)

const timeout = time.Second * 5

type reconcilerDeps struct {
	ifaceProvider     nt.InterfaceProvider
	iprSmartNatHelper *mocks.IPRouteSmartNatHelper
	syncer            *mocks.Syncer
	scrubber          Scrubber
	heartbeatChan     chan string
}

func getReconcilerWithMocks(t *testing.T, mgr manager.Manager) (reconcile.Reconciler, *reconcilerDeps) {
	cfg := testhelpers.GetTestConfig(t)

	ifaceProvider, _, err := ntmocks.NewMockInterfaceProvider("eth[0-9]+", true)
	if err != nil {
		t.Logf("Cannot create ifaceProvider object: %v", err)
		t.Fail()
	}

	ipRouteSNHelper := &mocks.IPRouteSmartNatHelper{}
	syncer := &mocks.Syncer{}
	scrubber := NewScrubber(ifaceProvider, cfg)
	heartbeatChan := make(chan string, 1)

	reconciler := newReconciler(mgr, cfg, ifaceProvider, ipRouteSNHelper, syncer, scrubber, heartbeatChan)
	return reconciler, &reconcilerDeps{
		ifaceProvider:     ifaceProvider,
		iprSmartNatHelper: ipRouteSNHelper,
		syncer:            syncer,
		scrubber:          scrubber,
		heartbeatChan:     heartbeatChan,
	}
}

type testInstance struct {
	deps     *reconcilerDeps
	stopFunc func()
	requests chan reconcile.Request
}

func setupTestInstance(t *testing.T, g *gomega.GomegaWithT) *testInstance {
	// Setup the Manager and Controller.  Wrap the Controller Reconcile function so it writes each request to a
	// channel when it is finished.
	mgr, err := manager.New(cfg, manager.Options{})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	c = mgr.GetClient()
	reconciler, reconcilerDeps := getReconcilerWithMocks(t, mgr)
	recFn, requests := SetupTestReconcile(reconciler)
	g.Expect(add(mgr, recFn)).NotTo(gomega.HaveOccurred())

	// setup api objects
	err = c.Create(context.TODO(), service)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	err = c.Create(context.TODO(), endpoints)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Start TestManager handling requests with wrapped Reconcile function
	stopMgr, mgrStopped := StartTestManager(mgr, g)

	stopFunc := func() {
		close(stopMgr)
		mgrStopped.Wait()
	}

	return &testInstance{
		deps:     reconcilerDeps,
		stopFunc: stopFunc,
		requests: requests,
	}
}

func TestReconcile(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	instance := setupTestInstance(t, g)
	defer instance.stopFunc()

	// Setup mock expectations
	instance.deps.syncer.
		On("SyncMapping", mock.AnythingOfType("*v1alpha1.Mapping"),
			mock.AnythingOfType("*v1.Service"), mock.AnythingOfType("*v1.Endpoints")).
		Return(true, nil)

	// Create the Mapping object and expect the Reconcile call, including heartbeat channel message
	err := c.Create(context.TODO(), testMapping)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Eventually(instance.deps.heartbeatChan, timeout).Should(gomega.Receive(gomega.ContainSubstring("OK")))
	g.Eventually(instance.requests, timeout).Should(gomega.Receive(gomega.Equal(expectedRequest)))

	// Fetch the Mapping from API server
	mapping := &v1alpha1.Mapping{}
	g.Eventually(func() error { return c.Get(context.TODO(), mappingKey, mapping) }, timeout).
		Should(gomega.Succeed())

	// validate mapping
	validateMappingFromAPIServer(g, mapping)

	// Delete the Mapping and expect Reconcile to be called for deletion
	g.Expect(c.Delete(context.TODO(), mapping)).NotTo(gomega.HaveOccurred())
	g.Eventually(instance.requests, timeout).Should(gomega.Receive(gomega.Equal(expectedRequest)))
	g.Eventually(func() error { return c.Get(context.TODO(), mappingKey, mapping) }, timeout).
		Should(gomega.Succeed())

	// check mock calls
	instance.deps.iprSmartNatHelper.AssertExpectations(t)
	instance.deps.syncer.AssertExpectations(t)
}

func validateMappingFromAPIServer(g *gomega.GomegaWithT, m *v1alpha1.Mapping) {
	g.Expect(m.Name).To(gomega.Equal(mappingName))
	g.Expect(m.Namespace).To(gomega.Equal(mappingNamespace))
	g.Expect(m.ObjectMeta.CreationTimestamp).NotTo(gomega.BeZero())
	g.Expect(m.ObjectMeta.Finalizers).To(gomega.HaveLen(1))
	g.Expect(m.Status).ToNot(gomega.BeNil())
	g.Expect(m.Status.Invalid).To(gomega.Equal("no"))
	g.Expect(m.Status.ConfiguredAddresses).To(gomega.HaveLen(1))
	_, ok := m.Status.ConfiguredAddresses[externalIP]
	g.Expect(ok).To(gomega.BeTrue())
}
