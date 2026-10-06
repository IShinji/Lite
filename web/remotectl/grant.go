package remotectl

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/accounts"
)

const (
	ScopeRemote     = "remote"
	ScopeExec       = "exec"
	GrantTTL        = 10 * time.Minute
	grantSecretSize = 32
)

func init() {
	accounts.AddUserSecurityListener(RevokeUser)
}

var (
	afterRevokeAll   func()
	afterRevokeLogin func(loginSession string)
)

// AddRevokeAllListener runs after human remote grants are cleared globally.
func AddRevokeAllListener(fn func()) {
	afterRevokeAll = fn
}

// AddRevokeLoginListener runs after grants for one login session are cleared.
func AddRevokeLoginListener(fn func(loginSession string)) {
	afterRevokeLogin = fn
}

var (
	ErrGrantRequired   = errors.New("remote grant is required")
	ErrGrantInvalid    = errors.New("remote grant is invalid")
	ErrGrantExpired    = errors.New("remote grant has expired")
	ErrGrantScope      = errors.New("remote grant does not match this page")
	ErrGrantPrincipal  = errors.New("remote grant does not match this login")
	ErrGrantWorkspace  = errors.New("remote grant does not match this workspace")
	ErrAPIKeyForbidden = errors.New("API keys cannot authorize remote management")
)

type storedGrant struct {
	hash         [32]byte
	userUUID     string
	loginSession string
	scope        string
	pageID       string
	expiresAt    time.Time
	epoch        uint64
}

// execPermit is the single-use right to rotate an exec grant that was just
// consumed. It keeps the epoch from identity confirmation, not a later read.
type execPermit struct {
	userUUID     string
	loginSession string
	pageID       string
	expiresAt    time.Time
	epoch        uint64
}

var (
	grantMu     sync.Mutex
	grants      = make(map[string]storedGrant) // keyed by hex(hash)
	execPermits = make(map[string]execPermit)
)

func execPermitKey(userUUID, loginSession, pageID string) string {
	return userUUID + "\x00" + loginSession + "\x00" + pageID
}

func IssueGrant(userUUID, loginSession, scope, pageID string) (string, time.Time, error) {
	return IssueGrantAtEpoch(userUUID, loginSession, scope, pageID, accounts.UserSecurityEpoch(userUUID))
}

// IssueGrantAtEpoch stores a grant only when the account is still at the
// security generation observed before the credential check.
func IssueGrantAtEpoch(userUUID, loginSession, scope, pageID string, epoch uint64) (string, time.Time, error) {
	return issueGrant(userUUID, loginSession, scope, pageID, time.Now().Add(GrantTTL), epoch)
}

func issueGrant(userUUID, loginSession, scope, pageID string, expires time.Time, epoch uint64) (string, time.Time, error) {
	prepared, err := prepareGrant(userUUID, loginSession, scope, pageID, expires)
	if err != nil {
		return "", time.Time{}, err
	}
	grantMu.Lock()
	defer grantMu.Unlock()
	if accounts.UserSecurityEpoch(prepared.userUUID) != epoch {
		return "", time.Time{}, ErrGrantInvalid
	}
	prepared.epoch = epoch
	return storeGrantLocked(prepared)
}

type preparedGrant struct {
	plain        string
	sum          [32]byte
	userUUID     string
	loginSession string
	scope        string
	pageID       string
	expiresAt    time.Time
	epoch        uint64
}

func prepareGrant(userUUID, loginSession, scope, pageID string, expires time.Time) (preparedGrant, error) {
	if userUUID == "" || loginSession == "" {
		return preparedGrant{}, ErrGrantPrincipal
	}
	loginSession = accounts.SessionLookupKey(loginSession)
	if scope != ScopeRemote && scope != ScopeExec {
		return preparedGrant{}, ErrGrantScope
	}
	pageID = strings.TrimSpace(pageID)
	if pageID == "" {
		return preparedGrant{}, ErrGrantWorkspace
	}
	if !expires.After(time.Now()) {
		return preparedGrant{}, ErrGrantExpired
	}
	var secret [grantSecretSize]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return preparedGrant{}, err
	}
	plain := hex.EncodeToString(secret[:])
	return preparedGrant{
		plain:        plain,
		sum:          sha256.Sum256([]byte(plain)),
		userUUID:     userUUID,
		loginSession: loginSession,
		scope:        scope,
		pageID:       pageID,
		expiresAt:    expires,
	}, nil
}

func storeGrantLocked(prepared preparedGrant) (string, time.Time, error) {
	pruneGrantsLocked(time.Now())
	grants[hex.EncodeToString(prepared.sum[:])] = storedGrant{
		hash:         prepared.sum,
		userUUID:     prepared.userUUID,
		loginSession: prepared.loginSession,
		scope:        prepared.scope,
		pageID:       prepared.pageID,
		expiresAt:    prepared.expiresAt,
		epoch:        prepared.epoch,
	}
	return prepared.plain, prepared.expiresAt, nil
}

func ConsumeAndRotateGrant(plain, userUUID, loginSession, scope, pageID string) (string, time.Time, error) {
	stored, err := lookupGrant(plain, userUUID, loginSession, scope, pageID, true)
	if err != nil {
		return "", time.Time{}, err
	}
	return issueGrant(stored.userUUID, stored.loginSession, stored.scope, stored.pageID, stored.expiresAt, stored.epoch)
}

func TakeExecGrant(plain, userUUID, loginSession, pageID string) (time.Time, uint64, error) {
	stored, err := lookupGrant(plain, userUUID, loginSession, ScopeExec, pageID, true)
	if err != nil {
		return time.Time{}, 0, err
	}
	return stored.expiresAt, stored.epoch, nil
}

// rotateGrantHook runs while grantMu is held, before the epoch check.
// Tests use it to show revoke cannot slip between the check and the store.
var rotateGrantHook func()

// RotateExecGrant issues the next exec grant only for a permit created when
// that same grant was consumed. The permit carries the identity-confirmation
// epoch and the original expiry. A later read of the account epoch cannot
// adopt an older grant.
func RotateExecGrant(userUUID, loginSession, pageID string, expires time.Time, epoch uint64) (string, time.Time, error) {
	prepared, err := prepareGrant(userUUID, loginSession, ScopeExec, pageID, expires)
	if err != nil {
		return "", time.Time{}, err
	}
	grantMu.Lock()
	defer grantMu.Unlock()
	if rotateGrantHook != nil {
		rotateGrantHook()
	}
	key := execPermitKey(prepared.userUUID, prepared.loginSession, prepared.pageID)
	permit, ok := execPermits[key]
	delete(execPermits, key)
	if !ok || permit.epoch != epoch || accounts.UserSecurityEpoch(prepared.userUUID) != permit.epoch {
		return "", time.Time{}, ErrGrantInvalid
	}
	prepared.expiresAt = permit.expiresAt
	prepared.epoch = permit.epoch
	return storeGrantLocked(prepared)
}

func lookupGrant(plain, userUUID, loginSession, scope, pageID string, consume bool) (storedGrant, error) {
	if plain == "" {
		return storedGrant{}, ErrGrantRequired
	}
	sum := sha256.Sum256([]byte(plain))
	key := hex.EncodeToString(sum[:])
	now := time.Now()
	grantMu.Lock()
	defer grantMu.Unlock()
	pruneGrantsLocked(now)
	stored, ok := grants[key]
	if !ok {
		return storedGrant{}, ErrGrantInvalid
	}
	if subtle.ConstantTimeCompare(stored.hash[:], sum[:]) != 1 {
		return storedGrant{}, ErrGrantInvalid
	}
	if stored.userUUID != userUUID || stored.loginSession != accounts.SessionLookupKey(loginSession) {
		return storedGrant{}, ErrGrantPrincipal
	}
	if stored.scope != scope {
		return storedGrant{}, ErrGrantScope
	}
	if stored.pageID != strings.TrimSpace(pageID) {
		return storedGrant{}, ErrGrantWorkspace
	}
	if !stored.expiresAt.After(now) {
		delete(grants, key)
		return storedGrant{}, ErrGrantExpired
	}
	if accounts.UserSecurityEpoch(stored.userUUID) != stored.epoch {
		delete(grants, key)
		return storedGrant{}, ErrGrantInvalid
	}
	if consume {
		delete(grants, key)
		if stored.scope == ScopeExec {
			execPermits[execPermitKey(stored.userUUID, stored.loginSession, stored.pageID)] = execPermit{
				userUUID:     stored.userUUID,
				loginSession: stored.loginSession,
				pageID:       stored.pageID,
				expiresAt:    stored.expiresAt,
				epoch:        stored.epoch,
			}
		}
	}
	return stored, nil
}

func RevokeGrant(plain string) {
	if plain == "" {
		return
	}
	sum := sha256.Sum256([]byte(plain))
	key := hex.EncodeToString(sum[:])
	grantMu.Lock()
	delete(grants, key)
	grantMu.Unlock()
}

func RevokeLogin(loginSession string) {
	if loginSession == "" {
		return
	}
	loginSession = accounts.SessionLookupKey(loginSession)
	grantMu.Lock()
	for key, stored := range grants {
		if stored.loginSession == loginSession {
			delete(grants, key)
		}
	}
	for key, permit := range execPermits {
		if permit.loginSession == loginSession {
			delete(execPermits, key)
		}
	}
	grantMu.Unlock()
	if CloseLoginSessions != nil {
		CloseLoginSessions(loginSession)
	}
	if afterRevokeLogin != nil {
		afterRevokeLogin(loginSession)
	}
}

func RevokeUser(userUUID string) {
	if userUUID == "" {
		return
	}
	grantMu.Lock()
	for key, stored := range grants {
		if stored.userUUID == userUUID {
			delete(grants, key)
		}
	}
	for key, permit := range execPermits {
		if permit.userUUID == userUUID {
			delete(execPermits, key)
		}
	}
	grantMu.Unlock()
	if CloseUserSessions != nil {
		CloseUserSessions(userUUID)
	}
}

func RevokeAll() {
	grantMu.Lock()
	grants = make(map[string]storedGrant)
	execPermits = make(map[string]execPermit)
	grantMu.Unlock()
	if CloseAllSessions != nil {
		CloseAllSessions()
	}
	if afterRevokeAll != nil {
		afterRevokeAll()
	}
}

func pruneGrantsLocked(now time.Time) {
	for key, stored := range grants {
		if !stored.expiresAt.After(now) {
			delete(grants, key)
		}
	}
}

func ResetForTest() {
	grantMu.Lock()
	grants = make(map[string]storedGrant)
	execPermits = make(map[string]execPermit)
	grantMu.Unlock()
	resetRateLimitsForTest()
}
