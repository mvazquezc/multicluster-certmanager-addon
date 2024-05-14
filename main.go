package main

import (
	customManagers "github.com/mvazquezc/multicluster-certmanager-addon/pkg/mgr"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func main() {
	logf.SetLogger(zap.New())

	log := logf.Log.WithName("multicluster-certmanager-addon")
	log.Info("Starting multicluster certmanager addon")
	customManagers.NewHubManager()
}

// CertificateReconciler

// SpokeCertificateReconciler
