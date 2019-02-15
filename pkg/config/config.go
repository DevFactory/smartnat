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

package config

import (
	"fmt"
	"os"
	"regexp"

	eos "github.com/DevFactory/go-tools/pkg/extensions/os"
)

// Config holds all configuration variables required by smart-nat-controller
type Config struct {
	InterfaceNamePattern          string
	SetupMasquerade               bool
	SetupSNAT                     bool
	DefaultGWInterface            string
	IPTablesTimeoutSec            int
	EnableDebug                   bool
	InterfaceAutorefreshPeriodSec int
	MaxFinalizeWaitMinutes        int
	GwAddressOffset               int32
}

const (
	// snctrlrInterfacesPattern configures a string with regexp for network interface names
	// in the operating system. Only matching interfaces and their addresses will be serviced by smartnat.
	snctrlrInterfacesPattern = "SNCTRLR_INTERFACES_PATTERN"
	// snctrlrSetupMasquerade defines a true/false bool variable that enables masquerading of traffic
	// coming to smartnat instance and then leaving to cluster pods. The traffic is masqueraded as if
	// it is coming from the smartnat instance itself. You generally want this to be set to true, unless
	// your smartnat instance is acting as routing gateway for traffic outgoing from the pods/workers.
	snctrlrSetupMasquerade = "SNCTRLR_SETUP_MASQUERADE"
	// snctrlrSetupSNAT defines a true/false bool variable that configures SNAT rules for a traffic
	// originating on pods and reaching smartnat instance. You want this to be true only if the traffic
	// that starts on pods should leave the smartnat instance with the same network interface and IP
	// address as the one configured for the traffic incoming to smartnat from clients for the same pods.
	// Making the traffic from pods to reach the smartnat is external problem and needs to be handled
	// outside of smartnat service.
	snctrlrSetupSNAT = "SNCTRLR_SETUP_SNAT"
	// snctrlrDefaultGwInterface this has to be configured to the name of the network interface that is
	// used as default gateway on the smartnat instance
	snctrlrDefaultGwInterface = "SNCTRLR_DEFAULT_GW_INTERFACE"
	// snctrlrIptablesTimeoutSec timeout in seconds for the `iptables` system command that is frequently
	// called by smartnat
	snctrlrIptablesTimeoutSec = "SNCTRLR_IPTABLES_TIMEOUT_SEC"
	// snctrlrDebugEnabled set to "true" to enable debug messages in log
	snctrlrDebugEnabled = "SNCTRLR_DEBUG_ENABLED"
	// snctrlrInterfaceAutorefreshPeriodSec how frequently to refresh interface and IP addresses
	// information from the operating system. This is the time between two runs in seconds.
	snctrlrInterfaceAutorefreshPeriodSec = "SNCTRLR_AUTOREFRESH_PERIOD_SEC"
	// snctrlrMaxFinalizeWaitMinutes sets the absolute finalizer timeout in minutes. The finalizer
	// timeout is a safety mechanism that needs to be triggered if a smartnat instance has configured
	// a Mapping and then the instance failed and never came back. In that case, when the Mapping is
	// removed, kubernetes doesn't really delete the Mapping until all smartnat nodes that had been
	// configured for it confirm that the configuration was removed. A failed instance will never do
	// that, so we use this timeout to forcibly remove the Mapping after SNCTRLR_AUTOREFRESH_PERIOD_SEC,
	// even if it is indicated as being configured on some smartnat nodes.
	snctrlrMaxFinalizeWaitMinutes = "SNCTRLR_MAX_FINALIZE_WAIT_MINUTES"
	// snctrlrGwAddressOffset configures the offset in IP address, where the default gateway can be found
	// for all the client network interfaces matching SNCTRLR_INTERFACES_PATTERN. A positive offset is
	// counted forward from the subnet address of the 1st IP found on the interface (so, if the IP is
	// "10.10.10.10/24" and offset is 1, the assumed gateway on this interface is "10.10.10.1").
	// A negative offset is counted backwards from the broadcast address (so, if the IP is "10.10.10.10/24"
	// and the offset is -1, the assumed gateway IP on this interface is "10.10.10.254").
	snctrlrGwAddressOffset = "SNCTRLR_GW_ADDRESS_OFFSET"
)

// NewConfig returns new configuration settings for smart-nat-controller
func NewConfig() (*Config, error) {
	cfg := &Config{}
	err := cfg.load()
	return cfg, err
}

// NewConfigFromArgs creates new config struct from arguments, after running validation
func NewConfigFromArgs(interfaceNamePattern string, setupMasquerade, setupSNAT bool, defaultGWInterface string,
	ipTablesTimeoutSec int, enableDebug bool, interfaceAutorefreshPeriodSec, maxFinalizeWaitMinutes int,
	gwAddressOffset int32) (*Config, error) {
	cfg := &Config{
		DefaultGWInterface:            defaultGWInterface,
		EnableDebug:                   enableDebug,
		InterfaceAutorefreshPeriodSec: interfaceAutorefreshPeriodSec,
		InterfaceNamePattern:          interfaceNamePattern,
		IPTablesTimeoutSec:            ipTablesTimeoutSec,
		SetupMasquerade:               setupMasquerade,
		SetupSNAT:                     setupSNAT,
		MaxFinalizeWaitMinutes:        maxFinalizeWaitMinutes,
		GwAddressOffset:               gwAddressOffset,
	}
	err := cfg.validate()
	return cfg, err
}

func (cfg *Config) load() error {
	// below are set all the default values
	cfg.DefaultGWInterface = os.Getenv(snctrlrDefaultGwInterface)
	cfg.EnableDebug, _ = eos.GetBoolFromEnvVarWithDefault(snctrlrDebugEnabled, false)
	cfg.InterfaceAutorefreshPeriodSec, _ = eos.GetIntFromEnvVarWithDefault(snctrlrInterfaceAutorefreshPeriodSec, 120)
	cfg.InterfaceNamePattern = os.Getenv(snctrlrInterfacesPattern)
	cfg.IPTablesTimeoutSec, _ = eos.GetIntFromEnvVarWithDefault(snctrlrIptablesTimeoutSec, 10)
	cfg.SetupMasquerade, _ = eos.GetBoolFromEnvVarWithDefault(snctrlrSetupMasquerade, true)
	cfg.SetupSNAT, _ = eos.GetBoolFromEnvVarWithDefault(snctrlrSetupSNAT, false)
	cfg.MaxFinalizeWaitMinutes, _ = eos.GetIntFromEnvVarWithDefault(snctrlrMaxFinalizeWaitMinutes, 60)
	offset, _ := eos.GetIntFromEnvVarWithDefault(snctrlrGwAddressOffset, 1)
	cfg.GwAddressOffset = int32(offset)
	return cfg.validate()
}

func (cfg *Config) validate() error {
	if cfg.InterfaceNamePattern == "" {
		return fmt.Errorf("Configuration env var %s can't be empty", snctrlrInterfacesPattern)
	}
	if _, err := regexp.Compile(cfg.InterfaceNamePattern); err != nil {
		return fmt.Errorf("Configuration env var %s is not a valid regexp", snctrlrInterfacesPattern)
	}
	if cfg.DefaultGWInterface == "" {
		return fmt.Errorf("Configuration env var %s can't be empty", snctrlrDefaultGwInterface)
	}
	if cfg.IPTablesTimeoutSec <= 0 {
		return fmt.Errorf("Configuration env var %s can't be <= 0", snctrlrIptablesTimeoutSec)
	}
	if cfg.InterfaceAutorefreshPeriodSec <= 0 {
		return fmt.Errorf("Configuration env var %s can't be <= 0", snctrlrInterfaceAutorefreshPeriodSec)
	}
	if cfg.MaxFinalizeWaitMinutes <= 0 {
		return fmt.Errorf("Configuration env var %s can't be <= 0", snctrlrMaxFinalizeWaitMinutes)
	}
	return nil
}
