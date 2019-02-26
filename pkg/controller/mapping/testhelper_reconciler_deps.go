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

	cmdmock "github.com/DevFactory/go-tools/pkg/linux/command/mock"
	"github.com/DevFactory/go-tools/pkg/nettools"
	ntmocks "github.com/DevFactory/go-tools/pkg/nettools/mocks"
	netoolsth "github.com/DevFactory/go-tools/pkg/nettools/testhelpers"
	"github.com/DevFactory/smartnat/pkg/config"
	"github.com/DevFactory/smartnat/pkg/controller/mapping/mocks"
	"github.com/DevFactory/smartnat/pkg/controller/mapping/testhelpers"
)

type reconcilerDependencies struct {
	config                            *config.Config
	ifaceProvider                     nettools.InterfaceProvider
	ifaceProviderUpdateChan           chan time.Time
	ifaceProviderExpectedInterfaces   []nettools.Interface
	ipRouteSmartNatHelper             IPRouteSmartNatHelper
	ipRouteSmartNatHelperExecutor     *cmdmock.MockExecutor
	ipRouteSmartNatHelperFileOperator *ntmocks.MockSimpleFileOperator
	ipRouteSmartNatHelperUpdateChan   chan time.Time
	mappingSyncer                     Syncer
	scrubber                          Scrubber
	heartbeatChan                     chan string
}

func getReconcilerDependencies(t *testing.T) *reconcilerDependencies {
	cfg := testhelpers.GetTestConfig(t)

	ifaceProvider, ifaceUpdateChan, err := ntmocks.NewMockInterfaceProvider("eth[0-9]+", true)
	if err != nil {
		t.Logf("Cannot create ifaceProvider object: %v", err)
		t.Fail()
	}

	expectedInterfaces := ntmocks.MockInterfacesEth
	expectedExecInfo := netoolsth.GetExecInfosForIPRouteInterfaceInit(expectedInterfaces)
	execMock := cmdmock.NewMockExecutorFromInfos(t, expectedExecInfo...)
	ioOp := netoolsth.GetMockIOOpProviderWithEmptyRTTablesFile([]byte{}, len(expectedInterfaces), true)
	routeUpdateChan := make(chan time.Time)
	heartbeatChan := make(chan string)
	ipRouteHelper := &ntmocks.IPRouteHelper{}
	ipRouteSmartNatHelper := NewChanIPRouteSmartNatHelper(ipRouteHelper, ifaceProvider, routeUpdateChan, false, 1)

	return &reconcilerDependencies{
		config:                            cfg,
		ifaceProvider:                     ifaceProvider,
		ifaceProviderExpectedInterfaces:   expectedInterfaces,
		ifaceProviderUpdateChan:           ifaceUpdateChan,
		ipRouteSmartNatHelper:             ipRouteSmartNatHelper,
		ipRouteSmartNatHelperExecutor:     execMock,
		ipRouteSmartNatHelperFileOperator: ioOp,
		ipRouteSmartNatHelperUpdateChan:   routeUpdateChan,
		scrubber:                          NewScrubber(ifaceProvider),
		mappingSyncer:                     &mocks.Syncer{},
		heartbeatChan:                     heartbeatChan,
	}
}
