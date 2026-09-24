/*
Copyright 2025.

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

// The binary keeps its historical repowatch-controller path/name so the
// StatefulSet and deploy scripts stay stable; since the RepoWatch EOL
// (docs/design/repoboard.md Phase 5) it runs the RepoBoard controller only.
package main

import (
	"flag"
	"os"

	"k8s.io/klog/v2"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	boardv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/repoboard/v1alpha1"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/controllers/repoboard"
	runctrl "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/controllers/run"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/factorycli"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(boardv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

// newRunnerWithProber wires the in-flight task preflight (adopt, don't
// duplicate — the watch dispatcher's recovery discipline) when a cluster
// client is available; without one the runner works as before.
// taskObserver lets the Run controller read a task's record off the
// sandbox disk. Without it a run launched by a previous process can
// only be resolved by timing out.
func taskObserver() factorycli.TaskObserver {
	prober, err := factorycli.NewPodTaskProber()
	if err != nil {
		klog.Warningf("run observation disabled (no cluster client): %v", err)
		return nil
	}
	return prober
}

func newRunnerWithProber() *factorycli.Runner {
	runner := factorycli.NewRunner()
	if prober, err := factorycli.NewPodTaskProber(); err == nil {
		runner.Prober = prober
	} else {
		klog.Warningf("task preflight disabled (no cluster client): %v", err)
	}
	return runner
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	var concurrentReconciles int
	flag.IntVar(&concurrentReconciles, "concurrent-reconciles", 1, "The number of concurrent reconciles.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		// -metrics-bind-address was parsed and then dropped, so the
		// manager always took its own :8080 default — the same port the
		// API serves on, which makes the two dev loops mutually
		// exclusive for a reason no flag could fix.
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "1a2b3c4d.x-k8s.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&repoboard.Reconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Factory: newRunnerWithProber(),
	}).SetupWithManager(mgr, concurrentReconciles); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "RepoBoard")
		os.Exit(1)
	}
	// Platform v2: one reconciler for every recipe. It shares the
	// runner with the v1 controller so both see the same in-flight
	// work, and it only touches repos whose spec.platform is v2.
	if err = (&runctrl.Reconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Factory:  newRunnerWithProber(),
		Observer: taskObserver(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Run")
		os.Exit(1)
	}

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
