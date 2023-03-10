/*
Copyright 2020-2023 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	registryv1alpha1 "github.com/devfile/registry-operator/api/v1alpha1"
	"github.com/devfile/registry-operator/pkg/cluster"
	"github.com/devfile/registry-operator/pkg/config"
	"github.com/devfile/registry-operator/pkg/registry"
	"github.com/devfile/registry-operator/pkg/util"
)

// DevfileRegistryReconciler reconciles a DevfileRegistry object
type DevfileRegistryReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=registry.devfile.io,resources=devfileregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=registry.devfile.io,resources=devfileregistries/status;devfileregistries/finalizers,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps;services;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes;routes/custom-host,verbs=get;list;watch;create;update;patch;delete

func (r *DevfileRegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("devfileregistry", req.NamespacedName)

	// Fetch the DevfileRegistry instance
	devfileRegistry := &registryv1alpha1.DevfileRegistry{}
	err := r.Get(ctx, req.NamespacedName, devfileRegistry)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("DevfileRegistry resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get DevfileRegistry")
		return ctrl.Result{}, err
	}

	// Generate labels for any subresources generated by the operator
	labels := registry.LabelsForDevfileRegistry(devfileRegistry.Name)

	log.Info("Deploying registry")

	// Check if the service already exists, if not create a new one
	result, err := r.ensure(ctx, devfileRegistry, &corev1.Service{}, labels, "")
	if result != nil {
		return *result, err
	}

	// If storage is enabled, create a persistent volume claim
	if registry.IsStorageEnabled(devfileRegistry) {
		// Check if the persistentvolumeclaim already exists, if not create a new one
		result, err = r.ensure(ctx, devfileRegistry, &corev1.PersistentVolumeClaim{}, labels, "")
		if result != nil {
			return *result, err
		}
	}

	result, err = r.ensure(ctx, devfileRegistry, &corev1.ConfigMap{}, labels, "")
	if result != nil {
		return *result, err
	}

	result, err = r.ensure(ctx, devfileRegistry, &appsv1.Deployment{}, labels, "")
	if result != nil {
		return *result, err
	}

	// Check to see if there's an old PVC that needs to be deleted
	// Has to happen AFTER the deployment has been updated.
	err = r.deleteOldPVCIfNeeded(ctx, devfileRegistry)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Create/update the ingress/route for the devfile registry
	hostname := devfileRegistry.Spec.K8s.IngressDomain
	if config.ControllerCfg.IsOpenShift() && hostname == "" {
		// Check if the route exposing the devfile index exists
		result, err = r.ensure(ctx, devfileRegistry, &routev1.Route{}, labels, "")
		if result != nil {
			return *result, err
		}

		// Get the hostname of the generated devfile route
		devfilesRoute := &routev1.Route{}
		err = r.Get(ctx, types.NamespacedName{Name: registry.IngressName(devfileRegistry.Name), Namespace: devfileRegistry.Namespace}, devfilesRoute)
		if err != nil {
			// Log an error, but requeue, as the controller's cached kube client likely hasn't registered the new route yet.
			// See https://github.com/operator-framework/operator-sdk/issues/4013#issuecomment-707267616 for an explanation on why we requeue rather than error out here
			log.Error(err, "Failed to get Route")
			return ctrl.Result{Requeue: true}, nil
		}
		hostname = devfilesRoute.Spec.Host
	} else {
		// Create/update the ingress for the devfile registry
		hostname = registry.GetDevfileRegistryIngress(devfileRegistry)
		result, err = r.ensure(ctx, devfileRegistry, &networkingv1.Ingress{}, labels, hostname)
		if result != nil {
			return *result, err
		}
	}

	var devfileRegistryServer string

	if registry.IsTLSEnabled(devfileRegistry) {
		devfileRegistryServer = "https://" + hostname
	} else {
		devfileRegistryServer = "http://" + hostname
	}

	if devfileRegistry.Status.URL != devfileRegistryServer {
		// Check to see if the registry is active, and if so, update the status to reflect the URL
		// when deploying a new devfile registry, it may not have a signed cert installed yet, so we will skip TLS checking.  We just want to make sure
		// server is up and running
		err = util.WaitForServer(devfileRegistryServer, 30*time.Second, false)
		if err != nil {
			log.Error(err, "Devfile registry server failed to start after 30 seconds, re-queueing...")
			return ctrl.Result{Requeue: true}, err
		}

		// Update the status
		devfileRegistry.Status.URL = devfileRegistryServer
		err := r.Status().Update(ctx, devfileRegistry)
		if err != nil {
			log.Error(err, "Failed to update DevfileRegistry status")
			return ctrl.Result{Requeue: true}, err
		}

		//update the config map
		result, err = r.ensure(ctx, devfileRegistry, &corev1.ConfigMap{}, labels, "")
		if result != nil {
			return *result, err
		}

	}

	return ctrl.Result{}, nil
}

func (r *DevfileRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Check if we're running on OpenShift
	isOS, err := cluster.IsOpenShift()
	if err != nil {
		return err
	}
	config.ControllerCfg.SetIsOpenShift(isOS)

	builder := ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.DevfileRegistry{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&networkingv1.Ingress{}).
		Owns(&corev1.ConfigMap{})

	// If on OpenShift, mark routes as owned by the controller
	if config.ControllerCfg.IsOpenShift() {
		builder.Owns(&routev1.Route{})
	}

	return builder.Complete(r)

}
