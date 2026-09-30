package jsonrpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/stretchr/testify/require"
)

func TestGetMeNilMetaDoesNotPanic(t *testing.T) {
	var result any
	var rpcErr *rpc.JsonRpcError
	require.NotPanics(t, func() {
		result, rpcErr = getMe(context.Background(), &rpc.JsonRpcRequest{})
	})
	require.Nil(t, rpcErr)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	var payload struct {
		LoggedIn bool `json:"logged_in"`
	}
	require.NoError(t, json.Unmarshal(raw, &payload))
	if payload.LoggedIn {
		t.Fatal("nil meta must be treated as a guest")
	}
}

func TestGetMeReturnsAvatarURLForLoggedInUser(t *testing.T) {
	ctx := rpc.NewContextWithMeta(context.Background(), &rpc.ContextMeta{
		Principal: rpc.PrincipalFromRole(rpc.RoleAdmin),
		User: &models.User{
			UUID:          "user-1",
			Username:      "admin",
			AvatarVersion: "face_v1",
		},
	})
	result, rpcErr := getMe(ctx, &rpc.JsonRpcRequest{})
	require.Nil(t, rpcErr)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	var payload struct {
		LoggedIn  bool   `json:"logged_in"`
		AvatarURL string `json:"avatar_url"`
	}
	require.NoError(t, json.Unmarshal(raw, &payload))
	require.True(t, payload.LoggedIn)
	require.Equal(t, "/api/admin/account/avatar/face_v1", payload.AvatarURL)
}

func TestIsLoginFromCtxNilMetaIsGuest(t *testing.T) {
	if isLoginFromCtx(context.Background()) {
		t.Fatal("missing RPC meta must not be treated as admin")
	}
	if isLoginFromCtx(rpc.NewContextWithMeta(context.Background(), nil)) {
		t.Fatal("nil meta argument must not be treated as admin")
	}
}
