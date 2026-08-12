package controlapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/pkg/envelope"
)

const siteSessionPrefix = "fos_v1."

var ErrInvalidSiteSession = errors.New("site session is invalid")

type SiteMembership struct {
	ID             string `json:"id"`
	SiteProjectID  string `json:"siteProjectId"`
	SiteUserKey    string `json:"siteUserKey"`
	OrganizationID string `json:"organizationId"`
	PrincipalID    string `json:"principalId"`
	Role           Role   `json:"role"`
}

func (m SiteMembership) Valid() bool {
	return envelope.ValidIdentifier(m.ID) && envelope.ValidIdentifier(m.SiteProjectID) &&
		validDigestHex(m.SiteUserKey) && envelope.ValidIdentifier(m.OrganizationID) &&
		envelope.ValidIdentifier(m.PrincipalID) && validHumanRole(m.Role)
}

type siteSessionClaims struct {
	Version        int    `json:"version"`
	MembershipID   string `json:"membershipId"`
	SiteProjectID  string `json:"siteProjectId"`
	SiteUserKey    string `json:"siteUserKey"`
	OrganizationID string `json:"organizationId"`
	PrincipalID    string `json:"principalId"`
	Role           Role   `json:"role"`
	IssuedAt       int64  `json:"issuedAt"`
	ExpiresAt      int64  `json:"expiresAt"`
}

type SiteSessionCodec struct {
	key   []byte
	ttl   time.Duration
	clock func() time.Time
}

func NewSiteSessionCodec(key []byte, ttl time.Duration, clock func() time.Time) (*SiteSessionCodec, error) {
	if len(key) != 32 {
		return nil, errors.New("site session key must contain exactly 32 bytes")
	}
	if ttl < time.Second || ttl > 5*time.Minute {
		return nil, errors.New("site session TTL must be at least one second and no longer than five minutes")
	}
	if clock == nil {
		clock = time.Now
	}
	return &SiteSessionCodec{key: append([]byte(nil), key...), ttl: ttl, clock: clock}, nil
}

func (c *SiteSessionCodec) Mint(membership SiteMembership) (string, time.Time, error) {
	if c == nil || !membership.Valid() {
		return "", time.Time{}, ErrInvalidSiteSession
	}
	now := c.clock().UTC().Truncate(time.Second)
	expiresAt := now.Add(c.ttl)
	claims := siteSessionClaims{
		Version: 1, MembershipID: membership.ID, SiteProjectID: membership.SiteProjectID,
		SiteUserKey: membership.SiteUserKey, OrganizationID: membership.OrganizationID,
		PrincipalID: membership.PrincipalID, Role: membership.Role,
		IssuedAt: now.Unix(), ExpiresAt: expiresAt.Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encode site session: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := c.sign(encoded)
	return siteSessionPrefix + encoded + "." + base64.RawURLEncoding.EncodeToString(signature), expiresAt, nil
}

func (c *SiteSessionCodec) Verify(token string) (SiteMembership, error) {
	if c == nil || len(token) < len(siteSessionPrefix)+3 || len(token) > 2048 || !strings.HasPrefix(token, siteSessionPrefix) {
		return SiteMembership{}, ErrInvalidSiteSession
	}
	parts := strings.Split(strings.TrimPrefix(token, siteSessionPrefix), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return SiteMembership{}, ErrInvalidSiteSession
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, c.sign(parts[0])) {
		return SiteMembership{}, ErrInvalidSiteSession
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) > 2048 {
		return SiteMembership{}, ErrInvalidSiteSession
	}
	var claims siteSessionClaims
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		return SiteMembership{}, ErrInvalidSiteSession
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return SiteMembership{}, ErrInvalidSiteSession
	}
	membership := SiteMembership{
		ID: claims.MembershipID, SiteProjectID: claims.SiteProjectID, SiteUserKey: claims.SiteUserKey,
		OrganizationID: claims.OrganizationID, PrincipalID: claims.PrincipalID, Role: claims.Role,
	}
	now := c.clock().UTC().Unix()
	if claims.Version != 1 || !membership.Valid() || claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt ||
		claims.ExpiresAt-claims.IssuedAt > int64((5*time.Minute)/time.Second) || claims.IssuedAt > now+30 || now >= claims.ExpiresAt {
		return SiteMembership{}, ErrInvalidSiteSession
	}
	return membership, nil
}

func (c *SiteSessionCodec) sign(encodedPayload string) []byte {
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write([]byte(siteSessionPrefix))
	_, _ = mac.Write([]byte(encodedPayload))
	return mac.Sum(nil)
}

func SiteUserKey(siteProjectID, siteUserID string) (string, error) {
	if !envelope.ValidIdentifier(siteProjectID) || strings.TrimSpace(siteUserID) == "" || len(siteUserID) > 512 {
		return "", errors.New("site identity is invalid")
	}
	digest := sha256.Sum256([]byte(siteProjectID + "\x00" + siteUserID))
	return fmt.Sprintf("%x", digest[:]), nil
}

func normalizedEmailDigest(email string) ([32]byte, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || len(normalized) > 320 || strings.ContainsAny(normalized, "\r\n\x00") ||
		strings.Count(normalized, "@") != 1 || strings.HasPrefix(normalized, "@") || strings.HasSuffix(normalized, "@") {
		return [32]byte{}, errors.New("site email is invalid")
	}
	return sha256.Sum256([]byte(normalized)), nil
}

func validHumanRole(role Role) bool {
	switch role {
	case RoleOwner, RoleAdmin, RoleDeveloper, RoleFinance, RoleApprover, RoleAuditor, RoleViewer:
		return true
	default:
		return false
	}
}

func validDigestHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
