/*


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
	"fmt"
	"github.com/go-logr/logr"
	"github.com/hetznercloud/hcloud-go/hcloud"
	hetznerv1 "github.com/kstiehl/hetzner-operator/api/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"strconv"
)

const serverFinalizer = "finalizer.hetzner.server.cleanup"

// ServerReconciler reconciles a Server object
type ServerReconciler struct {
	client.Client
	Log          logr.Logger
	Scheme       *runtime.Scheme
	Recorder     record.EventRecorder
	HetznerToken string
}

// +kubebuilder:rbac:groups=hetzner.kstiehl,resources=servers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=hetzner.kstiehl,resources=servers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;update;patch

func (r *ServerReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	_ = r.Log.WithValues("server", req.NamespacedName)
	server := &hetznerv1.Server{}
	err := r.Get(ctx, req.NamespacedName, server)
	if err != nil {
		return ctrl.Result{}, err
	}

	// server was deleted run finalizers
	if server.GetDeletionTimestamp() != nil {
		return ctrl.Result{}, r.handleDeletion(ctx, server)
	}

	// make sure finalizers are present
	if !contains(server.GetFinalizers(), serverFinalizer) {
		controllerutil.AddFinalizer(server, serverFinalizer)
		return ctrl.Result{}, r.Update(ctx, server)
	}

	// we havent deployed a server yet
	if server.Status.ServerID == "" {
		return ctrl.Result{}, r.handleCreation(server, ctx)
	} else if id, err := strconv.Atoi(server.Status.ServerID); err != nil {
		// make sure everything looks as it should on hetzner side
		return ctrl.Result{}, r.ensureServer(ctx, id)
	} else {
		// clean up state that we cant handle
		r.Log.Info("can't parse server id", "server", server.Name, "namespace", server.Namespace)
		server.Status.ServerID = ""
		return ctrl.Result{}, r.Update(ctx, server)
	}
}

func (r *ServerReconciler) ensureServer(ctx context.Context, id int) error {
	hetznerClient := r.createHetznerClient()
	_, _, err := hetznerClient.Server.GetByID(ctx, id)
	if err != nil {
		return err
	}
	r.Log.Info("ensure server is not yet implemented")
	return nil
}

func (r *ServerReconciler) handleCreation(server *hetznerv1.Server, ctx context.Context) error {
	r.Recorder.Event(server, "Normal", "Creation", "Going to create server")

	hetznerClient := r.createHetznerClient()
	image, _, err := hetznerClient.Image.GetByName(ctx, server.Spec.Image)
	if err != nil {
		return err
	}
	serverType, _, err := hetznerClient.ServerType.GetByName(ctx, server.Spec.Type)
	if err != nil {
		return err
	}

	serverName := fmt.Sprintf("%s-%s", server.Namespace, server.Name)

	newServer, _, err := hetznerClient.Server.Create(ctx,
		hcloud.ServerCreateOpts{
			Image:      image,
			Name:       serverName,
			ServerType: serverType})

	if err != nil {
		return err
	}

	server.Status.ServerID = strconv.Itoa(newServer.Server.ID)

	return r.Status().Update(ctx, server)
}

func (r *ServerReconciler) createHetznerClient() *hcloud.Client {
	return hcloud.NewClient(hcloud.WithToken(r.HetznerToken))
}

func (r *ServerReconciler) handleDeletion(ctx context.Context, server *hetznerv1.Server) error {
	if !contains(server.GetFinalizers(), serverFinalizer) {
		return nil
	}
	r.Recorder.Event(server, "Normal", "Deletion", "Server will be removed")

	if server.Status.ServerID == "" {
		controllerutil.RemoveFinalizer(server, serverFinalizer)
		return r.Update(ctx, server)
	}

	id, err := strconv.Atoi(server.Status.ServerID)
	if err != nil {
		controllerutil.RemoveFinalizer(server, serverFinalizer)
		return r.Update(ctx, server)
	}

	hetznerClient := r.createHetznerClient()
	hetznerServer, _, err := hetznerClient.Server.GetByID(ctx, id)
	if err != nil {
		return err
	}

	// server does not exist
	if hetznerServer == nil {
		controllerutil.RemoveFinalizer(server, serverFinalizer)
		return r.Update(ctx, server)
	}

	_, err = hetznerClient.Server.Delete(ctx, hetznerServer)
	if err != nil {
		return err
	}
	controllerutil.RemoveFinalizer(server, serverFinalizer)
	return r.Update(ctx, server)
}

func contains(slice []string, val string) bool {
	for _, v := range slice {
		if v == val {
			return true
		}
	}
	return false
}

func (r *ServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hetznerv1.Server{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
