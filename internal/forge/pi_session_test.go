package forge

import "testing"

func TestPiUUIDv7SessionCanContribute(t *testing.T) {
	f := newToolsFixture(t)
	result := toolsRequest(f.handler, "issue_create", toolsAttemptV7, "", `{"title":"Real Pi session UUIDv7"}`)
	toolsSuccess(t, result)
}
