package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"kvm-center/internal/models"
)

const (
	pbkdf2Iterations = 210000
	pbkdf2KeyLen     = 32
	pbkdf2SaltLen    = 16
)

// hashPassword returns a PBKDF2-SHA256 hash string in the format:
//
//	pbkdf2$sha256$<iterations>$<base64-salt>$<base64-hash>
//
// This is a compact self-describing format so we can raise iterations in
// the future without breaking existing accounts.
func hashPassword(password string) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2([]byte(password), salt, pbkdf2Iterations, pbkdf2KeyLen)
	return fmt.Sprintf("pbkdf2$sha256$%d$%s$%s",
		pbkdf2Iterations,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(key)), nil
}

// verifyPassword returns true if password matches the stored hash.
// Constant-time comparison prevents timing leaks.
func verifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 5 || parts[0] != "pbkdf2" || parts[1] != "sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[2])
	if err != nil || iter < 1000 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	expected, err := base64.StdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	actual := pbkdf2([]byte(password), salt, iter, len(expected))
	return subtle.ConstantTimeCompare(expected, actual) == 1
}

// pbkdf2 is an RFC 2898 PBKDF2-HMAC-SHA256 implementation. ~30 lines,
// stdlib only, deterministic.
func pbkdf2(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	out := make([]byte, 0, numBlocks*hashLen)
	buf := make([]byte, 4)
	for block := 1; block <= numBlocks; block++ {
		binary.BigEndian.PutUint32(buf, uint32(block))
		prf.Reset()
		prf.Write(salt)
		prf.Write(buf)
		u := prf.Sum(nil)
		t := make([]byte, hashLen)
		copy(t, u)
		for i := 2; i <= iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// newSessionToken generates a URL-safe random session token.
func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newID generates a short random ID for users/groups/roles.
func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(b)
}

// effectivePermissions returns the merged permission set for a user, combining
// direct role assignments and roles inherited through group membership.
func effectivePermissions(s *Store, u *models.User) map[string]bool {
	perms := map[string]bool{}
	if u == nil || !u.Enabled {
		return perms
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	addRole := func(rid string) {
		if r, ok := s.roles[rid]; ok {
			for _, p := range r.Permissions {
				perms[p] = true
			}
		}
	}
	for _, rid := range u.RoleIDs {
		addRole(rid)
	}
	for _, gid := range u.GroupIDs {
		if g, ok := s.groups[gid]; ok {
			for _, rid := range g.RoleIDs {
				addRole(rid)
			}
		}
	}
	return perms
}

func hasPermission(s *Store, u *models.User, needed string) bool {
	perms := effectivePermissions(s, u)
	return perms[models.PermAdmin] || perms[needed]
}

// sanitizeUser strips the password hash before sending over the API.
func sanitizeUser(u *models.User) *models.User {
	if u == nil {
		return nil
	}
	c := *u
	c.PasswordHash = ""
	return &c
}
