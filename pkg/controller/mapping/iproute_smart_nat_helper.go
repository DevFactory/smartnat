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
	"io/ioutil"
	"net"
	"time"

	etime "github.com/DevFactory/go-tools/pkg/extensions/time"
	"github.com/DevFactory/go-tools/pkg/nettools"
	nt "github.com/DevFactory/go-tools/pkg/nettools"
	"github.com/sirupsen/logrus"

	"github.com/DevFactory/go-tools/pkg/linux/command"
)

const (
	interfaceMarkStart = 0x0800
)

// IPRouteSmartNatHelper is a customized nettools.IPRouteProvider,
// which supports auto-refreshing of ip addresses and routes
type IPRouteSmartNatHelper interface {
	EnsureOnlyOneIPRuleExistsForFwMark(rule nettools.IPRule) ([]nettools.IPRule, time.Duration, error)
	etime.Refresher
}

type ipRouteSmartNatHelper struct {
	nettools.IPRouteHelper
	etime.Refresher
}

// NewIPRouteSmartNatHelper creates IPRouteSmartNatHelper with periodic autorefreshing for them
func NewIPRouteSmartNatHelper(routeHelperExecutor command.Executor, routeHelperIoOp nettools.SimpleFileOperator,
	ifaceProvider nettools.InterfaceProvider, refreshPeriod time.Duration, gwAddressOffset int32) IPRouteSmartNatHelper {
	ipRouteHelper := nettools.NewExecIPRouteHelper(routeHelperExecutor, routeHelperIoOp)
	action := initRefreshAction(ipRouteHelper, ifaceProvider, gwAddressOffset)
	refresher := etime.NewTickerRefresher(action, true, true, refreshPeriod)
	return &ipRouteSmartNatHelper{
		IPRouteHelper: ipRouteHelper,
		Refresher:     refresher,
	}
}

// NewChanIPRouteSmartNatHelper creates IPRouteSmartNatHelper with routing rules refresh
// ran every time there's a new message on the update channel
func NewChanIPRouteSmartNatHelper(ipRouteHelper nettools.IPRouteHelper, ifaceProvider nettools.InterfaceProvider,
	updateChan chan time.Time, refreshOnCreate bool, gwAddressOffset int32) IPRouteSmartNatHelper {
	action := initRefreshAction(ipRouteHelper, ifaceProvider, gwAddressOffset)
	refresher := etime.NewRefresher(updateChan, action, refreshOnCreate, true)
	return &ipRouteSmartNatHelper{
		IPRouteHelper: ipRouteHelper,
		Refresher:     refresher,
	}
}

func initRefreshAction(ipRouteHelper nettools.IPRouteHelper,
	ifaceProvider nettools.InterfaceProvider, gwAddressOffset int32) func() {
	ifaceProvider.StartRefreshing()
	action := func() {
		runReloading(ifaceProvider, ipRouteHelper, gwAddressOffset)
	}
	return action
}

func runReloading(ifaceProvider nettools.InterfaceProvider, routeHelper nettools.IPRouteHelper, gwAddressOffset int32) {
	logrus.Debug("Starting to reload interfaces and ip route")
	ifaces, err := ifaceProvider.Interfaces()
	if err != nil {
		logrus.Errorf("Error reloading information about interfaces, not updating routing rules: %v", err)
		return
	}
	logrus.Debug("Passing loaded interfaces to IPRouteHelper")
	if _, err := routeHelper.InitializeRoutingTablesPerInterface(ifaces); err != nil {
		logrus.Errorf("Error refreshing routing rules based on interface info passed: %v", err)
	}
	if err := disableRpFilter("all"); err != nil {
		logrus.Errorf("Error when disabling rp_filter for 'all': %v", err)
	}
	logrus.Debug("Starting to configure network interfaces")
	for _, iface := range ifaces {
		logrus.Debugf("Processing interface %s", iface.GetName())
		if err := disableRpFilter(iface.GetName()); err != nil {
			logrus.Warnf("Error when disabling rp_filter on interface %s: %v", iface.GetName(), err)
			continue
		}
		mark := getFwMarkForInterface(iface)
		rule := nettools.IPRule{
			FwMark:         mark,
			RouteTableName: iface.GetName(),
		}
		if _, _, err := routeHelper.EnsureOnlyOneIPRuleExistsForFwMark(rule); err != nil {
			logrus.Errorf("Error setting up ip routing rule for interface %s and mark %x: %v", iface.GetName(),
				mark, err)
		}
		addrs, err := iface.Addrs()
		if err != nil {
			logrus.Warnf("Couldn't list addresses of interface %s: %v", iface.GetName(), err)
			continue
		}
		if len(addrs) == 0 {
			logrus.Warnf("Interface %s has no IPs assigned, won't generate any routing entries in its route table",
				iface.GetName())
			continue
		}
		if err := setupRouteEntries(routeHelper, iface, gwAddressOffset); err != nil {
			logrus.Errorf("Error setting up route entries for interface %s in route table %s: %v", iface.GetName(),
				iface.GetName(), err)
		}
	}
	logrus.Debug("Interfaces and ip route reload done")
}

func setupRouteEntries(routeHelper nettools.IPRouteHelper, iface nettools.Interface, gwAddressOffset int32) error {
	addrs, _ := iface.Addrs()
	addr := addrs[0]
	logrus.Debugf("Configuring routing on interface %s based on address %s", iface.GetName(), addr.String())
	localSubnet := net.IPNet{
		IP:   addr.IP.Mask(addr.Mask),
		Mask: addr.Mask,
	}
	logrus.Debugf("Detected subnet address: %s", localSubnet.String())
	logrus.Debugf("Getting gateway address using offset: %d", gwAddressOffset)
	gwIP, err := nt.OffsetAddr(localSubnet, gwAddressOffset)
	if err != nil {
		logrus.Errorf("Error getting offset of %d from IP %v: %v", gwAddressOffset, gwIP, err)
		return err
	}
	logrus.Debugf("Detected gateway address: %s", gwIP.String())
	routeEntries := []nt.IPRouteEntry{
		{
			TableName:    iface.GetName(),
			TargetPrefix: localSubnet.String(),
			Mode:         "dev",
			Gateway:      iface.GetName(),
			Options:      "scope link",
		},
		{
			TableName:    iface.GetName(),
			TargetPrefix: "default",
			Mode:         "via",
			Gateway:      gwIP.String(),
			Options:      fmt.Sprintf("dev %s", iface.GetName()),
		},
	}
	if _, err := routeHelper.EnsureRoutes(routeEntries); err != nil {
		logrus.Errorf("Error adding route entries in table %s, with local subnet %s and gw %s; error: %v",
			iface.GetName(), localSubnet.String(), gwIP.String(), err)
		return err
	}

	return nil
}

func getFwMarkForInterface(iface nettools.Interface) int {
	return interfaceMarkStart + iface.GetIndex()
}

func disableRpFilter(ifaceName string) error {
	path := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/rp_filter", ifaceName)
	return ioutil.WriteFile(path, []byte("0"), 0644)
}
