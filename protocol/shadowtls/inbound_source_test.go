package shadowtls

import (
	"os"
	"strings"
	"testing"
)

func TestInboundUsesCompatibleMapForServiceStorage(t *testing.T) {
	content, err := os.ReadFile("inbound.go")
	if err != nil {
		t.Fatalf("read inbound.go: %v", err)
	}

	source := string(content)
	if strings.Contains(source, "atomic.Pointer") {
		t.Fatalf("shadowtls inbound must not use atomic.Pointer for service storage")
	}
	if !strings.Contains(source, "compatible.Map") {
		t.Fatalf("shadowtls inbound must use compatible.Map for service storage")
	}
}
