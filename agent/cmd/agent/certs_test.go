package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

func TestCertProblemsThresholds(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	at := func(d time.Duration) certFound {
		return certFound{namespace: "shop", object: "ingress/web", what: "https://shop.example.com",
			notAfter: now.Add(d), warn: certWarn, critical: certCritical}
	}
	sev := func(d time.Duration) string {
		ps := certProblems([]certFound{at(d)}, now)
		if len(ps) == 0 {
			return ""
		}
		return ps[0].Severity
	}
	for _, c := range []struct {
		d    time.Duration
		want string
	}{{60 * day, ""}, {15 * day, ""}, {14 * day, "warning"}, {9 * day, "warning"}, {3 * day, "critical"}, {-2 * day, "critical"}} {
		if got := sev(c.d); got != c.want {
			t.Errorf("%v left: severity %q, want %q", c.d, got, c.want)
		}
	}
	p := certProblems([]certFound{at(9 * day)}, now)[0]
	if p.Source != "certificate" || p.Reason != "CertificateExpiring" ||
		p.Message != "https://shop.example.com: expires in 9 days (21 Nov 2026 12:00 UTC)" {
		t.Fatalf("problem = %+v", p)
	}
	nr := certProblems([]certFound{{object: "certificate/web", notReady: "Failed: ACME order failed"}}, now)
	if len(nr) != 1 || nr[0].Reason != "CertificateNotReady" || nr[0].Severity != "critical" {
		t.Fatalf("not ready = %+v", nr)
	}
}

func TestExpiresIn(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		5 * time.Hour:        "expires in 5 hours",
		30 * time.Minute:     "expires in 30 minutes",
		-49 * time.Hour:      "expired 2 days ago",
		10*24*time.Hour + 30: "expires in 10 days",
	} {
		if got := expiresIn(now.Add(d), now); !strings.HasPrefix(got, want+" (") {
			t.Errorf("%v: %q, want %q…", d, got, want)
		}
	}
}

func TestCertManagerCertificate(t *testing.T) {
	now := time.Now()
	it := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "web", "namespace": "shop"},
		"spec":     map[string]any{"secretName": "web-tls", "dnsNames": []any{"Shop.example.com", "www.example.com"}},
		"status": map[string]any{
			"notAfter": now.Add(5 * 24 * time.Hour).UTC().Format(time.RFC3339),
			"conditions": []any{map[string]any{
				"type": "Ready", "status": "False", "reason": "Failed", "message": "ACME order failed",
				"lastTransitionTime": now.Add(-time.Hour).UTC().Format(time.RFC3339),
			}},
		},
	}}
	managed := map[string]bool{}
	c := cmCertificate(it, now, managed)
	if c.object != "certificate/web" || c.notReady != "Failed: ACME order failed" || c.notAfter.IsZero() ||
		c.what != "certificate web (Shop.example.com)" {
		t.Fatalf("found = %+v", c)
	}
	if !managed["shop/web-tls"] || !managed["shop.example.com"] || !managed["www.example.com"] {
		t.Fatalf("managed = %v", managed)
	}
	// Not Ready for a minute only: a first issuance, not a problem yet.
	conds := it.Object["status"].(map[string]any)["conditions"].([]any)
	conds[0].(map[string]any)["lastTransitionTime"] = now.Add(-time.Minute).UTC().Format(time.RFC3339)
	if c := cmCertificate(it, now, map[string]bool{}); c.notReady != "" {
		t.Fatalf("fresh issuance reported: %q", c.notReady)
	}
}

func TestIngressHostsSkipManagedAndWildcards(t *testing.T) {
	ing := func(name, secret string, hosts ...string) networkingv1.Ingress {
		return networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"},
			Spec:       networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{SecretName: secret, Hosts: hosts}}},
		}
	}
	managed := map[string]bool{"shop/cm-tls": true, "api.example.com": true}
	got := ingressHosts([]networkingv1.Ingress{
		ing("a", "cm-tls", "a.example.com"),                        // its secret comes from cert-manager
		ing("b", "own-tls", "B.example.com", "*.example.com", " "), // one real host
		ing("c", "own-tls", "api.example.com"),                     // a cert-manager dnsName
	}, managed)
	if len(got) != 1 || got[0].object != "ingress/b" || got[0].addr != "b.example.com:443" ||
		got[0].sni != "b.example.com" || got[0].checkHost != "b.example.com" {
		t.Fatalf("targets = %+v", got)
	}
}

func TestParseProbes(t *testing.T) {
	got := parseProbes(" owl/owl-relay:443 , bad, /x:1, owl/nope ")
	if len(got) != 1 || got[0].namespace != "owl" || got[0].object != "service/owl-relay" ||
		got[0].addr != "owl-relay.owl.svc:443" {
		t.Fatalf("probes = %+v", got)
	}
}

func TestProbeReadsTheServedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler()) // its certificate covers example.com
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	w := &certWatcher{dial: dialCert}
	unknown := map[string]bool{}
	found := w.probe(context.Background(), []probeTarget{
		{namespace: "shop", object: "ingress/a", what: "https://example.com", addr: addr, sni: "example.com", checkHost: "example.com"},
		{namespace: "shop", object: "ingress/b", what: "https://other.test", addr: addr, sni: "other.test", checkHost: "other.test"},
		{namespace: "shop", object: "ingress/c", what: "https://down.test", addr: "127.0.0.1:1", sni: "down.test"},
	}, unknown)
	if len(found) != 1 || found[0].object != "ingress/a" || found[0].notAfter.IsZero() {
		t.Fatalf("found = %+v (the default certificate of another host must be ignored)", found)
	}
	if !unknown["shop|ingress/c"] || unknown["shop|ingress/b"] {
		t.Fatalf("unknown = %v: only the unreachable host is unknown", unknown)
	}
}

func TestProbeKeepsTheFirstToExpirePerObject(t *testing.T) {
	soon, later := time.Now().Add(24*time.Hour), time.Now().Add(90*24*time.Hour)
	w := &certWatcher{dial: func(_ context.Context, addr, _ string) (*x509.Certificate, error) {
		if addr == "a:443" {
			return &x509.Certificate{NotAfter: later}, nil
		}
		return &x509.Certificate{NotAfter: soon}, nil
	}}
	found := w.probe(context.Background(), []probeTarget{
		{object: "httproute/r", addr: "a:443"}, {object: "httproute/r", addr: "b:443"},
	}, map[string]bool{})
	if len(found) != 1 || !found[0].notAfter.Equal(soon) {
		t.Fatalf("found = %+v", found)
	}
}

func TestClientCertificateOfTheKubeconfig(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	notAfter := time.Now().Add(20 * 24 * time.Hour).Truncate(time.Second)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "system:admin"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	w := &certWatcher{cfg: &rest.Config{TLSClientConfig: rest.TLSClientConfig{CertData: pemData}}}
	c := w.clientCert()
	if c == nil || c.object != "cluster/kubeconfig" || !c.notAfter.Equal(notAfter) || c.warn != clientCertWarn {
		t.Fatalf("client cert = %+v", c)
	}
	// 20 days left: a warning for a kubeconfig (30 days notice), nothing for a website (14).
	if ps := certProblems([]certFound{*c}, time.Now()); len(ps) != 1 || ps[0].Severity != "warning" {
		t.Fatalf("problems = %+v", ps)
	}
	if (&certWatcher{cfg: &rest.Config{BearerToken: "x"}}).clientCert() != nil {
		t.Fatal("a token has no certificate")
	}
}

func TestEvaluateCertsKeepsWhatCouldNotBeRead(t *testing.T) {
	tr := &tracker{cluster: "dev", relay: &relayClient{base: "http://127.0.0.1:1", http: &http.Client{Timeout: time.Second}}, open: map[string]Problem{}}
	now := time.Now()
	exp := certFound{namespace: "shop", object: "ingress/web", what: "https://shop", notAfter: now.Add(time.Hour), warn: certWarn, critical: certCritical}
	cm := certFound{namespace: "shop", object: "certificate/api", what: "certificate api", notAfter: now.Add(time.Hour), warn: certWarn, critical: certCritical}
	evaluateCerts(tr, []certFound{exp, cm}, nil, now)
	if len(tr.open) != 2 {
		t.Fatalf("open = %v", tr.open)
	}
	// Next check: the host is unreachable and cert-manager's list failed. Nothing resolves.
	evaluateCerts(tr, nil, map[string]bool{"shop|ingress/web": true, "kind:certificate": true}, now)
	if len(tr.open) != 2 {
		t.Fatalf("unknown objects must stay open: %v", tr.open)
	}
	// Both renewed.
	evaluateCerts(tr, nil, map[string]bool{}, now)
	if len(tr.open) != 0 {
		t.Fatalf("renewed certificates must resolve: %v", tr.open)
	}
}

func TestErrKind(t *testing.T) {
	if errKind(errors.New(`certificates.cert-manager.io is forbidden: User "x"`)) != "forbidden" ||
		errKind(errors.New("the server could not find the requested resource")) != "missing" {
		t.Fatal("errKind")
	}
}

// Without cert-manager or the Gateway API, there is nothing to keep: a cert-manager problem
// left from before must resolve. When the list fails (forbidden, API down), it must stay.
func TestMissingCRDsAreNotUnknown(t *testing.T) {
	gvrs := map[schema.GroupVersionResource]string{certificatesGVR: "CertificateList", httpRoutesGVR: "HTTPRouteList"}
	newWatcher := func(err error) *certWatcher {
		dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrs)
		dyn.PrependReactor("list", "*", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, err })
		return &certWatcher{cs: fake.NewClientset(), dyn: dyn, dial: func(context.Context, string, string) (*x509.Certificate, error) {
			return nil, errors.New("unreachable")
		}}
	}
	_, unknown := newWatcher(errors.New("the server could not find the requested resource")).check(context.Background(), time.Now())
	if unknown["kind:certificate"] || unknown["kind:httproute"] {
		t.Fatalf("not installed must not be unknown: %v", unknown)
	}
	_, unknown = newWatcher(errors.New(`certificates.cert-manager.io is forbidden`)).check(context.Background(), time.Now())
	if !unknown["kind:certificate"] || !unknown["kind:httproute"] {
		t.Fatalf("a failed list must be unknown: %v", unknown)
	}
}
