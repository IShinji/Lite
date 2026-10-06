package accounts

import "testing"

func TestSecurityEpochBumpsBeforeListeners(t *testing.T) {
	const user = "epoch-audit-user"
	before := UserSecurityEpoch(user)
	seen := before
	AddUserSecurityListener(func(uuid string) {
		if uuid == user {
			seen = UserSecurityEpoch(uuid)
		}
	})
	notifyUserSecurityChanged(user)
	if seen != before+1 {
		t.Fatalf("listener saw epoch %d, want %d", seen, before+1)
	}
	if UserSecurityEpoch(user) != before+1 {
		t.Fatalf("epoch = %d, want %d", UserSecurityEpoch(user), before+1)
	}
}
