/*
Copyright 2017-2019 Gravitational, Inc.

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

package services

import (
	"crypto"
	"crypto/x509"
	"encoding/json"
	"time"

	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/api/types/wrappers"
	"github.com/gravitational/teleport/lib/auth/u2f"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/jwt"
	"github.com/gravitational/teleport/lib/sshutils"
	"github.com/gravitational/teleport/lib/tlsca"
	"github.com/gravitational/teleport/lib/utils"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/jonboulle/clockwork"

	"github.com/gravitational/trace"
)

// CertAuthoritiesEquivalent checks if a pair of certificate authority resources are equivalent.
// This differs from normal equality only in that resource IDs are ignored.
func CertAuthoritiesEquivalent(lhs, rhs types.CertAuthority) bool {
	return cmp.Equal(lhs, rhs, cmpopts.IgnoreFields(types.Metadata{}, "ID"))
}

// NewJWTAuthority creates and returns a types.CertAuthority with a new
// key pair.
func NewJWTAuthority(clusterName string) (types.CertAuthority, error) {
	var err error
	var keyPair types.JWTKeyPair
	if keyPair.PublicKey, keyPair.PrivateKey, err = jwt.GenerateKeyPair(); err != nil {
		return nil, trace.Wrap(err)
	}
	return types.NewCertAuthority(types.CertAuthoritySpecV2{
		Type:        types.JWTSigner,
		ClusterName: clusterName,
		JWTKeyPairs: []types.JWTKeyPair{keyPair},
	}), nil
}

// NewCertAuthority returns new cert authority.
// Replaced by types.NewCertAuthority.
// DELETE in 7.0.0
func NewCertAuthority(
	caType types.CertAuthType,
	clusterName string,
	signingKeys [][]byte,
	checkingKeys [][]byte,
	roles []string,
	signingAlg types.CertAuthoritySpecV2_SigningAlgType,
) types.CertAuthority {
	return types.NewCertAuthority(types.CertAuthoritySpecV2{
		Type:         caType,
		ClusterName:  clusterName,
		SigningKeys:  signingKeys,
		CheckingKeys: checkingKeys,
		Roles:        roles,
		SigningAlg:   signingAlg,
	})
}

// ValidateCertAuthority validates the CertAuthority
func ValidateCertAuthority(ca types.CertAuthority) (err error) {
	if err = ca.CheckAndSetDefaults(); err != nil {
		return trace.Wrap(err)
	}
	switch ca.GetType() {
	case types.UserCA, types.HostCA:
		err = checkUserOrHostCA(ca)
	case types.JWTSigner:
		err = checkJWTKeys(ca)
	default:
		return trace.BadParameter("invalid CA type %q", ca.GetType())
	}
	return trace.Wrap(err)
}

func checkUserOrHostCA(ca types.CertAuthority) error {
	if len(ca.GetCheckingKeys()) == 0 {
		return trace.BadParameter("certificate authority missing SSH public keys")
	}
	if len(ca.GetTLSKeyPairs()) == 0 {
		return trace.BadParameter("certificate authority missing TLS key pairs")
	}
	if _, err := sshutils.GetCheckers(ca); err != nil {
		return trace.Wrap(err)
	}
	if _, err := sshutils.GetSigners(ca); err != nil {
		return trace.Wrap(err)
	}
	// This is to force users to migrate
	if len(ca.GetRoles()) != 0 && len(ca.GetRoleMap()) != 0 {
		return trace.BadParameter("should set either 'roles' or 'role_map', not both")
	}
	_, err := parseRoleMap(ca.GetRoleMap())
	return trace.Wrap(err)
}

func checkJWTKeys(ca types.CertAuthority) error {
	// Check that some JWT keys have been set on the CA.
	if len(ca.GetJWTKeyPairs()) == 0 {
		return trace.BadParameter("missing JWT CA")
	}

	var err error
	var privateKey crypto.Signer

	// Check that the JWT keys set are valid.
	for _, pair := range ca.GetJWTKeyPairs() {
		if len(pair.PrivateKey) > 0 {
			privateKey, err = utils.ParsePrivateKey(pair.PrivateKey)
			if err != nil {
				return trace.Wrap(err)
			}
		}
		publicKey, err := utils.ParsePublicKey(pair.PublicKey)
		if err != nil {
			return trace.Wrap(err)
		}
		cfg := &jwt.Config{
			Algorithm:   defaults.ApplicationTokenAlgorithm,
			ClusterName: ca.GetClusterName(),
			PrivateKey:  privateKey,
			PublicKey:   publicKey,
		}
		if _, err = jwt.New(cfg); err != nil {
			return trace.Wrap(err)
		}
	}

	return nil
}

// GetJWTSigner returns the active JWT key used to sign tokens.
func GetJWTSigner(ca types.CertAuthority, clock clockwork.Clock) (*jwt.Key, error) {
	if len(ca.GetJWTKeyPairs()) == 0 {
		return nil, trace.BadParameter("no JWT keypairs found")
	}
	privateKey, err := utils.ParsePrivateKey(ca.GetJWTKeyPairs()[0].PrivateKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	key, err := jwt.New(&jwt.Config{
		Clock:       clock,
		Algorithm:   defaults.ApplicationTokenAlgorithm,
		ClusterName: ca.GetClusterName(),
		PrivateKey:  privateKey,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return key, nil
}

// GetTLSCerts returns TLS certificates from CA
func GetTLSCerts(ca types.CertAuthority) [][]byte {
	pairs := ca.GetTLSKeyPairs()
	out := make([][]byte, len(pairs))
	for i, pair := range pairs {
		out[i] = append([]byte{}, pair.Cert...)
	}
	return out
}

// HostCertParams defines all parameters needed to generate a host certificate
type HostCertParams struct {
	// PrivateCASigningKey is the private key of the CA that will sign the public key of the host
	PrivateCASigningKey []byte
	// CASigningAlg is the signature algorithm used by the CA private key.
	CASigningAlg string
	// PublicHostKey is the public key of the host
	PublicHostKey []byte
	// HostID is used by Teleport to uniquely identify a node within a cluster
	HostID string
	// Principals is a list of additional principals to add to the certificate.
	Principals []string
	// NodeName is the DNS name of the node
	NodeName string
	// ClusterName is the name of the cluster within which a node lives
	ClusterName string
	// Roles identifies the roles of a Teleport instance
	Roles types.SystemRoles
	// TTL defines how long a certificate is valid for
	TTL time.Duration
}

// Check checks parameters for errors
func (c HostCertParams) Check() error {
	if len(c.PrivateCASigningKey) == 0 || c.CASigningAlg == "" {
		return trace.BadParameter("PrivateCASigningKey and CASigningAlg are required")
	}
	if c.HostID == "" && len(c.Principals) == 0 {
		return trace.BadParameter("HostID [%q] or Principals [%q] are required",
			c.HostID, c.Principals)
	}
	if c.ClusterName == "" {
		return trace.BadParameter("ClusterName [%q] is required", c.ClusterName)
	}

	if err := c.Roles.Check(); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// ChangePasswordReq defines a request to change user password
type ChangePasswordReq struct {
	// User is user ID
	User string
	// OldPassword is user current password
	OldPassword []byte `json:"old_password"`
	// NewPassword is user new password
	NewPassword []byte `json:"new_password"`
	// SecondFactorToken is user 2nd factor token
	SecondFactorToken string `json:"second_factor_token"`
	// U2FSignResponse is U2F sign response
	U2FSignResponse *u2f.AuthenticateChallengeResponse `json:"u2f_sign_response"`
}

// UserCertParams defines OpenSSH user certificate parameters
type UserCertParams struct {
	// PrivateCASigningKey is the private key of the CA that will sign the public key of the user
	PrivateCASigningKey []byte
	// CASigningAlg is the signature algorithm used by the CA private key.
	CASigningAlg string
	// PublicUserKey is the public key of the user
	PublicUserKey []byte
	// TTL defines how long a certificate is valid for
	TTL time.Duration
	// Username is teleport username
	Username string
	// Impersonator is set when a user requests certificate for another user
	Impersonator string
	// AllowedLogins is a list of SSH principals
	AllowedLogins []string
	// PermitX11Forwarding permits X11 forwarding for this cert
	PermitX11Forwarding bool
	// PermitAgentForwarding permits agent forwarding for this cert
	PermitAgentForwarding bool
	// PermitPortForwarding permits port forwarding.
	PermitPortForwarding bool
	// Roles is a list of roles assigned to this user
	Roles []string
	// CertificateFormat is the format of the SSH certificate.
	CertificateFormat string
	// RouteToCluster specifies the target cluster
	// if present in the certificate, will be used
	// to route the requests to
	RouteToCluster string
	// Traits hold claim data used to populate a role at runtime.
	Traits wrappers.Traits
	// ActiveRequests tracks privilege escalation requests applied during
	// certificate construction.
	ActiveRequests RequestIDs
	// MFAVerified is the UUID of an MFA device when this Identity was
	// confirmed immediately after an MFA check.
	MFAVerified string
	// ClientIP is an IP of the client to embed in the certificate.
	ClientIP string
}

// Check checks the user certificate parameters
func (c *UserCertParams) CheckAndSetDefaults() error {
	if len(c.PrivateCASigningKey) == 0 || c.CASigningAlg == "" {
		return trace.BadParameter("PrivateCASigningKey and CASigningAlg are required")
	}
	if c.TTL < defaults.MinCertDuration {
		c.TTL = defaults.MinCertDuration
	}
	if len(c.AllowedLogins) == 0 {
		return trace.BadParameter("AllowedLogins are required")
	}
	return nil
}

// CertPoolFromCertAuthorities returns certificate pools from TLS certificates
// set up in the certificate authorities list
func CertPoolFromCertAuthorities(cas []types.CertAuthority) (*x509.CertPool, error) {
	certPool := x509.NewCertPool()
	for _, ca := range cas {
		keyPairs := ca.GetTLSKeyPairs()
		if len(keyPairs) == 0 {
			continue
		}
		for _, keyPair := range keyPairs {
			cert, err := tlsca.ParseCertificatePEM(keyPair.Cert)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			certPool.AddCert(cert)
		}
	}
	return certPool, nil
}

// CertPool returns certificate pools from TLS certificates
// set up in the certificate authority
func CertPool(ca types.CertAuthority) (*x509.CertPool, error) {
	keyPairs := ca.GetTLSKeyPairs()
	if len(keyPairs) == 0 {
		return nil, trace.BadParameter("certificate authority has no TLS certificates")
	}
	certPool := x509.NewCertPool()
	for _, keyPair := range keyPairs {
		cert, err := tlsca.ParseCertificatePEM(keyPair.Cert)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		certPool.AddCert(cert)
	}
	return certPool, nil
}

// MarshalCertRoles marshal roles list to OpenSSH
func MarshalCertRoles(roles []string) (string, error) {
	out, err := json.Marshal(types.CertRoles{Version: types.V1, Roles: roles})
	if err != nil {
		return "", trace.Wrap(err)
	}
	return string(out), err
}

// UnmarshalCertRoles marshals roles list to OpenSSH format
func UnmarshalCertRoles(data string) ([]string, error) {
	var certRoles types.CertRoles
	if err := utils.FastUnmarshal([]byte(data), &certRoles); err != nil {
		return nil, trace.BadParameter(err.Error())
	}
	return certRoles.Roles, nil
}

// UnmarshalCertAuthority unmarshals the CertAuthority resource to JSON.
func UnmarshalCertAuthority(bytes []byte, opts ...MarshalOption) (types.CertAuthority, error) {
	cfg, err := CollectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var h types.ResourceHeader
	err = utils.FastUnmarshal(bytes, &h)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	switch h.Version {
	case types.V2:
		var ca types.CertAuthorityV2
		if err := utils.FastUnmarshal(bytes, &ca); err != nil {
			return nil, trace.BadParameter(err.Error())
		}
		if err := ValidateCertAuthority(&ca); err != nil {
			return nil, trace.Wrap(err)
		}
		if cfg.ID != 0 {
			ca.SetResourceID(cfg.ID)
		}
		return &ca, nil
	}

	return nil, trace.BadParameter("cert authority resource version %v is not supported", h.Version)
}

// MarshalCertAuthority marshals the CertAuthority resource to JSON.
func MarshalCertAuthority(certAuthority types.CertAuthority, opts ...MarshalOption) ([]byte, error) {
	cfg, err := CollectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	switch certAuthority := certAuthority.(type) {
	case *types.CertAuthorityV2:
		if version := certAuthority.GetVersion(); version != types.V2 {
			return nil, trace.BadParameter("mismatched certificate authority version %v and type %T", version, certAuthority)
		}
		if !cfg.PreserveResourceID {
			// avoid modifying the original object
			// to prevent unexpected data races
			copy := *certAuthority
			copy.SetResourceID(0)
			certAuthority = &copy
		}
		return utils.FastMarshal(certAuthority)
	default:
		return nil, trace.BadParameter("unrecognized certificate authority version %T", certAuthority)
	}
}
