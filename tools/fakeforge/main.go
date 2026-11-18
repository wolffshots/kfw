// fakeforge serves a minimal Forgejo API over HTTPS on 127.0.0.1:8443 inside
// an emulated device, so that a package manager or an updater can test its
// downloads without the internet. kfw vm forge builds it for ARM and starts
// it. It was written for the kpm upgrade tests. Layout under -root:
//
//	registry.toml                 served at /<owner>/<repo>/raw/branch/<ref>/registry.toml
//	repos/<repo>/<tag>/<asset>    one or more tags; "latest" is the greatest name
//
// API: /api/v1/version, /api/v1/repos/o/<repo>/releases/latest,
// /api/v1/repos/o/<repo>/releases/tags/<tag> and the downloads under /dl/.
// The owner is always "o". It writes a new self-signed certificate for
// localhost to <root>/ca.pem at each start.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	root := flag.String("root", "/state/forge", "data folder")
	flag.Parse()

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(*root, "ca.pem"), certPEM, 0o644); err != nil {
		log.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.URL.Path == "/":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/v1/version":
			w.Write([]byte(`{"version":"9.0.0"}`))
		case len(p) >= 6 && p[0] == "api" && p[3] == "o" && p[5] == "releases":
			release(w, *root, p[4], p[6:])
		case len(p) == 6 && p[2] == "raw" && p[5] == "registry.toml":
			http.ServeFile(w, r, filepath.Join(*root, "registry.toml"))
		case len(p) == 4 && p[0] == "dl":
			http.ServeFile(w, r, filepath.Join(*root, "repos", p[1], p[2], p[3]))
		default:
			http.NotFound(w, r)
		}
	})
	srv := &http.Server{Addr: "127.0.0.1:8443", TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
	log.Fatal(srv.ListenAndServeTLS("", ""))
}

// release answers releases/latest and releases/tags/<tag> for a repo.
func release(w http.ResponseWriter, root, repo string, rest []string) {
	dir := filepath.Join(root, "repos", repo)
	tags, _ := os.ReadDir(dir)
	var names []string
	for _, t := range tags {
		names = append(names, t.Name())
	}
	sort.Strings(names)
	tag := ""
	switch {
	case len(rest) == 1 && rest[0] == "latest" && len(names) > 0:
		tag = names[len(names)-1]
	case len(rest) == 2 && rest[0] == "tags":
		tag = rest[1]
	}
	files, err := os.ReadDir(filepath.Join(dir, tag))
	if tag == "" || err != nil {
		http.NotFound(w, nil)
		return
	}
	type asset struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		URL  string `json:"browser_download_url"`
	}
	out := struct {
		Tag       string  `json:"tag_name"`
		Published string  `json:"published_at"`
		Assets    []asset `json:"assets"`
	}{Tag: tag, Published: "2026-11-18T00:00:00Z"}
	for _, f := range files {
		info, _ := f.Info()
		out.Assets = append(out.Assets, asset{f.Name(), info.Size(), "https://localhost:8443/dl/" + repo + "/" + tag + "/" + f.Name()})
	}
	json.NewEncoder(w).Encode(out)
}
