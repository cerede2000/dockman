package ws

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckOriginRequiresSameOrGloballyValidatedOrigin(t *testing.T) {
	withoutOrigin := httptest.NewRequest("GET", "http://dockman.local/socket", nil)
	require.True(t, CheckOrigin(withoutOrigin))

	sameOrigin := httptest.NewRequest("GET", "http://dockman.local/socket", nil)
	sameOrigin.Header.Set("Origin", "http://dockman.local")
	require.True(t, CheckOrigin(sameOrigin))

	foreign := httptest.NewRequest("GET", "http://dockman.local/socket", nil)
	foreign.Header.Set("Origin", "https://admin.example")
	require.False(t, CheckOrigin(foreign))
	require.True(t, CheckOrigin(WithValidatedOrigin(foreign)))
}
