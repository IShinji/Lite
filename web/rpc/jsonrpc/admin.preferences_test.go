package jsonrpc

import (
	"context"
	"testing"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/stretchr/testify/require"
)

func TestAccountPreferencePermission(t *testing.T) {
	require.True(t, rpc.CheckPrincipal(rpc.PrincipalFromRole(rpc.RoleAdmin), "admin:updateAccountPreferences"))
	require.False(t, rpc.CheckPrincipal(rpc.PrincipalFromRole(rpc.RoleGuest), "admin:updateAccountPreferences"))
}

func TestPublicGetMeReturnsAccountPreferences(t *testing.T) {
	ctx := rpc.NewContextWithMeta(context.Background(), &rpc.ContextMeta{
		User: &models.User{
			UUID:     "user-1",
			Username: "admin",
			Language: "zh-CN",
			Color:    "jade",
		},
	})

	result, rpcErr := publicGetMe(ctx, &rpc.JsonRpcRequest{})
	require.Nil(t, rpcErr)
	account, ok := result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "zh-CN", account["language"])
	require.Equal(t, "jade", account["color"])
	require.Equal(t, false, account["has_password"])
	require.Equal(t, "", account["avatar_url"])
}

func TestPublicGetMeReturnsAvatarURL(t *testing.T) {
	ctx := rpc.NewContextWithMeta(context.Background(), &rpc.ContextMeta{
		User: &models.User{
			UUID:          "user-1",
			Username:      "admin",
			AvatarVersion: "face_v1",
		},
	})
	result, rpcErr := publicGetMe(ctx, &rpc.JsonRpcRequest{})
	require.Nil(t, rpcErr)
	account, ok := result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "/api/admin/account/avatar/face_v1", account["avatar_url"])
}

func TestPublicGetMeGuestOmitsAvatarURL(t *testing.T) {
	result, rpcErr := publicGetMe(context.Background(), &rpc.JsonRpcRequest{})
	require.Nil(t, rpcErr)
	account, ok := result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, false, account["logged_in"])
	_, hasAvatar := account["avatar_url"]
	require.False(t, hasAvatar)
}

func TestAccountPreferenceUpdateRequiresUserSession(t *testing.T) {
	_, rpcErr := adminUpdateAccountPreferences(
		context.Background(),
		&rpc.JsonRpcRequest{Params: map[string]any{"color": "jade"}},
	)
	require.NotNil(t, rpcErr)
	require.Equal(t, rpc.PermissionDenied, rpcErr.Code)
}
