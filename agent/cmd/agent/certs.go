package main

// Certificate expiry. Every 10 minutes the agent looks at the certificates that keep this
// cluster and its apps reachable, and raises CertificateExpiring before they run out:
//
//   - cert-manager Certificates: expiry and Ready from their status (also CertificateNotReady
//     when cert-manager cannot issue or renew one)
//   - HTTPS hosts of Ingresses and HTTPRoutes not managed by cert-manager: the certificate the
//     host serves, read like a browser does (a TLS handshake, no Secret is read)
//   - the API server's certificate, and the client certificate of the agent's kubeconfig
//     (when it expires, Owl loses the cluster: warned 30 days ahead)
//   - each node's kubelet (port 10250)
//   - explicit targets from AGENT_TLS_PROBES, like Owl's own HTTPS port (set by the chart)
//
// A host the agent cannot reach is skipped, never reported: its last answer is kept.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
)

const (
	certEvery        = 10 * time.Minute
	certWarn         = 14 * 24 * time.Hour
	certCritical     = 3 * 24 * time.Hour
	clientCertWarn   = 30 * 24 * time.Hour // renewing a kubeconfig takes a person: more notice
	clientCertCrit   = 7 * 24 * time.Hour
	certNotReadyFor  = 10 * time.Minute // a first issuance takes a minute or two
	certProbeTimeout = 5 * time.Second
	certProbeMax     = 200 // hosts per check
	certProbeWorkers = 8
)

var (
	certificatesGVR = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	httpRoutesGVR   = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
)

// certFound is one certificate the agent looked at.
type certFound struct {
	namespace, object string // what the problem is about: certificate/x, ingress/x, node/x…
	what              string // where it was found, for the message: "https://shop.example.com"
	notAfter          time.Time
	warn, critical    time.Duration
	notReady          string // cert-manager: why it is not Ready (empty = Ready or unknown)
}

// certProblems turns what was found into problems: expiring soon, expired, or not Ready.
func certProblems(found []certFound, now time.Time) []Problem {
	var out []Problem
	for _, c := range found {
		base := Problem{Source: "certificate", Namespace: c.namespace, Object: c.object}
		if c.notReady != "" {
			p := base
			p.Severity, p.Reason, p.Message = "critical", "CertificateNotReady", c.notReady
			out = append(out, p)
		}
		if c.notAfter.IsZero() {
			continue
		}
		left := c.notAfter.Sub(now)
		if left > c.warn {
			continue
		}
		p := base
		p.Severity, p.Reason = "warning", "CertificateExpiring"
		if left <= c.critical {
			p.Severity = "critical"
		}
		p.Message = c.what + ": " + expiresIn(c.notAfter, now)
		out = append(out, p)
	}
	return out
}

// expiresIn: "expires in 9 days (21 Nov 2026)", "expires in 5 hours (…)", "expired 2 days ago (…)".
func expiresIn(t, now time.Time) string {
	d := t.Sub(now)
	date := t.UTC().Format("2 Jan 2006 15:04 UTC")
	span := func(d time.Duration) string {
		if d >= 48*time.Hour {
			return fmt.Sprintf("%d days", int(d.Hours()/24))
		}
		if d >= 2*time.Hour {
			return fmt.Sprintf("%d hours", int(d.Hours()))
		}
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	if d < 0 {
		return "expired " + span(-d) + " ago (" + date + ")"
	}
	return "expires in " + span(d) + " (" + date + ")"
}

// tlsDial reads the certificate a server presents. sni empty = no SNI (an IP address).
type tlsDial func(ctx context.Context, addr, sni string) (*x509.Certificate, error)

func dialCert(ctx context.Context, addr, sni string) (*x509.Certificate, error) {
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: certProbeTimeout},
		// Only the expiry is read, never trusted: self-signed and private CAs must work too.
		Config: &tls.Config{ServerName: sni, InsecureSkipVerify: true}, // #nosec G402
	}
	ctx, cancel := context.WithTimeout(ctx, certProbeTimeout)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, fmt.Errorf("%s sent no certificate", addr)
	}
	return certs[0], nil
}

// probeTarget is one TLS endpoint to read: an Ingress or HTTPRoute host, a kubelet, a Service.
type probeTarget struct {
	namespace, object, what string
	addr, sni               string
	checkHost               string // the certificate must cover this name, else it is not this host's
}

type certWatcher struct {
	cs     kubernetes.Interface
	dyn    dynamic.Interface
	cfg    *rest.Config
	nodes  listerscorev1.NodeLister
	extra  []probeTarget // AGENT_TLS_PROBES
	dial   tlsDial
	logged sync.Map // what was already logged once (forbidden, missing CRD)
}

func newCertWatcher(cs kubernetes.Interface, dyn dynamic.Interface, cfg *rest.Config, nodes listerscorev1.NodeLister) *certWatcher {
	return &certWatcher{cs: cs, dyn: dyn, cfg: cfg, nodes: nodes, extra: parseProbes(os.Getenv("AGENT_TLS_PROBES")), dial: dialCert}
}

// parseProbes reads "namespace/service:port,…" (the chart sets Owl's own HTTPS port).
func parseProbes(v string) []probeTarget {
	var out []probeTarget
	for _, f := range strings.Split(v, ",") {
		f = strings.TrimSpace(f)
		ns, rest, ok := strings.Cut(f, "/")
		if !ok || ns == "" {
			continue
		}
		svc, port, err := net.SplitHostPort(rest)
		if err != nil || svc == "" {
			continue
		}
		out = append(out, probeTarget{
			namespace: ns, object: "service/" + svc, what: "https://" + svc + ":" + port,
			addr: net.JoinHostPort(svc+"."+ns+".svc", port),
		})
	}
	return out
}

func (w *certWatcher) once(key, format string, args ...any) {
	if _, seen := w.logged.LoadOrStore(key, true); !seen {
		log.Printf(format, args...)
	}
}

// check looks at every certificate. unknown: objects that could not be read this time
// (unreachable host, API error); their open problems stay as they are.
func (w *certWatcher) check(ctx context.Context, now time.Time) (found []certFound, unknown map[string]bool) {
	unknown = map[string]bool{}
	managed := map[string]bool{} // hosts and ns/secrets cert-manager already reports
	cm, ok := w.certManager(ctx, now, managed)
	found = append(found, cm...)
	if !ok {
		unknown["kind:certificate"] = true
	}

	var targets []probeTarget
	ing, ok := w.ingressTargets(ctx, managed)
	if !ok {
		unknown["kind:ingress"] = true
	}
	routes, ok := w.routeTargets(ctx, managed)
	if !ok {
		unknown["kind:httproute"] = true
	}
	targets = append(targets, ing...)
	targets = append(targets, routes...)
	targets = append(targets, w.kubeletTargets()...)
	targets = append(targets, w.extra...)
	if len(targets) > certProbeMax {
		w.once("probe-max", "certificates: %d TLS hosts, checking the first %d", len(targets), certProbeMax)
		targets = targets[:certProbeMax]
	}
	found = append(found, w.probe(ctx, targets, unknown)...)

	if c := w.apiServer(ctx); c != nil {
		found = append(found, *c)
	} else {
		unknown["|cluster/apiserver"] = true
	}
	if c := w.clientCert(); c != nil {
		found = append(found, *c)
	}
	return found, unknown
}

// certManager reads cert-manager Certificates. No cert-manager: nothing, quietly.
// ok is false when the list failed for another reason: keep what was reported.
func (w *certWatcher) certManager(ctx context.Context, now time.Time, managed map[string]bool) (_ []certFound, ok bool) {
	if w.dyn == nil {
		return nil, true
	}
	list, err := w.dyn.Resource(certificatesGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if errKind(err) == "missing" {
			w.once("cm-missing", "certificates: cert-manager is not installed, no Certificates to read")
			return nil, true // nothing to keep
		}
		w.once("cm-"+errKind(err), "certificates: cert-manager Certificates not read (%v)", err)
		return nil, false
	}
	var out []certFound
	for _, it := range list.Items {
		out = append(out, cmCertificate(it, now, managed))
	}
	return out, true
}

func cmCertificate(it unstructured.Unstructured, now time.Time, managed map[string]bool) certFound {
	c := certFound{
		namespace: it.GetNamespace(), object: "certificate/" + it.GetName(),
		what: "certificate " + it.GetName(), warn: certWarn, critical: certCritical,
	}
	if s, _, _ := unstructured.NestedString(it.Object, "spec", "secretName"); s != "" {
		managed[it.GetNamespace()+"/"+s] = true
	}
	names, _, _ := unstructured.NestedStringSlice(it.Object, "spec", "dnsNames")
	for _, n := range names {
		managed[strings.ToLower(n)] = true
	}
	if len(names) > 0 {
		c.what = "certificate " + it.GetName() + " (" + names[0] + ")"
	}
	if s, _, _ := unstructured.NestedString(it.Object, "status", "notAfter"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			c.notAfter = t
		}
	}
	conds, _, _ := unstructured.NestedSlice(it.Object, "status", "conditions")
	for _, raw := range conds {
		m, ok := raw.(map[string]any)
		if !ok || m["type"] != "Ready" || m["status"] != "False" {
			continue
		}
		since, _ := time.Parse(time.RFC3339, fmt.Sprint(m["lastTransitionTime"]))
		if since.IsZero() || now.Sub(since) >= certNotReadyFor {
			msg := strings.TrimSpace(fmt.Sprint(m["reason"]) + ": " + fmt.Sprint(m["message"]))
			c.notReady = strings.TrimPrefix(msg, ": ")
		}
	}
	return c
}

func (w *certWatcher) ingressTargets(ctx context.Context, managed map[string]bool) (_ []probeTarget, ok bool) {
	list, err := w.cs.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		w.once("ing-"+errKind(err), "certificates: Ingresses not read (%v)", err)
		return nil, false
	}
	return ingressHosts(list.Items, managed), true
}

func ingressHosts(ings []networkingv1.Ingress, managed map[string]bool) []probeTarget {
	var out []probeTarget
	for _, ing := range ings {
		for _, t := range ing.Spec.TLS {
			if t.SecretName != "" && managed[ing.Namespace+"/"+t.SecretName] {
				continue // cert-manager's Certificate says it better (and earlier)
			}
			for _, h := range t.Hosts {
				if tgt, ok := hostTarget(ing.Namespace, "ingress/"+ing.Name, h, managed); ok {
					out = append(out, tgt)
				}
			}
		}
	}
	return out
}

func (w *certWatcher) routeTargets(ctx context.Context, managed map[string]bool) (_ []probeTarget, ok bool) {
	if w.dyn == nil {
		return nil, true
	}
	list, err := w.dyn.Resource(httpRoutesGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if errKind(err) == "missing" {
			return nil, true // no Gateway API: nothing to read, nothing to keep
		}
		w.once("route-"+errKind(err), "certificates: HTTPRoutes not read (%v)", err)
		return nil, false
	}
	var out []probeTarget
	for _, it := range list.Items {
		hosts, _, _ := unstructured.NestedStringSlice(it.Object, "spec", "hostnames")
		for _, h := range hosts {
			if tgt, ok := hostTarget(it.GetNamespace(), "httproute/"+it.GetName(), h, managed); ok {
				out = append(out, tgt)
			}
		}
	}
	return out, true
}

// hostTarget: an HTTPS host worth reading. Wildcards have no single address to call.
func hostTarget(ns, object, host string, managed map[string]bool) (probeTarget, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || strings.Contains(host, "*") || managed[host] {
		return probeTarget{}, false
	}
	return probeTarget{namespace: ns, object: object, what: "https://" + host,
		addr: net.JoinHostPort(host, "443"), sni: host, checkHost: host}, true
}

func (w *certWatcher) kubeletTargets() []probeTarget {
	if w.nodes == nil {
		return nil
	}
	nodes, err := w.nodes.List(labels.Everything())
	if err != nil {
		return nil
	}
	var out []probeTarget
	for _, n := range nodes {
		ip := nodeIP(n)
		port := int(n.Status.DaemonEndpoints.KubeletEndpoint.Port)
		if ip == "" || port == 0 {
			continue
		}
		out = append(out, probeTarget{object: "node/" + n.Name, what: "kubelet on " + n.Name,
			addr: net.JoinHostPort(ip, strconv.Itoa(port))})
	}
	return out
}

func nodeIP(n *corev1.Node) string {
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	return ""
}

// probe reads the targets in parallel. One problem per object: the host that expires first.
func (w *certWatcher) probe(ctx context.Context, targets []probeTarget, unknown map[string]bool) []certFound {
	type result struct {
		t    probeTarget
		cert *x509.Certificate
	}
	var (
		mu      sync.Mutex
		results []result
		wg      sync.WaitGroup
		sem     = make(chan struct{}, certProbeWorkers)
	)
	for _, t := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(t probeTarget) {
			defer wg.Done()
			defer func() { <-sem }()
			cert, err := w.dial(ctx, t.addr, t.sni)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				unknown[t.namespace+"|"+t.object] = true // unreachable from here: say nothing
				return
			}
			if t.checkHost != "" && cert.VerifyHostname(t.checkHost) != nil {
				return // a default certificate, not this host's: its expiry says nothing about it
			}
			results = append(results, result{t, cert})
		}(t)
	}
	wg.Wait()

	first := map[string]result{}
	for _, r := range results {
		k := r.t.namespace + "|" + r.t.object
		if cur, ok := first[k]; !ok || r.cert.NotAfter.Before(cur.cert.NotAfter) {
			first[k] = r
		}
	}
	keys := make([]string, 0, len(first))
	for k := range first {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []certFound
	for _, k := range keys {
		r := first[k]
		out = append(out, certFound{namespace: r.t.namespace, object: r.t.object, what: r.t.what,
			notAfter: r.cert.NotAfter, warn: certWarn, critical: certCritical})
	}
	return out
}

// apiServer reads the certificate of the API server the agent talks to.
func (w *certWatcher) apiServer(ctx context.Context) *certFound {
	if w.cfg == nil {
		return nil
	}
	u, err := url.Parse(w.cfg.Host)
	if err != nil || u.Scheme != "https" {
		return nil
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), "443")
	}
	sni := w.cfg.TLSClientConfig.ServerName
	if sni == "" && net.ParseIP(u.Hostname()) == nil {
		sni = u.Hostname()
	}
	cert, err := w.dial(ctx, addr, sni)
	if err != nil {
		return nil
	}
	return &certFound{object: "cluster/apiserver", what: "API server " + u.Host,
		notAfter: cert.NotAfter, warn: certWarn, critical: certCritical}
}

// clientCert: the certificate the agent signs in with (kubeconfig), if it uses one.
func (w *certWatcher) clientCert() *certFound {
	if w.cfg == nil {
		return nil
	}
	data := w.cfg.TLSClientConfig.CertData
	if len(data) == 0 && w.cfg.TLSClientConfig.CertFile != "" {
		data, _ = os.ReadFile(w.cfg.TLSClientConfig.CertFile)
	}
	cert := firstCert(data)
	if cert == nil {
		return nil // a token, or in-cluster: nothing expires here
	}
	return &certFound{object: "cluster/kubeconfig", what: "the client certificate Owl's agent signs in with (" + cert.Subject.CommonName + ")",
		notAfter: cert.NotAfter, warn: clientCertWarn, critical: clientCertCrit}
}

func firstCert(data []byte) *x509.Certificate {
	for len(data) > 0 {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return nil
		}
		if b.Type == "CERTIFICATE" {
			if c, err := x509.ParseCertificate(b.Bytes); err == nil {
				return c
			}
		}
	}
	return nil
}

// evaluate raises what was found and resolves what cleared, except objects not read this time.
func evaluateCerts(tr *tracker, found []certFound, unknown map[string]bool, now time.Time) {
	current := map[string]bool{}
	for _, p := range certProblems(found, now) {
		current[p.key()] = true
		tr.raise(p)
	}
	tr.resolveWhere(func(p Problem) bool {
		kind, _, _ := strings.Cut(p.Object, "/")
		return p.Source == "certificate" && !current[p.key()] && !unknown[p.Namespace+"|"+p.Object] && !unknown["kind:"+kind]
	})
}

// run checks now, then every certEvery.
func (w *certWatcher) run(ctx context.Context, tr *tracker) {
	for first := true; ; first = false {
		found, unknown := w.check(ctx, time.Now())
		evaluateCerts(tr, found, unknown, time.Now())
		if first {
			hosts := 0
			for k := range unknown {
				if !strings.HasPrefix(k, "kind:") {
					hosts++
				}
			}
			log.Print(certSummary(found, hosts))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(certEvery):
		}
	}
}

// certSummary: one line after the first check, to see what is watched.
func certSummary(found []certFound, unreachable int) string {
	var next *certFound
	for i := range found {
		if !found[i].notAfter.IsZero() && (next == nil || found[i].notAfter.Before(next.notAfter)) {
			next = &found[i]
		}
	}
	msg := fmt.Sprintf("certificates: %d watched, %d not reachable from here", len(found), unreachable)
	if next != nil {
		msg += "; first to expire: " + next.what + ", " + expiresIn(next.notAfter, time.Now())
	}
	return msg + "; checked every " + certEvery.String()
}

func errKind(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "forbidden"):
		return "forbidden"
	case strings.Contains(s, "could not find") || strings.Contains(s, "not found"):
		return "missing"
	}
	return "other"
}
