package main

// Cluster rules: problems that are not about one pod. Nodes that are not Ready or under
// pressure, workloads that stay without their ready replicas, stuck rollouts, failed Jobs
// and CronJobs, volumes that cannot be provisioned. Checked from the informer caches at
// every tick (15 s): no extra API calls.

import (
	"fmt"
	"sort"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	listersappsv1 "k8s.io/client-go/listers/apps/v1"
	listersbatchv1 "k8s.io/client-go/listers/batch/v1"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	listersstoragev1 "k8s.io/client-go/listers/storage/v1"
)

const (
	nodeNotReadyAfter    = time.Minute      // a node flapping for a few seconds is not a problem
	replicasNotReadyFor  = 10 * time.Minute // longer than a normal rolling update
	pvcPendingAfter      = 5 * time.Minute
	jobFailureKeptFor    = 24 * time.Hour // a failed one-off Job stops being a problem after a day
	selectedNodeAnnotate = "volume.kubernetes.io/selected-node"
	defaultClassAnnotate = "storageclass.kubernetes.io/is-default-class"
)

// clusterSources are the problem sources these rules own: the ones they resolve.
var clusterSources = map[string]bool{"node": true, "workload": true, "storage": true}

type clusterRules struct {
	nodes        listerscorev1.NodeLister
	deployments  listersappsv1.DeploymentLister
	statefulSets listersappsv1.StatefulSetLister
	daemonSets   listersappsv1.DaemonSetLister
	jobs         listersbatchv1.JobLister
	cronJobs     listersbatchv1.CronJobLister
	pvcs         listerscorev1.PersistentVolumeClaimLister
	classes      listersstoragev1.StorageClassLister

	mu       sync.Mutex
	notReady map[string]time.Time // workload -> since when it is short of ready replicas
}

// newClusterRules registers the informers it reads; call it before factory.Start.
func newClusterRules(f informers.SharedInformerFactory) *clusterRules {
	return &clusterRules{
		nodes:        f.Core().V1().Nodes().Lister(),
		deployments:  f.Apps().V1().Deployments().Lister(),
		statefulSets: f.Apps().V1().StatefulSets().Lister(),
		daemonSets:   f.Apps().V1().DaemonSets().Lister(),
		jobs:         f.Batch().V1().Jobs().Lister(),
		cronJobs:     f.Batch().V1().CronJobs().Lister(),
		pvcs:         f.Core().V1().PersistentVolumeClaims().Lister(),
		classes:      f.Storage().V1().StorageClasses().Lister(),
		notReady:     map[string]time.Time{},
	}
}

// evaluate raises the current problems and resolves the ones that cleared.
func (r *clusterRules) evaluate(tr *tracker, now time.Time) {
	current := map[string]bool{}
	for _, p := range r.problems(now) {
		current[p.key()] = true
		tr.raise(p)
	}
	tr.resolveWhere(func(p Problem) bool { return clusterSources[p.Source] && !current[p.key()] })
}

func (r *clusterRules) problems(now time.Time) []Problem {
	var out []Problem
	if nodes, err := r.nodes.List(labels.Everything()); err == nil {
		for _, n := range nodes {
			out = append(out, nodeProblems(n, now)...)
		}
	}

	// Workloads short of ready replicas: only once it lasts (a rolling update is normal).
	short := map[string]bool{}
	wl := func(kind, ns, name string, want, ready int32) {
		if p := r.replicas(kind, ns, name, want, ready, now); p != nil {
			out = append(out, *p)
		}
		if want > 0 && ready < want {
			short[kind+"/"+ns+"/"+name] = true
		}
	}
	if ds, err := r.deployments.List(labels.Everything()); err == nil {
		for _, d := range ds {
			out = append(out, rolloutProblems(d)...)
			if !d.Spec.Paused {
				wl("deployment", d.Namespace, d.Name, replicasOf(d.Spec.Replicas), d.Status.ReadyReplicas)
			}
		}
	}
	if ss, err := r.statefulSets.List(labels.Everything()); err == nil {
		for _, s := range ss {
			wl("statefulset", s.Namespace, s.Name, replicasOf(s.Spec.Replicas), s.Status.ReadyReplicas)
		}
	}
	if ds, err := r.daemonSets.List(labels.Everything()); err == nil {
		for _, d := range ds {
			wl("daemonset", d.Namespace, d.Name, d.Status.DesiredNumberScheduled, d.Status.NumberReady)
		}
	}
	r.mu.Lock()
	for k := range r.notReady {
		if !short[k] {
			delete(r.notReady, k) // back to full, or deleted: the clock starts over next time
		}
	}
	r.mu.Unlock()

	if jobs, err := r.jobs.List(labels.Everything()); err == nil {
		out = append(out, jobProblems(jobs, now)...)
	}
	if pvcs, err := r.pvcs.List(labels.Everything()); err == nil {
		classes, _ := r.classes.List(labels.Everything())
		for _, c := range pvcs {
			out = append(out, pvcProblems(c, classes, now)...)
		}
	}
	return out
}

// replicas: a workload that has been short of ready replicas for replicasNotReadyFor.
func (r *clusterRules) replicas(kind, ns, name string, want, ready int32, now time.Time) *Problem {
	if want <= 0 || ready >= want {
		return nil
	}
	k := kind + "/" + ns + "/" + name
	r.mu.Lock()
	since, ok := r.notReady[k]
	if !ok {
		since = now
		r.notReady[k] = now
	}
	r.mu.Unlock()
	if now.Sub(since) < replicasNotReadyFor {
		return nil
	}
	sev := "warning"
	if ready == 0 {
		sev = "critical" // nothing serves
	}
	return &Problem{
		Source: "workload", Severity: sev, Namespace: ns, Object: kind + "/" + name, Reason: "ReplicasNotReady",
		Message: fmt.Sprintf("%d of %d replicas ready for %s", ready, want, now.Sub(since).Truncate(time.Minute)),
	}
}

func replicasOf(p *int32) int32 {
	if p == nil {
		return 1 // the API default
	}
	return *p
}

// nodeProblems: not Ready for a minute, or a pressure condition the kubelet reports.
func nodeProblems(n *corev1.Node, now time.Time) []Problem {
	var out []Problem
	add := func(sev, reason, msg string) {
		out = append(out, Problem{Source: "node", Severity: sev, Object: "node/" + n.Name, Reason: reason, Message: msg})
	}
	for _, c := range n.Status.Conditions {
		switch c.Type {
		case corev1.NodeReady:
			if c.Status != corev1.ConditionTrue && now.Sub(c.LastTransitionTime.Time) >= nodeNotReadyAfter {
				msg := "the kubelet stopped reporting"
				if c.Status == corev1.ConditionFalse {
					msg = c.Message
				}
				add("critical", "NodeNotReady", msg)
			}
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure, corev1.NodeNetworkUnavailable:
			if c.Status == corev1.ConditionTrue {
				add("warning", string(c.Type), c.Message)
			}
		}
	}
	return out
}

// rolloutProblems: the Deployment gave up on its rollout (progressDeadlineSeconds passed).
func rolloutProblems(d *appsv1.Deployment) []Problem {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse && c.Reason == "ProgressDeadlineExceeded" {
			return []Problem{{
				Source: "workload", Severity: "critical", Namespace: d.Namespace, Object: "deployment/" + d.Name,
				Reason: "RolloutStuck", Message: c.Message,
			}}
		}
	}
	return nil
}

// jobProblems: a one-off Job that failed in the last day, and a CronJob whose last run failed
// (it clears when a later run succeeds, even while the failed Job is still kept).
func jobProblems(jobs []*batchv1.Job, now time.Time) []Problem {
	var out []Problem
	last := map[string]*batchv1.Job{} // namespace/cronjob -> its most recent finished run
	for _, j := range jobs {
		failed, at, why := jobFailed(j)
		if cron := cronJobOf(j); cron != "" {
			if !failed && !jobComplete(j) {
				continue // still running
			}
			k := j.Namespace + "/" + cron
			if prev := last[k]; prev == nil || j.CreationTimestamp.After(prev.CreationTimestamp.Time) {
				last[k] = j
			}
			continue
		}
		if failed && now.Sub(at) < jobFailureKeptFor {
			out = append(out, Problem{
				Source: "workload", Severity: "critical", Namespace: j.Namespace, Object: "job/" + j.Name,
				Reason: "JobFailed", Message: why,
			})
		}
	}
	keys := make([]string, 0, len(last))
	for k := range last {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		j := last[k]
		if failed, _, why := jobFailed(j); failed {
			out = append(out, Problem{
				Source: "workload", Severity: "critical", Namespace: j.Namespace, Object: "cronjob/" + cronJobOf(j),
				Reason: "CronJobFailed", Message: fmt.Sprintf("last run %s failed: %s", j.Name, why),
			})
		}
	}
	return out
}

func jobFailed(j *batchv1.Job) (bool, time.Time, string) {
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			why := c.Reason
			if c.Message != "" {
				why += ": " + c.Message
			}
			return true, c.LastTransitionTime.Time, why
		}
	}
	return false, time.Time{}, ""
}

func jobComplete(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func cronJobOf(j *batchv1.Job) string {
	for _, o := range j.OwnerReferences {
		if o.Kind == "CronJob" {
			return o.Name
		}
	}
	return ""
}

// pvcProblems: a claim lost its volume, or stays Pending although it should be provisioned.
// With WaitForFirstConsumer (k3s local-path), Pending is normal until a pod uses the claim:
// only once the scheduler picked a node (the selected-node annotation) is it a problem.
func pvcProblems(c *corev1.PersistentVolumeClaim, classes []*storagev1.StorageClass, now time.Time) []Problem {
	obj := "persistentvolumeclaim/" + c.Name
	switch c.Status.Phase {
	case corev1.ClaimLost:
		return []Problem{{Source: "storage", Severity: "critical", Namespace: c.Namespace, Object: obj,
			Reason: "PVCLost", Message: "its volume " + c.Spec.VolumeName + " no longer exists"}}
	case corev1.ClaimPending:
		if now.Sub(c.CreationTimestamp.Time) < pvcPendingAfter {
			return nil
		}
		_, picked := c.Annotations[selectedNodeAnnotate]
		if !picked && waitsForConsumer(c, classes) {
			return nil
		}
		return []Problem{{Source: "storage", Severity: "warning", Namespace: c.Namespace, Object: obj,
			Reason: "PVCPending", Message: fmt.Sprintf("not bound after %s: check its StorageClass and events", now.Sub(c.CreationTimestamp.Time).Truncate(time.Minute))}}
	}
	return nil
}

func waitsForConsumer(c *corev1.PersistentVolumeClaim, classes []*storagev1.StorageClass) bool {
	var class *storagev1.StorageClass
	for _, sc := range classes {
		if c.Spec.StorageClassName != nil && sc.Name == *c.Spec.StorageClassName ||
			c.Spec.StorageClassName == nil && sc.Annotations[defaultClassAnnotate] == "true" {
			class = sc
			break
		}
	}
	return class != nil && class.VolumeBindingMode != nil && *class.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer
}
