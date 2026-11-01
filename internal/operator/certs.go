package operator

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	admissionv1 "k8s.io/api/admissionregistration/v1"
)

const (
	secretCert = "tls.crt"
	secretKey  = "tls.key"
	secretCA   = "ca.crt"
	// DefaultLeafTTL is the self-managed serving certificate lifetime.
	DefaultLeafTTL = 24 * time.Hour
	// DefaultRotateBefore is how long before expiry a certificate is replaced.
	DefaultRotateBefore = time.Hour
)

// Issuer mints a self-managed webhook CA and leaf and patches caBundle.
type Issuer struct {
	Client       client.Client
	Namespace    string
	SecretName   string
	WebhookName  string
	DNSNames     []string
	Clock        Clock
	LeafTTL      time.Duration
	RotateBefore time.Duration
}

// Ensure writes the serving secret and patches the webhook caBundle.
// A leaf inside the rotate-before window is minted again.
func (i *Issuer) Ensure(ctx context.Context) error {
	if i.Client == nil {
		return errors.New("cert issuer client is unset")
	}
	secret, err := i.current(ctx)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if secret != nil && !i.expiring(secret.Data[secretCert]) {
		return i.patchCA(ctx, secret.Data[secretCA])
	}
	caPEM, certPEM, keyPEM, err := i.mint()
	if err != nil {
		return err
	}
	if err := i.write(ctx, secret, caPEM, certPEM, keyPEM); err != nil {
		return err
	}
	return i.patchCA(ctx, caPEM)
}

// Certificate is the serving certificate for tls.Config.GetCertificate.
func (i *Issuer) Certificate(ctx context.Context) (*tls.Certificate, error) {
	if err := i.Ensure(ctx); err != nil {
		return nil, err
	}
	var secret corev1.Secret
	if err := i.Client.Get(ctx, client.ObjectKey{Namespace: i.Namespace, Name: i.SecretName}, &secret); err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(secret.Data[secretCert], secret.Data[secretKey])
	if err != nil {
		return nil, err
	}
	return &pair, nil
}

func (i *Issuer) current(ctx context.Context) (*corev1.Secret, error) {
	var secret corev1.Secret
	err := i.Client.Get(ctx, client.ObjectKey{Namespace: i.Namespace, Name: i.SecretName}, &secret)
	if err != nil {
		return nil, err
	}
	if len(secret.Data[secretCert]) == 0 || len(secret.Data[secretKey]) == 0 || len(secret.Data[secretCA]) == 0 {
		return nil, apierrors.NewNotFound(corev1.Resource("secrets"), i.SecretName)
	}
	return &secret, nil
}

func (i *Issuer) expiring(certPEM []byte) bool {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return true
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	return i.now().Add(i.rotateBefore()).After(cert.NotAfter)
}

func (i *Issuer) now() time.Time {
	if i.Clock != nil {
		return i.Clock.Now()
	}
	return time.Now()
}

func (i *Issuer) ttl() time.Duration {
	if i.LeafTTL > 0 {
		return i.LeafTTL
	}
	return DefaultLeafTTL
}

func (i *Issuer) rotateBefore() time.Duration {
	if i.RotateBefore > 0 {
		return i.RotateBefore
	}
	return DefaultRotateBefore
}

func (i *Issuer) mint() (caPEM, certPEM, keyPEM []byte, err error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	caCert, err := i.signCA(caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	leafCert, err := i.signLeaf(leafKey, caCert, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafCert.Raw})
	keyBytes, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return nil, nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	return caPEM, certPEM, keyPEM, nil
}

func (i *Issuer) signCA(key *ecdsa.PrivateKey) (*x509.Certificate, error) {
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "factory-operator-webhook-ca"},
		NotBefore:             i.now().Add(-time.Minute),
		NotAfter:              i.now().Add(i.ttl() * 2),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

func (i *Issuer) signLeaf(key *ecdsa.PrivateKey, ca *x509.Certificate, caKey *ecdsa.PrivateKey) (*x509.Certificate, error) {
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: i.DNSNamesFirst()},
		NotBefore:    i.now().Add(-time.Minute),
		NotAfter:     i.now().Add(i.ttl()),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     i.DNSNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

func (i *Issuer) DNSNamesFirst() string {
	if len(i.DNSNames) == 0 {
		return "factory-operator-webhook"
	}
	return i.DNSNames[0]
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}

func (i *Issuer) write(ctx context.Context, existing *corev1.Secret, caPEM, certPEM, keyPEM []byte) error {
	data := map[string][]byte{secretCA: caPEM, secretCert: certPEM, secretKey: keyPEM}
	if existing == nil {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: i.Namespace, Name: i.SecretName},
			Type:       corev1.SecretTypeTLS,
			Data:       data,
		}
		return i.Client.Create(ctx, secret)
	}
	base := existing.DeepCopy()
	existing.Data = data
	return i.Client.Patch(ctx, existing, client.MergeFrom(base))
}

func (i *Issuer) patchCA(ctx context.Context, caPEM []byte) error {
	var cfg admissionv1.ValidatingWebhookConfiguration
	if err := i.Client.Get(ctx, client.ObjectKey{Name: i.WebhookName}, &cfg); err != nil {
		return err
	}
	base := cfg.DeepCopy()
	changed := false
	for n := range cfg.Webhooks {
		if string(cfg.Webhooks[n].ClientConfig.CABundle) != string(caPEM) {
			cfg.Webhooks[n].ClientConfig.CABundle = append([]byte(nil), caPEM...)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return i.Client.Patch(ctx, &cfg, client.MergeFrom(base))
}
