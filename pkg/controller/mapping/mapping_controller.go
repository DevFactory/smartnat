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
	"context"
	"fmt"
	"net"
	"time"

	log "github.com/sirupsen/logrus"

	estrings "github.com/DevFactory/go-tools/pkg/extensions/strings"
	"github.com/DevFactory/go-tools/pkg/linux/command"
	"github.com/DevFactory/go-tools/pkg/nettools"
	smartnatv1alpha1 "github.com/DevFactory/smartnat/pkg/apis/smartnat/v1alpha1"
	"github.com/DevFactory/smartnat/pkg/config"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	requeueTimeOnErrorSec   = 30
	requeueTimeOnSuccessSec = 600
	finalizerName           = "mapping.finalizers.aureacentral.com"
)

type ipsConfig struct {
	configuredLocalExternalIP *net.IP
	fromStatusLocalExternalIP *net.IP
}

// Add creates a new Mapping Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, heartbeatChan chan<- string) error {
	// here we are configuring all the dependencies required by the actual syncer object
	cfg, err := config.NewConfig()
	if err != nil {
		log.Fatalf("Fatal error when loading configuration: %v", err)
		panic("Configuration error")
	}
	refreshInterval := time.Duration(cfg.InterfaceAutorefreshPeriodSec) * time.Second

	executor := command.NewExecutor()
	interfaceProvider, err := nettools.NewNetInterfaceProvider(cfg.InterfaceNamePattern, false, refreshInterval)
	if err != nil {
		panic(fmt.Sprintf("Fatal error when creating interface provider: %v", err))
	}
	iptablesHelper := nettools.NewExecIPTablesHelper(executor, time.Duration(cfg.IPTablesTimeoutSec)*time.Second)
	namer := NewNamer()
	ipsetHelper := nettools.NewExecIPSetHelper(executor)
	dnatProvider := NewThroughServiceDNATProvider(iptablesHelper, ipsetHelper, namer)
	ipTablesHelper, err := NewIPTablesHelper(dnatProvider, iptablesHelper, namer, interfaceProvider,
		cfg.SetupMasquerade, cfg.SetupSNAT)
	if err != nil {
		panic(fmt.Sprintf("Fatal error when creating iptables helper: %v", err))
	}
	routeIoOp := nettools.NewIOSimpleFileOperator()
	ipRouteHelper := NewIPRouteSmartNatHelper(executor, routeIoOp, interfaceProvider, refreshInterval, cfg.GwAddressOffset)
	scrubber := NewScrubber(interfaceProvider, cfg)
	conntrackHelper := nettools.NewExecConntrackHelper(executor)
	syncer := NewSyncer(namer, interfaceProvider, ipRouteHelper, conntrackHelper, ipTablesHelper, ipsetHelper,
		cfg.SetupSNAT, cfg.SetupMasquerade)
	return add(mgr, newReconciler(mgr, cfg, interfaceProvider, ipRouteHelper, syncer, scrubber, heartbeatChan))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, cfg *config.Config, interfaceProvider nettools.InterfaceProvider,
	ipRouteHelper IPRouteSmartNatHelper, syncer Syncer, scrubber Scrubber,
	heartbeatChan chan<- string) reconcile.Reconciler {
	client := mgr.GetClient()

	return &ReconcileMapping{
		Client:        client,
		scheme:        mgr.GetScheme(),
		cfg:           cfg,
		ifaceProvider: interfaceProvider,
		routeHelper:   ipRouteHelper,
		syncer:        syncer,
		scrubber:      scrubber,
		heartbeatChan: heartbeatChan,
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("mapping-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to Mapping
	err = c.Watch(&source.Kind{Type: &smartnatv1alpha1.Mapping{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	client := mgr.GetClient()
	// Watch changes to Services and trigger Reconciles for Mappings
	err = c.Watch(
		&source.Kind{Type: &v1.Service{}},
		&handler.EnqueueRequestsFromMapFunc{
			ToRequests: handler.ToRequestsFunc(func(m handler.MapObject) []reconcile.Request {
				return mapFromMeta(client, m, "Service")
			}),
		})
	if err != nil {
		return err
	}

	// Watch changes to Endpoints and trigger Reconciles for Mappings
	err = c.Watch(
		&source.Kind{Type: &v1.Endpoints{}},
		&handler.EnqueueRequestsFromMapFunc{
			ToRequests: handler.ToRequestsFunc(func(m handler.MapObject) []reconcile.Request {
				return mapFromMeta(client, m, "Endpoints")
			}),
		})
	if err != nil {
		return err
	}

	return nil
}

func mapFromMeta(c client.Client, m handler.MapObject, source string) []reconcile.Request {
	name := m.Meta.GetName()
	namespace := m.Meta.GetNamespace()

	mappingList := &smartnatv1alpha1.MappingList{}
	listOps := &client.ListOptions{Namespace: namespace}
	if err := c.List(context.TODO(), listOps, mappingList); err != nil {
		log.Errorf("Error when trying to list all Mappings in namespace %v. Needed for check if should "+
			"reconcile. Error: %v", namespace, err)
		return []reconcile.Request{}
	}

	requests := []reconcile.Request{}
	for _, mapping := range mappingList.Items {
		if mapping.GetNamespace() == namespace && mapping.Spec.ServiceName == name {
			log.Debugf("Found a Mapping object for %s named: %s/%s", source, namespace, name)
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      mapping.ObjectMeta.Name,
					Namespace: namespace,
				},
			})
		}
	}

	return requests
}

var _ reconcile.Reconciler = &ReconcileMapping{}

// ReconcileMapping reconciles a Mapping object
type ReconcileMapping struct {
	client.Client
	scheme        *runtime.Scheme
	cfg           *config.Config
	ifaceProvider nettools.InterfaceProvider
	routeHelper   IPRouteSmartNatHelper
	syncer        Syncer
	scrubber      Scrubber
	heartbeatChan chan<- string
}

var (
	errResult         = reconcile.Result{RequeueAfter: time.Duration(requeueTimeOnErrorSec) * time.Second}
	okResult          = reconcile.Result{RequeueAfter: time.Duration(requeueTimeOnSuccessSec) * time.Second}
	okResultNoRequeue = reconcile.Result{Requeue: false}
)

// Reconcile reads that state of the cluster for a Mapping object and makes changes based on the state read
// and what is in the Mapping.Spec
// Automatically generate RBAC rules to allow the Controller to read and write Mappings, Services and Endpoints
// +kubebuilder:rbac:groups=apps,resources=services;endpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=smartnat.aureacentral.com,resources=mappings,verbs=get;list;watch;create;update;patch;delete
func (r *ReconcileMapping) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	// update heartbeat channel
	r.heartbeatChan <- fmt.Sprintf(GetHeartbeatString())

	// Fetch the Mapping instance
	instance, fetchResult, err := r.fetchMapping(request.NamespacedName)
	if err != nil || fetchResult != nil {
		return *fetchResult, err
	}

	mapping := instance.DeepCopy()
	var result *reconcile.Result
	// Here's the main synchronization logic. It's based strongly on two places showing what is the state of the
	// Mapping configuration: intended and actual:
	// * configuration intended by user is expressed in Mapping.Spec.Addresses; if any of these addresses is assigned
	//   to the local smart-nat node, we call it "local External IP"
	// * configuration really performed by smart-nat controller is stored in Mapping.Status.ConfiguredAddresses. If
	//   operating system configuration was started for any local IP address from Mapping.Spec.Addresses, it is added
	//   to the dictionary Mapping.Status.ConfiguredAddresses and called "status External IP". If the configuration
	//   was done, the key has a value, which is a list of pods' IP addresses that were configured for the related External IP.
	// Now, the logic is:
	// - if both localExternalIP == nil and statusExternalIP == nil - we have nothing to do
	// - if localExternalIP == nil and statusExternalIP != nil - localExternalIP was removed from Mapping.Spec and
	//   we have to remove the configuration from the operating system and the IP from Mapping.Status.ConfiguredAddresses
	// - if localExternalIP != nil - we need to run local configuration synchronization to make sure it matches the requested
	//   one.
	mapping, service, endpoints, result, err := r.runPreSyncChecks(mapping)
	if result != nil {
		return *result, err
	}

	// everything's ready and validated - let's run actual config sync
	syncDirty, err := r.syncer.SyncMapping(mapping, service, endpoints)
	if err != nil {
		logrusWithMapping(mapping).Errorf("Error syncing system configuration for Mapping: %v", err)
		return errResult, err
	}

	if syncDirty {
		logrusWithMapping(mapping).Debug("Updating Mapping object after syncing system configuration")
		var result *reconcile.Result
		mapping, result, err = r.updateAndGetMapping(mapping, true)
		if err != nil {
			logrusWithMapping(mapping).Warnf("Error during Update and Get mapping: %v", err)
			return errResult, err
		}
		if result != nil {
			logrusWithMapping(mapping).Debug("Temporary issue during Update and Get.")
			return *result, nil
		}
	}

	logrusWithMapping(mapping).Info("Mapping update and system state synchronization successful")
	return okResult, nil
}

func (r *ReconcileMapping) handleMissingEndpoints(mapping *smartnatv1alpha1.Mapping, targetName types.NamespacedName,
	ipsCfg *ipsConfig) (*reconcile.Result, error) {
	// there are 2 cases: either the Service/Endpoints were never created yet (so they were never configured on
	// the local node as well) or they were already configured here, but the Service/Endpoints were removed.
	// In that case we have fromStatusExternalIP and we have to run deleteHandler
	if ipsCfg.fromStatusLocalExternalIP != nil {
		logrusWithMapping(mapping).Debugf("Endpoints %s not found, but mapping was previously configured. "+
			"Deleting it.", targetName)
		dirtyDelete, err := r.syncer.DeleteMapping(mapping, *ipsCfg.fromStatusLocalExternalIP)
		if err != nil {
			logrusWithMapping(mapping).Errorf("Error when deleting configuration for IP %v: %v",
				ipsCfg.fromStatusLocalExternalIP, err)
			return &errResult, err
		}
		dirtyServiceIP := false
		if mapping.Status.ServiceVIP != "" {
			mapping.Status.ServiceVIP = ""
			dirtyServiceIP = true
		}
		if dirtyDelete || dirtyServiceIP {
			err := r.updateMapping(mapping, true)
			if err != nil {
				logrusWithMapping(mapping).Warnf("Error during Update of Mapping: %v", err)
				return &errResult, err
			}
		}
		return &okResult, nil
	}
	return &okResult, nil
}

func (r *ReconcileMapping) runValidation(mapping *smartnatv1alpha1.Mapping) (*smartnatv1alpha1.Mapping,
	*reconcile.Result, *net.IP, bool, bool, error) {
	mappingList := &smartnatv1alpha1.MappingList{}
	listOps := &client.ListOptions{Namespace: mapping.Namespace}
	if err := r.Client.List(context.TODO(), listOps, mappingList); err != nil {
		log.Errorf("Error when trying to list all Mappings in namespace %v. Error: %v", mapping.Namespace, err)
		return mapping, &errResult, nil, false, false, err
	}
	valid, dirtyDefaults, validErrMessage, localExternalIP := r.scrubber.ScrubMapping(mapping, mappingList.Items)
	validChanged := false
	if !valid && mapping.Status.Invalid != validErrMessage {
		mapping.Status.Invalid = validErrMessage
		validChanged = true
	}
	if valid && mapping.Status.Invalid != defaultInvalidOKStatus {
		mapping.Status.Invalid = defaultInvalidOKStatus
		validChanged = true
	}
	if !valid {
		logrusWithMapping(mapping).Warn("Validation failed. Won't perform any actions.")
		return mapping, &errResult, nil, dirtyDefaults, validChanged, fmt.Errorf(validErrMessage)
	}
	return mapping, nil, localExternalIP, dirtyDefaults, validChanged, nil
}

func (r *ReconcileMapping) runPreSyncChecks(mapping *smartnatv1alpha1.Mapping) (*smartnatv1alpha1.Mapping,
	*v1.Service, *v1.Endpoints, *reconcile.Result, error) {
	// validate
	mapping, result, localExternalIP, dirtyDefaults, validChanged, err := r.runValidation(mapping)
	if err != nil {
		// if there's an error and we won't proceed, but the validity error message in Status changed, we have to persist it now
		if validChanged {
			if err := r.updateMapping(mapping, true); err != nil {
				logrusWithMapping(mapping).Errorf("Error updating Mapping status after failed validation. Will retry."+
					" Message: %v", err)
				return mapping, nil, nil, &errResult, err
			}
		}
		return mapping, nil, nil, result, err
	}

	// check if ever a configuration attempt was started for a local External IP and an external IP is saved in Mapping.Status
	ipsCfg := &ipsConfig{
		configuredLocalExternalIP: localExternalIP,
		fromStatusLocalExternalIP: r.getLocalExternalIPFromStatus(mapping),
	}

	// Check if the finalizer should be set up. If it already was, check if we have to run it and remove configuration from
	// the operating system. Also, we have to remove it, if the Mapping is still valid, but the local External IP was
	// removed
	result, dirtyFinalizer, err := r.runDeleteChecks(mapping, ipsCfg)
	if result != nil {
		return mapping, nil, nil, result, err
	}

	// Check if the Mapping is already marked for deletion, we have already removed local configuration,
	// but are still waiting for other nodes to run finalizer. In that case - do nothing.
	if !mapping.ObjectMeta.DeletionTimestamp.IsZero() {
		logrusWithMapping(mapping).Info("Mapping is being deleted, but our node doesn't have a configured " +
			"External IP for it. Nothing to do, we're waiting for others to remove the Mapping")
		return mapping, nil, nil, &okResult, nil
	}

	// if the local node neither has nor had any External IP - nothing to do
	if ipsCfg.fromStatusLocalExternalIP == nil && ipsCfg.configuredLocalExternalIP == nil {
		logrusWithMapping(mapping).Info("Mapping doesn't have External IP here. Finishing.")
		return mapping, nil, nil, &okResult, nil
	}

	// because Endpoints is created with Service, this name is valid for both of them
	targetName := types.NamespacedName{
		Name:      mapping.Spec.ServiceName,
		Namespace: mapping.Namespace,
	}

	// Fetch the related Endpoints
	endpoints, fetchResult, err := r.fetchEndpoints(targetName, mapping)
	if err != nil || fetchResult != nil {
		result, err := r.handleMissingEndpoints(mapping, targetName, ipsCfg)
		return mapping, nil, nil, result, err
	}

	// we have to validate the Endpoints
	// in the case we have multi-container pods, some containers might be ready and some not
	// inside the same pod. This will result in the pod having multiple Endpoints.Subsets
	// we wait until everything's up and len(Endpoints.Subsets) == 1 (or 0, if not pods)
	if err = r.scrubber.ValidateEndpoints(mapping, endpoints); err != nil {
		logrusWithMapping(mapping).Infof(
			"The related Endpoints doesn't have the required number of Subsets: %s", err)
		return mapping, nil, nil, &errResult, err
	}

	// Fetch the related Service
	service, fetchResult, err := r.fetchService(targetName, mapping)
	if err != nil || fetchResult != nil {
		logrusWithMapping(mapping).Debugf("Error when fetching service %s for Mapping", targetName.String())
		return mapping, nil, nil, fetchResult, err
	}

	// Add local External IP of this node to mark that the setup was initialized.
	// This is very important: actual execution of the setup can fail and the Mapping
	// will never be updated. Still, if Spec.Address is removed or the whole Mapping, we
	// have to run system cleanup. That way we know for which IP to run the cleanup.
	dirtyStatus := false
	if _, found := mapping.Status.ConfiguredAddresses[localExternalIP.String()]; !found {
		mapping.Status.ConfiguredAddresses[localExternalIP.String()] = smartnatv1alpha1.MappingPodIPs{
			PodIPs: []string{},
		}
		dirtyStatus = true
	}

	// if needed save changes to the Mapping
	if dirtyDefaults || dirtyFinalizer || dirtyStatus {
		logrusWithMapping(mapping).Debug("Mapping was updated during init, updating object")
		var result *reconcile.Result
		mapping, result, err = r.updateAndGetMapping(mapping, !(dirtyDefaults || dirtyFinalizer))
		if err != nil {
			logrusWithMapping(mapping).Warnf("Error during Update and Get mapping: %v", err)
			return mapping, nil, nil, &errResult, err
		}
		if result != nil {
			logrusWithMapping(mapping).Debug("Temporary issue during Update and Get.")
			return mapping, nil, nil, result, nil
		}
	}

	return mapping, service, endpoints, nil, nil
}

func (r *ReconcileMapping) runDeleteChecks(mapping *smartnatv1alpha1.Mapping, ipsCfg *ipsConfig) (*reconcile.Result,
	bool, error) {
	// setup finalizer - a procedure we want to run before API server deletes the Mapping
	dirtyFinalizer, dirtyDelete, err := r.checkSetupFinalizer(mapping, ipsCfg.configuredLocalExternalIP,
		ipsCfg.fromStatusLocalExternalIP, r.cfg.MaxFinalizeWaitMinutes)
	if err != nil {
		return &errResult, dirtyFinalizer, err
	}

	// if the mapping had an ExternalIP configured, but it doesn't have it anymore,
	// run config removal for the External IP
	dirtyDeleteStatus := false
	if ipsCfg.fromStatusLocalExternalIP != nil && ipsCfg.configuredLocalExternalIP == nil {
		logrusWithMapping(mapping).Warnf("No local External IPs found, but it was configured previously."+
			" Deleting local configuration for IP: %v", ipsCfg.fromStatusLocalExternalIP)
		dirtyDeleteStatus, err = r.syncer.DeleteMapping(mapping, *ipsCfg.fromStatusLocalExternalIP)
		if err != nil {
			logrusWithMapping(mapping).Warnf("Delete mapping for removed external IP failed: %v", err)
			return &errResult, dirtyFinalizer, err
		}
	}

	if dirtyDelete || dirtyDeleteStatus {
		logrusWithMapping(mapping).Debug("Configuration for External IP was removed, updating Mapping")
		if err := r.updateMapping(mapping, !dirtyFinalizer); err != nil {
			logrusWithMapping(mapping).Errorf("Error updating Mapping after configuration deletion. Will retry."+
				" Message: %v", err)
			return &errResult, dirtyFinalizer, err
		}
		logrusWithMapping(mapping).Info("Finishing sync, removed External IP configuration and updated Mapping")
		return &okResultNoRequeue, dirtyFinalizer, nil
	}
	return nil, dirtyFinalizer, nil
}

func (r *ReconcileMapping) updateMapping(mapping *smartnatv1alpha1.Mapping, statusOnly bool) error {
	// TODO: status subresources are only alpha and disabed by default on 1.10. add "+" here when
	// ready to enable. Sync with types definition in mapping_types.go
	// if !statusOnly {
	logrusWithMapping(mapping).Debug("Updating mapping")
	if err := r.Update(context.Background(), mapping); err != nil {
		logrusWithMapping(mapping).Warnf("Error updating mapping: %v", err)
		return err
	}
	logrusWithMapping(mapping).Debug("Mapping update successful")
	// }

	// logrusWithMapping(mapping).Debug("Updating mapping status")
	// TODO: status subresources are only alpha and disabed by default on 1.10. add "+" here when
	// ready to enable. Sync with types definition in mapping_types.go
	// if err := r.Status().Update(context.Background(), mapping); err != nil {
	// 	logrusWithMapping(mapping).Warnf("Error updating mapping status: %v", err)
	// 	return err
	// }
	// logrusWithMapping(mapping).Debug("Mapping status update successful")

	return nil
}

func (r *ReconcileMapping) updateAndGetMapping(mapping *smartnatv1alpha1.Mapping, statusOnly bool) (*smartnatv1alpha1.Mapping,
	*reconcile.Result, error) {
	if err := r.updateMapping(mapping, statusOnly); err != nil {
		return mapping, &errResult, err
	}

	name := types.NamespacedName{
		Namespace: mapping.Namespace,
		Name:      mapping.Name,
	}

	var (
		result        *reconcile.Result
		err           error
		dirtyDefaults bool
	)

	logrusWithMapping(mapping).Debug("Getting mapping after update")
	mapping, result, err = r.fetchMapping(name)
	if err != nil {
		return mapping, result, err
	}
	mapping, result, _, dirtyDefaults, _, err = r.runValidation(mapping)
	if err != nil {
		return mapping, result, err
	}
	if dirtyDefaults {
		logrusWithMapping(mapping).Debug("Refreshed Mapping still has missing defaults. Will requeue.")
		return mapping, &okResult, nil
	}
	return mapping, result, nil
}

func (r *ReconcileMapping) getLocalExternalIPFromStatus(mapping *smartnatv1alpha1.Mapping) *net.IP {
	var fromStatusLocalIP *net.IP
	for addr := range mapping.Status.ConfiguredAddresses {
		checkedIP := net.ParseIP(addr)
		if r.ifaceProvider.IsLocalIP(checkedIP) {
			fromStatusLocalIP = &checkedIP
			break
		}
	}
	return fromStatusLocalIP
}

func (r *ReconcileMapping) fetchMapping(name types.NamespacedName) (*smartnatv1alpha1.Mapping,
	*reconcile.Result, error) {
	instance := &smartnatv1alpha1.Mapping{}
	err := r.Get(context.TODO(), name, instance)
	result, err := r.validateFetched(name, instance, err, "Mapping")
	return instance, result, err
}

func (r *ReconcileMapping) fetchService(name types.NamespacedName, instance *smartnatv1alpha1.Mapping) (
	*v1.Service, *reconcile.Result, error) {
	// Fetch the related Service
	service := &v1.Service{}
	err := r.Get(context.TODO(), name, service)
	result, err := r.validateFetched(name, instance, err, "Service")
	return service, result, err
}

func (r *ReconcileMapping) fetchEndpoints(name types.NamespacedName, instance *smartnatv1alpha1.Mapping) (
	*v1.Endpoints, *reconcile.Result, error) {
	// Fetch the related Endpoints
	endpoints := &v1.Endpoints{}
	err := r.Get(context.TODO(), name, endpoints)
	result, err := r.validateFetched(name, instance, err, "Endpoints")
	return endpoints, result, err
}

func (r *ReconcileMapping) validateFetched(name types.NamespacedName, instance *smartnatv1alpha1.Mapping,
	err error, objectName string) (*reconcile.Result, error) {
	if err == nil {
		return nil, nil
	}
	if errors.IsNotFound(err) {
		// Object doesn't exist yet. If it is a missing Endpoints or Service - Requeue.
		// If we got a request to update a Mapping that doesn't exist anymore - no requeue.
		if objectName == "Mapping" {
			logrusWithMapping(instance).Debugf("Mapping %s wasn't found anymore. Won't requeue.", name)
			return &reconcile.Result{Requeue: false}, nil
		}
		logrusWithMapping(instance).Debugf("Tried to fetch %s %v, but it wasn't found. "+
			"Will requeue in %d s", objectName, name, requeueTimeOnErrorSec)
		return &errResult, nil
	}
	// Error reading the object - requeue the request.
	logrusWithMapping(instance).
		Errorf("Error trying to fetch %s %v. Will requeue. Error: %v", objectName, name, err)
	return &errResult, err
}

func (r *ReconcileMapping) checkSetupFinalizer(mapping *smartnatv1alpha1.Mapping, localExternalIP,
	fromStatusExternalIP *net.IP, maxFinalizeWaitMinutes int) (dirtyFinalizer, dirtyDelete bool, err error) {
	// safety check: one of the nodes that had set up the External IP for the Mapping might be down
	// and never coming back. If the DeletionTimestamp is present for too long, we remove the finalizer
	// anyway to unblock delete on Mapping.
	if mapping.ObjectMeta.DeletionTimestamp != nil && time.Now().Sub(mapping.ObjectMeta.DeletionTimestamp.Time) >
		time.Duration(maxFinalizeWaitMinutes)*time.Minute {
		logrusWithMapping(mapping).Warnf("Some instances didn't run their finalizers in %d minutes. "+
			"Forcing removal of the finalizer from Mapping", maxFinalizeWaitMinutes)
		mapping.ObjectMeta.Finalizers = estrings.RemoveString(mapping.ObjectMeta.Finalizers, finalizerName)
		return true, true, nil
	}

	// check if this node is or was involved in configuring any External IP for the mapping
	if localExternalIP == nil && fromStatusExternalIP == nil {
		logrusWithMapping(mapping).Debug("Not running any finalizer actions, as no local External IP is involved")
		return false, false, nil
	}

	// based on https://book.kubebuilder.io/beyond_basics/using_finalizers.html
	// when the object is not marked to be deleted
	if mapping.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted - let's check if it's newly created and doesn't have
		// a finalizer yet. If it doesn't have our finalizer and has a configured
		// local External IP then let's add the finalizer and update the object.
		if localExternalIP != nil && !estrings.ContainsString(mapping.ObjectMeta.Finalizers, finalizerName) {
			logrusWithMapping(mapping).Debug("adding finalizer info to Mapping object")
			mapping.ObjectMeta.Finalizers = append(mapping.ObjectMeta.Finalizers, finalizerName)
			return true, false, nil
		}
		return false, false, nil
	}

	// The object is being deleted - let's check if it has finalizer and a local External IP
	// configured in the Status section of Mapping
	if fromStatusExternalIP != nil && estrings.ContainsString(mapping.ObjectMeta.Finalizers, finalizerName) {
		// our finalizer is present, so lets handle our external dependency
		dirty, err := r.syncer.DeleteMapping(mapping, *fromStatusExternalIP)
		if err != nil {
			// if fail to delete the external dependency here, return with error so that it can be retried
			logrusWithMapping(mapping).Warnf("Failed to cleanup mapping from the operating system: %c",
				err)
			return false, dirty, err
		}
		logrusWithMapping(mapping).Debugf("Configuration successfully cleaned up in finalizer for IP: %v",
			*fromStatusExternalIP)

		// if we're the last node to clean up the config - remove our finalizer from the list and update it.
		if len(mapping.Status.ConfiguredAddresses) == 0 {
			logrusWithMapping(mapping).Debug("No more nodes to perform finalize found, removing finalizer")
			mapping.ObjectMeta.Finalizers = estrings.RemoveString(mapping.ObjectMeta.Finalizers, finalizerName)
			return true, dirty, nil
		}
	}

	return false, false, nil
}

// GetHeartbeatString returns string printed by heartbeat http request
func GetHeartbeatString() string {
	return fmt.Sprintf("OK %v", time.Now().UTC().Format(time.RFC3339))
}
