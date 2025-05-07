package chromekiosk

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

func makeCertificate() (*tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		return nil, err
	}

	var (
		now      = time.Now()
		template = &x509.Certificate{
			Subject: pkix.Name{
				Organization:       []string{"go.pdmccormick.com"},
				OrganizationalUnit: []string{"chromekiosk"},
				CommonName:         "proxy",
			},

			NotBefore: now.AddDate(0, -1, 0),
			NotAfter:  now.AddDate(0, 1, 0),

			KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},

			BasicConstraintsValid: true,
			IsCA:                  true,
			MaxPathLenZero:        true,
		}
	)

	certDer, err := x509.CreateCertificate(cryptorand.Reader, template, template, priv.Public(), priv)
	if err != nil {
		return nil, err
	}

	cert, err := x509.ParseCertificate(certDer)
	if err != nil {
		return nil, err
	}

	var tlsCert = tls.Certificate{
		Certificate: [][]byte{certDer},
		PrivateKey:  priv,
		Leaf:        cert,
	}

	return &tlsCert, nil
}

func runTLSProxy(ctx context.Context, ln net.Listener, h http.Handler) error {
	if h == nil {
		h = &DefaultProxyHandler
	}

	tlsCert, err := makeCertificate()
	if err != nil {
		return err
	}

	var (
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{*tlsCert},
			MinVersion:   tls.VersionTLS13,
		}
		serv = http.Server{
			Handler:     h,
			TLSConfig:   tlsConfig,
			BaseContext: func(net.Listener) context.Context { return ctx },
		}
		tlsListener = tls.NewListener(ln, tlsConfig)
	)

	return serv.Serve(tlsListener)
}

var InternalChromeRequests = []string{
	"CONNECT //accounts.google.com:443",
	"CONNECT //content-autofill.googleapis.com:443",
	"CONNECT //optimizationguide-pa.googleapis.com:443",
	"CONNECT //safebrowsingohttpgateway.googleapis.com:443",
	"CONNECT //update.googleapis.com:443",
	"CONNECT //www.google.com:443",
	"GET http://clients2.google.com/time/1/current?",
	"POST http://update.googleapis.com/service/update2/json?",
}

func IsInternalChromeRequest(r *http.Request) bool {
	return isInternalChromeRequestStr(r.Method + " " + r.URL.String())
}

func isInternalChromeRequestStr(urlStr string) bool {
	urls := InternalChromeRequests
	n, _ := slices.BinarySearch(urls, urlStr)

	if n > 1 {
		if strings.HasPrefix(urlStr, urls[n-1]) {
			return true
		}
	}

	if n == len(urls) {
		n--
	}

	if urlStr == urls[n] {
		return true
	}

	return false
}

type Proxy struct {
	Logger              *log.Logger
	Dialer              net.Dialer
	Transport           http.RoundTripper
	AllowChromeInternal bool
}

var _ http.Handler = (*Proxy)(nil)

var DefaultProxyHandler = Proxy{
	Logger: log.Default(),
}

func (pr *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !pr.AllowChromeInternal && IsInternalChromeRequest(r) {
		http.Error(w, "", http.StatusGatewayTimeout)
		return
	}

	pr.logf("proxy: %s %s", r.Method, r.URL)

	if r.Method == "CONNECT" {
		pr.connect(w, r)
		return
	}

	pr.passthru(w, r)
}

func (pr *Proxy) logf(format string, v ...any) {
	l := pr.Logger
	if l == nil {
		return
	}
	l.Output(2, fmt.Sprintf(format, v...))
}

func (pr *Proxy) passthru(w http.ResponseWriter, r *http.Request) {
	var (
		src = r.URL
		dst = url.URL{
			Scheme: src.Scheme,
			Host:   src.Host,
			Path:   src.Path,
		}
	)

	if dst.Scheme == "" {
		dst.Scheme = "http"
	}

	if dst.Host == "" {
		dst.Host = r.Host
	}

	req, err := http.NewRequest(r.Method, dst.String(), r.Body)
	if err != nil {
		pr.logf("proxy: %s %s: NewRequest %s", r.Method, r.URL, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	req.Header = r.Header.Clone()

	tr := pr.Transport
	if tr == nil {
		tr = http.DefaultTransport
	}

	resp, err := tr.RoundTrip(req)
	if err != nil {
		pr.logf("proxy: %s %s: RoundTrip %s", r.Method, r.URL, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	defer resp.Body.Close()

	hdr := w.Header()

	for k, vs := range resp.Header {
		for _, v := range vs {
			hdr.Add(k, v)
		}
	}

	hdr.Set("Connection", "close")
	hdr.Del("Content-Length")

	var flush = func() {}
	if f, ok := w.(http.Flusher); ok {
		flush = f.Flush
	}

	w.WriteHeader(resp.StatusCode)

	var raw = make([]byte, 4096)
	for {
		n, err := resp.Body.Read(raw)
		if n == 0 && err != nil {
			return
		}

		buf := raw[:n]

		if _, err := w.Write(buf); err != nil {
			return
		}

		flush()
	}
}

func (pr *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "ResponseWriter does not implement http.Hijacker", http.StatusInternalServerError)
		return
	}

	addr := net.JoinHostPort(r.URL.Hostname(), r.URL.Port())
	pr.logf("CONNECT %s", addr)

	upconn, err := pr.Dialer.DialContext(r.Context(), "tcp", addr)
	if err != nil {
		pr.logf("dial %s error: %s", addr, err)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			http.Error(w, err.Error(), http.StatusGatewayTimeout)
		} else {
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
		return
	}

	defer upconn.Close()

	w.WriteHeader(http.StatusOK)

	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return
	}

	if bufr := bufrw.Reader; bufr.Buffered() > 0 {
		if _, err := io.Copy(upconn, bufr); err != nil {
			return
		}

		bufr.Reset(nil)
	}

	teeConn(conn, upconn)
}

func teeConn(left, right io.ReadWriteCloser) error {
	var (
		wg   sync.WaitGroup
		errL error
		errR error
	)
	wg.Add(2)

	// Left to right
	go func() {
		defer wg.Done()
		defer left.Close()
		defer right.Close()

		_, errL = io.Copy(right, left)
	}()

	// Right to left
	go func() {
		defer wg.Done()
		defer left.Close()
		defer right.Close()

		_, errR = io.Copy(left, right)
	}()

	wg.Wait()

	return errors.Join(errL, errR)
}
