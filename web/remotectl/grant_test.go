package remotectl

import (
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/accounts"
)

func consumeGrant(plain, userUUID, loginSession, scope, pageID string) error {
	_, err := lookupGrant(plain, userUUID, loginSession, scope, pageID, true)
	return err
}

func TestConsumeGrantRejectsEmpty(t *testing.T) {
	ResetForTest()
	if err := consumeGrant("", "user-a", "login-a", ScopeRemote, "page-a"); !errors.Is(err, ErrGrantRequired) {
		t.Fatalf("empty grant error = %v", err)
	}
}

func TestIssueAndLookupGrant(t *testing.T) {
	ResetForTest()
	plain, expires, err := IssueGrant("user-a", "login-a", ScopeRemote, "page-a")
	if err != nil || plain == "" || !expires.After(time.Now()) {
		t.Fatalf("IssueGrant() = %q %v %v", plain, expires, err)
	}
	if err := consumeGrant(plain, "user-b", "login-a", ScopeRemote, "page-a"); !errors.Is(err, ErrGrantPrincipal) {
		t.Fatalf("cross-user grant error = %v", err)
	}
	if err := consumeGrant(plain, "user-a", "login-b", ScopeRemote, "page-a"); !errors.Is(err, ErrGrantPrincipal) {
		t.Fatalf("cross-login grant error = %v", err)
	}
	if err := consumeGrant(plain, "user-a", "login-a", ScopeExec, "page-a"); !errors.Is(err, ErrGrantScope) {
		t.Fatalf("cross-scope grant error = %v", err)
	}
	if err := consumeGrant(plain, "user-a", "login-a", ScopeRemote, "page-b"); !errors.Is(err, ErrGrantWorkspace) {
		t.Fatalf("cross-page grant error = %v", err)
	}
	if err := consumeGrant(plain, "user-a", "login-a", ScopeRemote, "page-a"); err != nil {
		t.Fatalf("valid grant rejected: %v", err)
	}
	if err := consumeGrant(plain, "user-a", "login-a", ScopeRemote, "page-a"); !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("consumed grant error = %v", err)
	}
}

func TestIssueRemoteGrantRequiresPage(t *testing.T) {
	ResetForTest()
	if _, _, err := IssueGrant("user-a", "login-a", ScopeRemote, ""); !errors.Is(err, ErrGrantWorkspace) {
		t.Fatalf("empty page error = %v", err)
	}
}

func TestConsumeAndRotateGrantKeepsPageAndExpiry(t *testing.T) {
	ResetForTest()
	plain, expires, err := IssueGrant("user-a", "login-a", ScopeRemote, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	next, nextExpires, err := ConsumeAndRotateGrant(plain, "user-a", "login-a", ScopeRemote, "page-a")
	if err != nil || next == "" || next == plain {
		t.Fatalf("rotate = %q %v %v", next, nextExpires, err)
	}
	if !nextExpires.Equal(expires) {
		t.Fatalf("rotated expiry = %v, want %v", nextExpires, expires)
	}
	if err := consumeGrant(plain, "user-a", "login-a", ScopeRemote, "page-a"); !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("old grant after rotate error = %v", err)
	}
	if _, _, err := ConsumeAndRotateGrant(next, "user-a", "login-a", ScopeRemote, "page-b"); !errors.Is(err, ErrGrantWorkspace) {
		t.Fatalf("rotated grant on other page error = %v", err)
	}
	if err := consumeGrant(next, "user-a", "login-a", ScopeRemote, "page-a"); err != nil {
		t.Fatalf("rotated grant rejected: %v", err)
	}
}

func TestRevokeGrantAndLogin(t *testing.T) {
	ResetForTest()
	plain, _, err := IssueGrant("user-a", "login-a", ScopeRemote, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	RevokeGrant(plain)
	if err := consumeGrant(plain, "user-a", "login-a", ScopeRemote, "page-a"); !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("revoked grant error = %v", err)
	}
	plain, _, err = IssueGrant("user-a", "login-a", ScopeExec, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	RevokeLogin("login-a")
	if err := consumeGrant(plain, "user-a", "login-a", ScopeExec, "page-a"); !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("login-revoked grant error = %v", err)
	}
}

func TestGrantExpiry(t *testing.T) {
	ResetForTest()
	plain, _, err := IssueGrant("user-a", "login-a", ScopeRemote, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	grantMu.Lock()
	for key, stored := range grants {
		stored.expiresAt = time.Now().Add(-time.Second)
		grants[key] = stored
	}
	grantMu.Unlock()
	if err := consumeGrant(plain, "user-a", "login-a", ScopeRemote, "page-a"); !errors.Is(err, ErrGrantExpired) && !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("expired grant error = %v", err)
	}
}

func TestTakeExecGrantIsSingleUse(t *testing.T) {
	ResetForTest()
	plain, expires, err := IssueGrant("user-a", "login-a", ScopeExec, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	gotExpires, _, err := TakeExecGrant(plain, "user-a", "login-a", "page-a")
	if err != nil {
		t.Fatal(err)
	}
	if !gotExpires.Equal(expires) {
		t.Fatalf("expires = %v, want %v", gotExpires, expires)
	}
	if _, _, err := TakeExecGrant(plain, "user-a", "login-a", "page-a"); !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("reused exec grant error = %v", err)
	}
}

func TestConcurrentTakeExecGrant(t *testing.T) {
	ResetForTest()
	plain, _, err := IssueGrant("user-a", "login-a", ScopeExec, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]error, 8)
	wg.Add(len(results))
	for i := range results {
		go func(i int) {
			defer wg.Done()
			_, _, results[i] = TakeExecGrant(plain, "user-a", "login-a", "page-a")
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("concurrent consume successes = %d, want 1", ok)
	}
}

func TestRotateExecGrantKeepsAbsoluteExpiry(t *testing.T) {
	ResetForTest()
	plain, expires, err := IssueGrant("user-a", "login-a", ScopeExec, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	gotExpires, epoch, err := TakeExecGrant(plain, "user-a", "login-a", "page-a")
	if err != nil {
		t.Fatal(err)
	}
	next, rotatedExpires, err := RotateExecGrant("user-a", "login-a", "page-a", gotExpires, epoch)
	if err != nil || next == "" {
		t.Fatal(err)
	}
	if !rotatedExpires.Equal(expires) {
		t.Fatalf("rotated expiry = %v, want %v", rotatedExpires, expires)
	}
	if _, _, err := TakeExecGrant(next, "user-a", "login-a", "page-a"); err != nil {
		t.Fatal(err)
	}
}

func TestRotateBeforeRevokeIsClearedAndStaleEpochCannotIssue(t *testing.T) {
	ResetForTest()
	const user = "rotate-epoch-user"
	plain, expires, err := IssueGrant(user, "login-a", ScopeExec, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	gotExpires, epoch, err := TakeExecGrant(plain, user, "login-a", "page-a")
	if err != nil {
		t.Fatal(err)
	}
	next, rotatedExpires, err := RotateExecGrant(user, "login-a", "page-a", gotExpires, epoch)
	if err != nil || next == "" {
		t.Fatal(err)
	}
	if !rotatedExpires.Equal(expires) {
		t.Fatalf("expiry = %v, want %v", rotatedExpires, expires)
	}
	RevokeUser(user)
	if _, _, err := TakeExecGrant(next, user, "login-a", "page-a"); err == nil {
		t.Fatal("grant rotated before revoke was still usable")
	}
	accounts.AdvanceUserSecurityEpochForTest(user)
	issued, _, err := RotateExecGrant(user, "login-a", "page-a", expires, epoch)
	if err == nil || issued != "" {
		t.Fatalf("stale epoch issued %q err=%v", issued, err)
	}
	freshPlain, freshExpires, err := IssueGrant(user, "login-a", ScopeExec, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	takenExpires, freshEpoch, err := TakeExecGrant(freshPlain, user, "login-a", "page-a")
	if err != nil {
		t.Fatal(err)
	}
	again, againExpires, err := RotateExecGrant(user, "login-a", "page-a", takenExpires, freshEpoch)
	if err != nil || again == "" {
		t.Fatal(err)
	}
	if !againExpires.Equal(freshExpires) {
		t.Fatalf("fresh rotation expiry = %v, want %v", againExpires, freshExpires)
	}
}

func TestRevokeWaitsForRotateCriticalSection(t *testing.T) {
	ResetForTest()
	const user = "rotate-lock-user"
	plain, _, err := IssueGrant(user, "login-a", ScopeExec, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	expires, epoch, err := TakeExecGrant(plain, user, "login-a", "page-a")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var revokeDone atomic.Bool
	var revokeWaited atomic.Bool
	rotateGrantHook = func() {
		accounts.AdvanceUserSecurityEpochForTest(user)
		started := make(chan struct{})
		go func() {
			close(started)
			RevokeUser(user)
			revokeDone.Store(true)
		}()
		<-started
		select {
		case <-time.After(40 * time.Millisecond):
		case <-release:
		}
		revokeWaited.Store(!revokeDone.Load())
	}
	defer func() { rotateGrantHook = nil }()
	issued, _, err := RotateExecGrant(user, "login-a", "page-a", expires, epoch)
	close(release)
	if err == nil || issued != "" {
		t.Fatalf("epoch bump inside the grant lock still issued %q", issued)
	}
	if !revokeWaited.Load() {
		t.Fatal("revoke finished while rotate still held the grant lock")
	}
	deadline := time.Now().Add(time.Second)
	for !revokeDone.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !revokeDone.Load() {
		t.Fatal("revoke did not finish after rotate released the grant lock")
	}
}

func TestOldGrantAfterEpochBumpCannotBeConsumedOrRotated(t *testing.T) {
	ResetForTest()
	const user = "old-grant-epoch"
	const session = "login-a"
	const page = "page-a"
	epoch := accounts.UserSecurityEpoch(user)
	plain, expires, err := IssueGrant(user, session, ScopeExec, page)
	if err != nil {
		t.Fatal(err)
	}
	remotePlain, _, err := IssueGrant(user, session, ScopeRemote, page)
	if err != nil {
		t.Fatal(err)
	}
	accounts.AdvanceUserSecurityEpochForTest(user)
	if _, _, err := TakeExecGrant(plain, user, session, page); !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("old exec grant consumed after epoch bump: %v", err)
	}
	if err := consumeGrant(remotePlain, user, session, ScopeRemote, page); !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("old remote grant consumed after epoch bump: %v", err)
	}
	current := accounts.UserSecurityEpoch(user)
	if current == epoch {
		t.Fatal("epoch did not move")
	}
	for _, candidate := range []uint64{epoch, current} {
		issued, _, err := RotateExecGrant(user, session, page, expires, candidate)
		if err == nil || issued != "" {
			t.Fatalf("late rotate at epoch %d issued %q err=%v", candidate, issued, err)
		}
	}
	next, nextExpires, err := IssueGrant(user, session, ScopeExec, page)
	if err != nil {
		t.Fatal(err)
	}
	takenExpires, takenEpoch, err := TakeExecGrant(next, user, session, page)
	if err != nil {
		t.Fatal(err)
	}
	if takenEpoch != current {
		t.Fatalf("new grant epoch = %d, want %d", takenEpoch, current)
	}
	rotated, rotatedExpires, err := RotateExecGrant(user, session, page, takenExpires, takenEpoch)
	if err != nil || rotated == "" {
		t.Fatal(err)
	}
	if !rotatedExpires.Equal(nextExpires) {
		t.Fatalf("new confirmation expiry = %v, want %v", rotatedExpires, nextExpires)
	}
}

func TestIssueGrantAtEpochRejectsAfterBump(t *testing.T) {
	ResetForTest()
	const user = "issue-epoch-user"
	epoch := accounts.UserSecurityEpoch(user)
	accounts.AdvanceUserSecurityEpochForTest(user)
	if _, _, err := IssueGrantAtEpoch(user, "login-a", ScopeExec, "page-a", epoch); !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("stale confirmation issued a grant: %v", err)
	}
}

func TestReauthRateLimit(t *testing.T) {
	ResetForTest()
	for i := 0; i < reauthMaxFailures; i++ {
		recordFailure("missing-user", "127.0.0.1")
	}
	if err := Reauthorize("missing-user", "x", "", "127.0.0.1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate limit error = %v", err)
	}
}

func TestReauthRateLimitSplitsUserAndIP(t *testing.T) {
	ResetForTest()
	for i := 0; i < reauthMaxFailures; i++ {
		recordFailure("user-a", "10.0.0.1")
	}
	if err := Reauthorize("user-b", "x", "", "10.0.0.1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("IP bucket should block other users: %v", err)
	}
	ResetForTest()
	for i := 0; i < reauthMaxFailures; i++ {
		recordFailure("user-a", "10.0.0.8")
	}
	if err := Reauthorize("user-a", "x", "", "10.0.0.9"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("user bucket should block other IPs: %v", err)
	}
	clearPairFailures("user-a", "10.0.0.8")
	if err := Reauthorize("user-a", "x", "", "10.0.0.9"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("clearing one pair must not reset the user bucket: %v", err)
	}
}

func TestConcurrentReauthFailuresShareBuckets(t *testing.T) {
	ResetForTest()
	var wg sync.WaitGroup
	wg.Add(20)
	for i := 0; i < 20; i++ {
		go func() {
			defer wg.Done()
			recordFailure("user-a", "10.0.0.4")
		}()
	}
	wg.Wait()
	if !throttled("user-a", "10.0.0.4") {
		t.Fatal("concurrent failures did not trip the shared buckets")
	}
}

func TestReauthWindowExpiryAndCapacity(t *testing.T) {
	ResetForTest()
	for i := 0; i < reauthMaxFailures; i++ {
		recordFailure("user-a", "10.0.0.5")
	}
	expired := time.Now().Add(-reauthWindow - time.Second)
	rateMu.Lock()
	for _, buckets := range []map[string]rateBucket{rateByUser, rateByIP, rateByPair} {
		for key, bucket := range buckets {
			bucket.windowStart = expired
			buckets[key] = bucket
		}
	}
	rateMu.Unlock()
	if throttled("user-a", "10.0.0.5") {
		t.Fatal("expired rate-limit window still blocked")
	}

	ResetForTest()
	for i := 0; i < rateLimitCapacity+32; i++ {
		recordFailure("user-"+strconv.Itoa(i), "10.0.0.6")
	}
	rateMu.Lock()
	defer rateMu.Unlock()
	if len(rateByUser) > rateLimitCapacity || len(rateByIP) > rateLimitCapacity || len(rateByPair) > rateLimitCapacity {
		t.Fatalf("rate limit maps grew past cap user=%d ip=%d pair=%d", len(rateByUser), len(rateByIP), len(rateByPair))
	}
}
