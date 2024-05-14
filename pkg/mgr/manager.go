package manager

import (
	"context"
	stderror "errors"
	"fmt"
	"os"
	"reflect"
	"time"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"github.com/go-logr/logr"
	utils "github.com/mvazquezc/multicluster-certmanager-addon/pkg/utils"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/pointer"
	openclusterv1 "open-cluster-management.io/api/cluster/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

type ManagedClusterReconciler struct {
	client.Client
	Logger                   logr.Logger
	managedClusterReconciled []string
}

type CertificateReconciler struct {
	client.Client
	Logger      logr.Logger
	HubClient   client.Client
	ClusterName string
}

type CertificateRequestReconciler struct {
	client.Client
	Logger      logr.Logger
	HubClient   client.Client
	ClusterName string
}

const (
	certificateFinalizer = "certdeletion.finaler.multicluster-cert-manager-addon"
)

func NewHubManager() {
	logf.SetLogger(zap.New())

	log := logf.Log.WithName("hub-manager")
	ctrlCacheConfig := cache.Options{
		/* Listen on specific namespace, otherwise all namespaces
		DefaultNamespaces: map[string]cache.Config{
			"open-cluster-management": {},
		},
		*/
	}

	mgr, err := manager.New(config.GetConfigOrDie(), manager.Options{Cache: ctrlCacheConfig})

	if err != nil {
		log.Error(err, "could not create manager")
		os.Exit(1)
	}

	// Register types into the mgr
	schemeBuilder := &scheme.Builder{GroupVersion: schema.GroupVersion{Group: "cluster.open-cluster-management.io", Version: "v1"}}
	schemeBuilder.Register(&openclusterv1.ManagedCluster{}, &openclusterv1.ManagedClusterList{})

	if err := schemeBuilder.AddToScheme(mgr.GetScheme()); err != nil {
		log.Error(err, "Couldn't register types into mgr")
		os.Exit(1)
	}

	err = builder.
		ControllerManagedBy(mgr).             // Create the ControllerManagedBy
		For(&openclusterv1.ManagedCluster{}). // Watch ManagedClusters in the hub
		Complete(&ManagedClusterReconciler{
			Client: mgr.GetClient(),
			Logger: log,
		})
	if err != nil {
		log.Error(err, "could not create managedcluster controller")
		os.Exit(1)
	}

	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		log.Error(err, "could not start manager")
		os.Exit(1)
	}

}

func NewSpokeManager(ctx context.Context, kubeconfigData []byte, hubClient client.Client, clusterName string) error {
	logf.SetLogger(zap.New())

	log := logf.Log.WithName(clusterName + "-spoke-manager")
	kubeconfig, err := clientcmd.NewClientConfigFromBytes(kubeconfigData)
	if err != nil {
		log.Error(err, "could not create new client from kubeconfig")
		return err
	}

	restConfig, err := kubeconfig.ClientConfig()
	if err != nil {
		log.Error(err, "could not create new restconfig")
		return err
	}

	ctrlCacheConfig := cache.Options{
		/* Listen on specific namespace, otherwise all namespaces
		DefaultNamespaces: map[string]cache.Config{
			"open-cluster-management": {},
		},
		*/
	}

	//mgr, err := manager.New(restConfig, manager.Options{Cache: ctrlCacheConfig, Metrics: server.Options{BindAddress: ":8081"}})
	// no metrics server to avoid port colision
	mgr, err := manager.New(restConfig, manager.Options{Cache: ctrlCacheConfig, Metrics: server.Options{BindAddress: "0"}})
	if err != nil {
		log.Error(err, "could not create manager")
		return err
	}

	// Register cert-manager types into the mgr
	schemeBuilder := &scheme.Builder{GroupVersion: schema.GroupVersion{Group: "cert-manager.io", Version: "v1"}}
	schemeBuilder.Register(&certmanagerv1.Certificate{}, &certmanagerv1.CertificateList{})
	schemeBuilder.Register(&certmanagerv1.CertificateRequest{}, &certmanagerv1.CertificateRequestList{})

	if err := schemeBuilder.AddToScheme(mgr.GetScheme()); err != nil {
		log.Error(err, "Couldn't register types into mgr")
		return err
	}

	err = builder.
		ControllerManagedBy(mgr).                   // Create the ControllerManagedBy
		For(&certmanagerv1.Certificate{}).          // Watch for Certificates in the Spoke
		Owns(&corev1.Secret{}).                     // Watch for changes in secrets owned by the certificates
		WithEventFilter(ignoreDeletionPredicate()). // Do not react to events that do not generate a new object revision
		Complete(&CertificateReconciler{
			Client:      mgr.GetClient(),
			Logger:      log,
			HubClient:   hubClient,
			ClusterName: clusterName,
		})
	if err != nil {
		log.Error(err, "could not create certificates controller")
		return err
	}

	err = builder.
		ControllerManagedBy(mgr).                 // Create the ControllerManagedBy
		For(&certmanagerv1.CertificateRequest{}). // Watch for CertificateRequests in the Spoke
		Complete(&CertificateRequestReconciler{
			Client:      mgr.GetClient(),
			Logger:      log,
			HubClient:   hubClient,
			ClusterName: clusterName,
		})
	if err != nil {
		log.Error(err, "could not create certificates controller")
		return err
	}

	//if err := mgr.Start(context.TODO()); err != nil {
	if err := mgr.Start(ctx); err != nil {
		return err
	}

	return nil
}

func (a *ManagedClusterReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {

	a.Logger.Info("Reconcile managed cluster")
	mc := &openclusterv1.ManagedCluster{}
	err := a.Get(ctx, req.NamespacedName, mc)
	if err != nil {
		return reconcile.Result{}, err
	}
	// Check if managedcluster has the label
	if utils.KubeObjectHasLabel(mc.Labels, "multiclustercerts") {
		// Do we know about this cluster?
		if utils.Contains(a.managedClusterReconciled, mc.Name) {
			a.Logger.Info("Managed cluster already has a reconciler " + mc.Name)
			// Do not reconcile
			return reconcile.Result{}, nil
		} else {
			a.Logger.Info("Managed cluster added " + mc.Name)
			a.managedClusterReconciled = append(a.managedClusterReconciled, mc.Name)
			// Create manager and stuff for this managedcluster here and forget about it
			// Get kubeconfig from secret
			// Create Mgr, add reconciler to the manager
			kubeconfigSecret := &corev1.Secret{}
			err = a.Get(ctx, client.ObjectKey{Name: mc.Name + "-admin-kubeconfig", Namespace: mc.Name}, kubeconfigSecret)
			if err != nil {
				if errors.IsNotFound(err) {
					a.Logger.Info("admin kubeconfig secret not found for managedcluster " + mc.Name + " we cannot proceed")
					return reconcile.Result{}, err
				}
				return reconcile.Result{}, err
			}
			kubeconfigData, ok := kubeconfigSecret.Data["kubeconfig"]

			if !ok {
				err = stderror.New("could not get kubeconfig from secret, we cannot proceed")
				a.Logger.Error(err, "could not get kubeconfig from secret, we cannot proceed")
				return reconcile.Result{}, err
			}

			err := NewSpokeManager(ctx, kubeconfigData, a.Client, mc.Name)
			if err != nil {
				return reconcile.Result{}, err
			}
			// At this point we have a running reconciler for the spoke cluster
		}
	} else {
		// Was part of known clusters?
		if utils.Contains(a.managedClusterReconciled, mc.Name) {
			// Do not reconcile, remove manager, remove from list
			a.managedClusterReconciled = utils.RemoveStringFromSlice(a.managedClusterReconciled, mc.Name)
			a.Logger.Info("Managed cluster removed " + mc.Name)
			return reconcile.Result{}, nil
		}
	}

	return reconcile.Result{}, nil
}

func (a *CertificateReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	// Read renewal time from cert and plan a reconciliation before that time
	cert := &certmanagerv1.Certificate{}
	err := a.Get(ctx, req.NamespacedName, cert)

	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			a.Logger.Info("Certficate not found. Ignoring object may have been deleted in a previous reconciliation")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	//hubCertName := a.ClusterName + "-" + cert.Namespace + "-" + cert.Name
	hubCertName := cert.Namespace + "-" + cert.Name
	hubCertNamespace := a.ClusterName

	// Check if it's a deletion
	certMarkedToBeDeleted := cert.GetDeletionTimestamp() != nil

	// Check if Certificate already exists on hub
	hubCert := &certmanagerv1.Certificate{}
	err = a.HubClient.Get(ctx, client.ObjectKey{Name: hubCertName, Namespace: hubCertNamespace}, hubCert)
	if err != nil {
		if errors.IsNotFound(err) && !certMarkedToBeDeleted {
			a.Logger.Info("Certificate " + hubCertName + " doesn't exist on hub, creating it")
			cert.Name = hubCertName
			cert.Namespace = a.ClusterName
			cert.UID = ""
			cert.ResourceVersion = ""
			// Store original secretName in an annotation and change secretname to avoid collisions
			originalCertNameAnnotation := map[string]string{}
			originalCertNameAnnotation["multicluster-cert-manager-addon.original-secret-name"] = cert.Spec.SecretName
			cert.SetAnnotations(originalCertNameAnnotation)
			cert.Spec.SecretName = cert.Namespace + "-" + cert.Spec.SecretName
			err = a.HubClient.Create(ctx, cert)
			if err != nil {
				return reconcile.Result{}, err
			}
			return reconcile.Result{RequeueAfter: 10 * time.Second, Requeue: true}, nil
		}
		return reconcile.Result{}, err
	}

	if certMarkedToBeDeleted {
		a.Logger.Info("Certificate marked for deletion, removing from hub")
		// Remove Cert from hub, we keep the secret.
		err = a.HubClient.Delete(ctx, hubCert)
		if err != nil {
			return reconcile.Result{}, err
		}
		a.Logger.Info("Certificate deleted from the hub")
		// Clean all finalizers (since we don't have certmanager running on spoke we clean ours and any others)
		if utils.Contains(cert.GetFinalizers(), certificateFinalizer) {
			finalizers := []string{}
			cert.SetFinalizers(finalizers)
			err = a.Update(ctx, cert)
			if err != nil {
				return reconcile.Result{}, err
			}
		}
		// End reconciliation (cert will be deleted from spoke)
		return reconcile.Result{}, nil
	}

	// Certificate exists on hub already, compare spec to what we have
	// Find a way to not check cert.Spec.Secretname otherwise we will end up in a loop here
	// We "hack" the secretName by getting the one in the annotation (if the annotation exist, which it should)
	hubSecretName := hubCert.Spec.SecretName
	if len(hubCert.Annotations["multicluster-cert-manager-addon.original-secret-name"]) > 0 {
		hubCert.Spec.SecretName = hubCert.Annotations["multicluster-cert-manager-addon.original-secret-name"]
	}
	if !reflect.DeepEqual(cert.Spec, hubCert.Spec) {
		// We need to avoid this if only difference is secretName
		a.Logger.Info("Certificate spec mismatch between hub and spoke, syncing spoke to hub")
		hubCert.Spec = cert.Spec
		err = a.HubClient.Update(ctx, hubCert)
		if err != nil {
			return reconcile.Result{}, err
		}
		a.Logger.Info("Certificate spec synced")
		//return reconcile.Result{}, nil
		return reconcile.Result{RequeueAfter: 10 * time.Second, Requeue: true}, nil
	}
	// Certificate exists on hub already, compare its status with what we have
	if !reflect.DeepEqual(cert.Status, hubCert.Status) {
		a.Logger.Info("Certificate status mismatch between hub and spoke, syncing hub to spoke")
		// Check if the cert has been issued
		for _, condition := range hubCert.Status.Conditions {
			if condition.Type == certmanagerv1.CertificateConditionReady {
				a.Logger.Info("Certificate has been issued on the hub")
			} else {
				a.Logger.Info("Certificate yet to be issued on the hub")
				// TODO: Change to requeue with backoff
				return reconcile.Result{RequeueAfter: 10 * time.Second, Requeue: true}, nil
			}
		}
		// Try to reflect cert status
		cert.Status = hubCert.Status
		err = a.Status().Update(ctx, cert)
		if err != nil {
			return reconcile.Result{}, err
		}
		a.Logger.Info("Certificate status synced")
	}

	// Certificate should create a secret on the hub cluster when it got issued
	hubSecret := &corev1.Secret{}
	err = a.HubClient.Get(ctx, client.ObjectKey{Name: hubSecretName, Namespace: hubCert.Namespace}, hubSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			a.Logger.Info("Certificate " + hubSecretName + " doesn't exist on hub yet")
			// TODO: do backoff
			return reconcile.Result{RequeueAfter: 10 * time.Second, Requeue: true}, nil
		}
		return reconcile.Result{}, err
	}

	// Certificate is issued, copy secret over in case it doesn't exist
	spokeSecret := &corev1.Secret{}
	err = a.Get(ctx, client.ObjectKey{Name: cert.Spec.SecretName, Namespace: cert.Namespace}, spokeSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			a.Logger.Info("Certificate " + cert.Spec.SecretName + " doesn't exist on spoke yet. Grab it from hub and create it")
			// TODO: do backoff
			hubSecret.Name = cert.Spec.SecretName
			hubSecret.Namespace = cert.Namespace
			hubSecret.UID = ""
			hubSecret.ResourceVersion = ""
			// Add ownerreference
			ownerRef := metav1.OwnerReference{
				APIVersion: cert.GetObjectKind().GroupVersionKind().GroupVersion().String(),
				Kind:       cert.GetObjectKind().GroupVersionKind().Kind,
				Name:       cert.GetName(),
				UID:        cert.GetUID(),
				Controller: pointer.BoolPtr(true), // Indicates that the owner controls the lifecycle of the object
			}
			hubSecret.SetOwnerReferences(append(hubSecret.GetOwnerReferences(), ownerRef))
			err = a.Create(ctx, hubSecret)
			if err != nil {
				return reconcile.Result{}, err
			}
			a.Logger.Info("Certificate " + cert.Spec.SecretName + " has been created on the spoke")
			//return reconcile.Result{}, nil
			return reconcile.Result{RequeueAfter: 10 * time.Second, Requeue: true}, nil
		}
		return reconcile.Result{}, err
	}
	// Cert exist on spoke, compare
	if !reflect.DeepEqual(spokeSecret.Data, hubSecret.Data) {
		// We need to avoid this if only difference is secretName
		a.Logger.Info("Certificate data mismatch between hub and spoke, syncing from hub to spoke")
		spokeSecret.Data = hubSecret.Data
		err = a.Update(ctx, spokeSecret)
		if err != nil {
			return reconcile.Result{}, err
		}

		return reconcile.Result{RequeueAfter: 10 * time.Second, Requeue: true}, nil
	}
	a.Logger.Info("Certificate " + req.NamespacedName.String() + " properly synced with the hub")

	//Add our finalizer if it's not there
	if !utils.Contains(cert.GetFinalizers(), certificateFinalizer) {
		finalizers := append(cert.GetFinalizers(), certificateFinalizer)
		cert.SetFinalizers(finalizers)
		err = a.Update(ctx, cert)
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	// Get renewal time from status and plan a reconciliation at that time
	secondsToRenewal := utils.SecondsToRenewal(hubCert.Status.RenewalTime)
	if secondsToRenewal > 0 {
		a.Logger.Info("Certificate " + req.NamespacedName.String() + " will be renewed at " + hubCert.Status.RenewalTime.String() + " requeuing to reconcile in " + utils.SecondsToRenewal(hubCert.Status.RenewalTime).String() + "s")
		return reconcile.Result{RequeueAfter: utils.SecondsToRenewal(hubCert.Status.RenewalTime), Requeue: true}, nil
	}
	return reconcile.Result{RequeueAfter: 10 * time.Second, Requeue: true}, nil

}

func (a *CertificateRequestReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	fmt.Println("Reconciling certificaterequest - not implemented")
	certRequest := &certmanagerv1.CertificateRequest{}
	err := a.Get(ctx, req.NamespacedName, certRequest)
	if err != nil {
		return reconcile.Result{}, err
	}
	fmt.Println(certRequest)
	return reconcile.Result{}, nil
}

func ignoreDeletionPredicate() predicate.Predicate {
	return predicate.Funcs{

		UpdateFunc: func(e event.UpdateEvent) bool {
			// Ignore updates to CR status in which case metadata.Generation does not change
			return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration()
		},

		DeleteFunc: func(e event.DeleteEvent) bool {
			// Evaluates to false if the object has been confirmed deleted.
			return !e.DeleteStateUnknown
		},
	}
}
