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

package main

import (
	"fmt"
	"net/http"
	"os"
	"runtime"

	"github.com/DevFactory/smartnat/pkg/apis"
	mgrconfig "github.com/DevFactory/smartnat/pkg/config"
	"github.com/DevFactory/smartnat/pkg/controller"
	"github.com/DevFactory/smartnat/pkg/controller/mapping"
	"github.com/DevFactory/smartnat/pkg/webhook"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"

	log "github.com/sirupsen/logrus"
)

var (
	version   string = "v2.0.0-dev"
	commit    string = "none"
	date      string = "unknown"
	heartbeat string = mapping.GetHeartbeatString()
)

func printVersion() {
	log.Infof("Go Version: %s", runtime.Version())
	log.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	log.Infof("smart-nat-manager version: %s, commit: %s, build date: %s", version, commit, date)
}

func main() {
	log.SetOutput(os.Stdout)
	if version == "" {
		version = "2.0.0-devel"
	}
	printVersion()

	// load manager's config
	mgrCfg, err := mgrconfig.NewConfig()
	if err != nil {
		log.Fatalf("Fatal error when loading configuration: %v", err)
		panic("Configuration error")
	}
	if mgrCfg.EnableDebug {
		log.SetLevel(log.DebugLevel)
	}

	// Get a config to talk to the apiserver
	log.Info("setting up client for manager")
	cfg, err := config.GetConfig()
	if err != nil {
		log.Error(err, " - unable to set up client config")
		os.Exit(1)
	}

	// Create a new Cmd to provide shared dependencies and start components
	log.Info("setting up manager")
	mgr, err := manager.New(cfg, manager.Options{})
	if err != nil {
		log.Error(err, " - unable to set up overall controller manager")
		os.Exit(1)
	}

	log.Info("Registering Components.")

	// Setup Scheme for all resources
	log.Info("setting up scheme")
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		log.Error(err, "unable add APIs to scheme")
		os.Exit(1)
	}

	// Setup all Controllers
	log.Info("Setting up controllers")
	heartbeatChan := make(chan string)
	if err := controller.AddToManager(mgr, heartbeatChan); err != nil {
		log.Error(err, "unable to register controllers to the manager")
		os.Exit(1)
	}

	log.Info("setting up webhooks")
	if err := webhook.AddToManager(mgr); err != nil {
		log.Error(err, "unable to register webhooks to the manager")
		os.Exit(1)
	}

	// healthchecks
	log.Info("setting up health check")
	go func() {
		for {
			heartbeat = <-heartbeatChan
		}
	}()
	http.HandleFunc("/healthcheck", handleHealthcheck)
	go func() {
		http.ListenAndServe(fmt.Sprintf(":%d", mgrCfg.MonPort), nil)
	}()

	// Start the Cmd
	// TODO: run cleanup on shutdown
	log.Info("Starting the Cmd.")
	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		log.Error(err, "unable to run the manager")
		os.Exit(1)
	}
}

func handleHealthcheck(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(heartbeat))
}
