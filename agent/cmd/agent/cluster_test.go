package main

import (
	"net/http"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
)

func reasons(ps []Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Reason)
	}
	return out
}

func node(conds ...corev1.NodeCondition) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: corev1.NodeStatus{Conditions: conds}}
}

func TestNodeProblems(t *testing.T) {
	now := time.Now()
	old := metav1.NewTime(now.Add(-5 * time.Minute))
	fresh := metav1.NewTime(now.Add(-10 * time.Second))

	ok := node(corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastTransitionTime: old})
	if got := nodeProblems(ok, now); len(got) != 0 {
		t.Fatalf("ready node: %v", reasons(got))
	}
	flap := node(corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionUnknown, LastTransitionTime: fresh})
	if got := nodeProblems(flap, now); len(got) != 0 {
		t.Fatalf("not ready for 10 s must wait: %v", reasons(got))
	}
	down := node(
		corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionUnknown, LastTransitionTime: old},
		corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue, Message: "disk full"},
		corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
	)
	got := nodeProblems(down, now)
	if len(got) != 2 || got[0].Reason != "NodeNotReady" || got[0].Severity != "critical" ||
		got[1].Reason != "DiskPressure" || got[1].Object != "node/n1" || got[1].Source != "node" {
		t.Fatalf("problems = %+v", got)
	}
}

func TestRolloutStuck(t *testing.T) {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"}}
	d.Status.Conditions = []appsv1.DeploymentCondition{{
		Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: "took too long",
	}}
	got := rolloutProblems(d)
	if len(got) != 1 || got[0].Reason != "RolloutStuck" || got[0].Object != "deployment/web" {
		t.Fatalf("problems = %+v", got)
	}
	d.Status.Conditions[0] = appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable"}
	if got := rolloutProblems(d); len(got) != 0 {
		t.Fatalf("healthy rollout: %v", reasons(got))
	}
}

func TestReplicasNotReadyOnlyOnceItLasts(t *testing.T) {
	r := newClusterRules(informers.NewSharedInformerFactory(fake.NewClientset(), 0))
	t0 := time.Now()
	if p := r.replicas("deployment", "default", "web", 3, 1, t0); p != nil {
		t.Fatal("must not fire at once (rolling update)")
	}
	if p := r.replicas("deployment", "default", "web", 3, 1, t0.Add(replicasNotReadyFor-time.Second)); p != nil {
		t.Fatal("fired too early")
	}
	p := r.replicas("deployment", "default", "web", 3, 1, t0.Add(replicasNotReadyFor))
	if p == nil || p.Severity != "warning" || p.Object != "deployment/web" || p.Message != "1 of 3 replicas ready for 10m0s" {
		t.Fatalf("problem = %+v", p)
	}
	if p := r.replicas("deployment", "default", "web", 3, 0, t0.Add(replicasNotReadyFor)); p == nil || p.Severity != "critical" {
		t.Fatalf("none ready must be critical: %+v", p)
	}
	if p := r.replicas("deployment", "default", "web", 3, 3, t0.Add(time.Hour)); p != nil {
		t.Fatal("all ready is fine")
	}
}

func TestShortWorkloadRaisedFromTheCache(t *testing.T) {
	cs := fake.NewClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
	})
	f := informers.NewSharedInformerFactory(cs, 0)
	r := newClusterRules(f)
	stop := make(chan struct{})
	defer close(stop)
	f.Start(stop)
	f.WaitForCacheSync(stop)

	t0 := time.Now()
	r.problems(t0) // starts the clock: 0 of 1 ready
	if got := r.problems(t0.Add(replicasNotReadyFor)); len(got) != 1 || got[0].Reason != "ReplicasNotReady" {
		t.Fatalf("problems = %+v", got)
	}
	r.mu.Lock()
	n := len(r.notReady)
	r.mu.Unlock()
	if n != 1 {
		t.Fatalf("tracked = %d", n)
	}
}

func job(name, cron string, created time.Time, cond batchv1.JobConditionType, at time.Time) *batchv1.Job {
	j := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: metav1.NewTime(created)}}
	if cron != "" {
		j.OwnerReferences = []metav1.OwnerReference{{Kind: "CronJob", Name: cron}}
	}
	if cond != "" {
		j.Status.Conditions = []batchv1.JobCondition{{Type: cond, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded",
			Message: "Job has reached the specified backoff limit", LastTransitionTime: metav1.NewTime(at)}}
	}
	return j
}

func TestJobProblems(t *testing.T) {
	now := time.Now()
	h := func(n int) time.Time { return now.Add(time.Duration(-n) * time.Hour) }
	got := jobProblems([]*batchv1.Job{
		job("migrate", "", h(2), batchv1.JobFailed, h(1)),        // failed an hour ago: open
		job("old-migrate", "", h(50), batchv1.JobFailed, h(48)),  // failed 2 days ago: forgotten
		job("ok", "", h(2), batchv1.JobComplete, h(1)),           // fine
		job("backup-1", "backup", h(3), batchv1.JobFailed, h(3)), // failed, but a later run succeeded
		job("backup-2", "backup", h(2), batchv1.JobComplete, h(2)),
		job("report-1", "report", h(3), batchv1.JobComplete, h(3)),
		job("report-2", "report", h(2), batchv1.JobFailed, h(2)), // last finished run failed
		job("report-3", "report", h(1), "", time.Time{}),         // running: does not count
	}, now)
	if len(got) != 2 {
		t.Fatalf("problems = %+v", got)
	}
	if got[0].Reason != "JobFailed" || got[0].Object != "job/migrate" {
		t.Fatalf("job = %+v", got[0])
	}
	if got[1].Reason != "CronJobFailed" || got[1].Object != "cronjob/report" ||
		got[1].Message != "last run report-2 failed: BackoffLimitExceeded: Job has reached the specified backoff limit" {
		t.Fatalf("cronjob = %+v", got[1])
	}
}

func TestPVCProblems(t *testing.T) {
	now := time.Now()
	wait := storagev1.VolumeBindingWaitForFirstConsumer
	immediate := storagev1.VolumeBindingImmediate
	classes := []*storagev1.StorageClass{
		{ObjectMeta: metav1.ObjectMeta{Name: "local-path", Annotations: map[string]string{defaultClassAnnotate: "true"}}, VolumeBindingMode: &wait},
		{ObjectMeta: metav1.ObjectMeta{Name: "ssd"}, VolumeBindingMode: &immediate},
	}
	pvc := func(class string, age time.Duration, phase corev1.PersistentVolumeClaimPhase, picked bool) *corev1.PersistentVolumeClaim {
		c := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default",
			CreationTimestamp: metav1.NewTime(now.Add(-age)), Annotations: map[string]string{}}}
		if class != "" {
			c.Spec.StorageClassName = &class
		}
		if picked {
			c.Annotations[selectedNodeAnnotate] = "n1"
		}
		c.Status.Phase = phase
		return c
	}
	cases := []struct {
		name string
		c    *corev1.PersistentVolumeClaim
		want string
	}{
		{"bound", pvc("ssd", time.Hour, corev1.ClaimBound, false), ""},
		{"pending, young", pvc("ssd", time.Minute, corev1.ClaimPending, false), ""},
		{"pending, immediate class", pvc("ssd", time.Hour, corev1.ClaimPending, false), "PVCPending"},
		{"pending, waits for a pod (default class)", pvc("", time.Hour, corev1.ClaimPending, false), ""},
		{"pending, node picked but no volume", pvc("local-path", time.Hour, corev1.ClaimPending, true), "PVCPending"},
		{"lost", pvc("ssd", time.Hour, corev1.ClaimLost, false), "PVCLost"},
	}
	for _, tc := range cases {
		got := pvcProblems(tc.c, classes, now)
		r := ""
		if len(got) == 1 {
			r = got[0].Reason
		}
		if len(got) > 1 || r != tc.want {
			t.Errorf("%s: problems = %v, want %q", tc.name, reasons(got), tc.want)
		}
	}
}

func TestClusterRulesResolveWhatCleared(t *testing.T) {
	tr := &tracker{cluster: "dev", relay: &relayClient{base: "http://127.0.0.1:1", http: &http.Client{Timeout: time.Second}}, open: map[string]Problem{}}
	cs := fake.NewClientset(node(corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue}))
	f := informers.NewSharedInformerFactory(cs, 0)
	r := newClusterRules(f)
	stop := make(chan struct{})
	defer close(stop)
	f.Start(stop)
	f.WaitForCacheSync(stop)

	tr.raise(Problem{Source: "pod", Namespace: "default", Object: "pod/web", Reason: "Failed", Severity: "critical"})
	r.evaluate(tr, time.Now())
	if _, ok := tr.open["node||node/n1|DiskPressure"]; !ok || len(tr.open) != 2 {
		t.Fatalf("open = %v", tr.open)
	}
	if err := cs.Tracker().Update(corev1.SchemeGroupVersion.WithResource("nodes"), node(), ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if n, _ := r.nodes.Get("n1"); n != nil && len(n.Status.Conditions) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("informer did not see the update")
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.evaluate(tr, time.Now())
	if len(tr.open) != 1 {
		t.Fatalf("the node problem must resolve, the pod problem stay: %v", tr.open)
	}
}
